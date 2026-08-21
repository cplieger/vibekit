package bridge

// The startup handshake's deadline.
//
// Bridge.Call carries no client-side deadline by design, and with a live
// subprocess its only other exits are the response arriving and the bridge
// dying. So before this budget existed, an initialize or a session/new that
// never answered blocked Start forever: the chat sat registered as starting, so
// every later Send answered 409 busy, and the singleflight key folded those
// callers onto the same wedged spawn. The user saw their own message and then
// nothing, with no error, no log line past `bridge spawned`, and no recovery
// short of closing the tab.
//
// These tests drive the expiry through the SHIPPED budgets, shortened for the
// duration of one test, rather than through a short parent context.
// context.WithTimeout takes the earlier of the two deadlines, so a short parent
// expires the handshake whether or not vibekit has a budget of its own — a test
// written that way passes with the budget deleted, which is the one thing it is
// supposed to catch. Shortening the budget instead means the assertion fails if
// the timer is removed. The budgets are package vars for this reason and no
// other; nothing in production writes them, so these tests must not run in
// parallel with anything that starts a bridge.
//
// NOT a synctest bubble, and that is deliberate: these bridges hold a live
// subprocess with open pipes, so a goroutine is parked indefinitely on an
// external FD and the fake clock can never advance. The bubble would hang until
// the go-test timeout instead of failing.
//
// Red-check note: deleting the timer makes these HANG rather than report, so the
// failure arrives as `panic: test timed out`. That is the correct signal here
// rather than a defect in the tests — the bug being guarded against IS an
// unbounded wait, so a guard for it cannot fail any faster than the thing it
// guards. The same shape as the shutdown-ordering guard in internal/agent.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// stallingFake writes a fake kiro-cli that answers every request EXCEPT the named
// method, which it reads and then ignores forever. That is the shape the budget
// exists for: the subprocess is alive and healthy, the pipe is open, and one
// response simply never comes.
func stallingFake(t *testing.T, dir, stallOn string) string {
	t.Helper()
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  if [ "$method" = "` + stallOn + `" ]; then
    continue
  fi
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-stall","configOptions":[{"id":"model","currentValue":"engine-default"}]}}\n' "$id"
      ;;
    session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-stall"}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	scriptPath := filepath.Join(dir, "stalling-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	return scriptPath
}

// budgetProbe is how long the test gives the handshake. Short enough to keep the
// suite fast, long enough to let a shell fake spawn and answer the requests that
// come BEFORE the stalled one.
const budgetProbe = 750 * time.Millisecond

// shortenBudgets replaces both shipped budgets with budgetProbe for the duration
// of one test and restores them afterwards. Restored via t.Cleanup rather than
// defer, so a subtest that fails its way out still puts them back.
func shortenBudgets(t *testing.T) {
	t.Helper()
	origHandshake, origReplay := handshakeBudget, replayBudget
	handshakeBudget, replayBudget = budgetProbe, budgetProbe
	t.Cleanup(func() { handshakeBudget, replayBudget = origHandshake, origReplay })
}

// TestStart_ExpiresOnAnUnansweredHandshake is the headline case, once per phase
// so the message names the right one.
//
// The subprocess is deliberately left healthy and the pipe open: this is not a
// dead-bridge test (b.done covers that, and Call already exits on it), it is the
// case where kiro-cli is alive and simply never answers.
func TestStart_ExpiresOnAnUnansweredHandshake(t *testing.T) {
	cases := map[string]struct {
		stallOn   string
		sessionID string
		wantPhase string
	}{
		"initialize never answers": {
			stallOn:   "initialize",
			wantPhase: "session start",
		},
		"session/new never answers": {
			stallOn:   "session/new",
			wantPhase: "session start",
		},
		// A resume gets the larger replayBudget, so the phase in the message has
		// to differ or an operator reading it cannot tell which ceiling applied.
		"session/load never answers": {
			stallOn:   "session/load",
			sessionID: "01HXYZ0000000000000000000A",
			wantPhase: "session resume",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			shortenBudgets(t)
			dir := t.TempDir()
			scriptPath := stallingFake(t, dir, tc.stallOn)

			b := New(scriptPath, dir)
			t.Cleanup(b.Stop)

			start := time.Now()
			// A parent with NO deadline: the budget under test has to be the
			// thing that fires, or the assertion proves only that a context is
			// honoured.
			err := b.Start(context.Background(), &vibekit.StartOpts{
				Lifetime: context.Background(), SessionID: tc.sessionID,
			})
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("Start returned nil on a handshake that was never answered")
			}
			// The classification has to survive the wrap, or a caller that wants
			// to tell a timeout from a refusal cannot.
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Start error = %v, want it to wrap context.DeadlineExceeded", err)
			}
			if elapsed > 10*time.Second {
				t.Errorf("Start took %v to give up, want about %v", elapsed, budgetProbe)
			}
			// The user-facing half. Each of these is a thing the raw
			// "context deadline exceeded" does not say.
			for _, want := range []string{tc.wantPhase, "Send again", "/api/health"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Start error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestStart_ReapsTheSubprocessOnExpiry: a start that gives up must not leave the
// kiro-cli tree running. Without this the budget would trade one wedge for
// another — the chat would recover and the process would leak, once per attempt.
//
// Asserted through NotifCh rather than by polling the pid: Stop calls cmd.Wait,
// so a successful teardown leaves no process entry at all, and a kill(pid,0)
// probe would be racing the reap it is trying to observe.
func TestStart_ReapsTheSubprocessOnExpiry(t *testing.T) {
	shortenBudgets(t)
	dir := t.TempDir()
	scriptPath := stallingFake(t, dir, "initialize")

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	if err := b.Start(context.Background(), &vibekit.StartOpts{Lifetime: context.Background()}); err == nil {
		t.Fatal("Start returned nil on a handshake that was never answered")
	}

	select {
	case <-b.NotifCh():
	case <-time.After(5 * time.Second):
		t.Fatal("an expired handshake left the bridge up; Stop did not run")
	}
}

// TestStart_FailsClosedWhenTheBudgetExpiresInsideTheAppliers is the subtle half,
// and the reason the check exists at all.
//
// newSession's appliers are best-effort: each logs and returns rather than
// failing session creation, because refusing to open a chat over a model
// preference is worse than opening it on the default. An expired budget turns
// that contract into a silent downgrade — a session on the wrong model and, worse,
// in autopilot for a chat the user marked supervised, since applySupervised is
// last in the sequence and its whole job is to make writes ask first.
//
// So newSession returns nil here and Start must still fail. The fake answers
// initialize and session/new and stalls only on the config-option call the model
// applier makes.
func TestStart_FailsClosedWhenTheBudgetExpiresInsideTheAppliers(t *testing.T) {
	shortenBudgets(t)
	dir := t.TempDir()
	scriptPath := stallingFake(t, dir, "session/set_config_option")

	b := New(scriptPath, dir)
	t.Cleanup(b.Stop)

	// Model differs from the currentValue the fake reports, which is what makes
	// applyInitialModel issue the call that stalls. Supervised is set too, so the
	// case carries the applier whose loss actually matters.
	err := b.Start(context.Background(), &vibekit.StartOpts{
		Lifetime: context.Background(), Model: "claude-opus-5", Supervised: true,
	})

	if err == nil {
		t.Fatal("Start succeeded with a session whose appliers never landed; " +
			"a supervised chat would be running in autopilot with only a log line to say so")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Start error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

// TestHandshakeBudgets_ResumeIsNotTighterThanStart pins the one relationship
// between the two constants that is a real invariant rather than a restatement of
// their values: a resume streams the whole prior transcript inside its window, so
// giving it the tighter ceiling would kill exactly the long replays it exists to
// admit.
func TestHandshakeBudgets_ResumeIsNotTighterThanStart(t *testing.T) {
	if replayBudget <= handshakeBudget {
		t.Errorf("replayBudget (%v) must exceed handshakeBudget (%v): a resume replays the "+
			"whole transcript inside its budget, which a fresh session never pays for",
			replayBudget, handshakeBudget)
	}
}

// TestHandshakeTimeout_LeavesOtherFailuresAlone: the wrap must be reachable only
// by an expiry. A refusal, a parse failure or a dead bridge already carries the
// cause a reader needs, and dressing it as a timeout would misattribute it.
func TestHandshakeTimeout_LeavesOtherFailuresAlone(t *testing.T) {
	for name, err := range map[string]error{
		"nil":            nil,
		"plain":          errors.New("session/new: Invalid params"),
		"cancelled":      context.Canceled,
		"bridge exited":  vibekit.ErrBridgeExited,
		"wrapped cancel": errors.New("session/load: " + context.Canceled.Error()),
	} {
		t.Run(name, func(t *testing.T) {
			got := handshakeTimeout(err, "session start", handshakeBudget)
			if got != err {
				t.Errorf("handshakeTimeout(%v) = %v, want the error returned unchanged", err, got)
			}
		})
	}
}
