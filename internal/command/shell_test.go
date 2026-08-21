package command

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestShellFence verifies the fence is sized one backtick longer than the
// longest backtick run in the body, so command output containing a ```
// run can never close the fence early (the MED bug/ux finding).
func TestShellFence(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int // expected fence length in backticks
	}{
		{"no backticks", "plain output", 3},
		{"single backtick", "a`b", 3},
		{"double backtick", "a``b", 3},
		{"triple backtick run", "```", 4},
		{"code block in output", "text\n```\ncode\n```\nmore", 4},
		{"five backtick run", "`````", 6},
		{"runs split by newline stay separate", "``\n``", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fence := shellFence(tc.body)
			if len(fence) != tc.want {
				t.Errorf("shellFence(%q) len = %d, want %d", tc.body, len(fence), tc.want)
			}
			// The fence must be strictly longer than any backtick run in
			// the body, so the body can never contain (and thus close
			// with) the fence sequence itself.
			if strings.Contains(tc.body, fence) {
				t.Errorf("body %q contains fence %q — output could close it early", tc.body, fence)
			}
		})
	}
}

// TestRenderShellResult_CodeFenceInOutput pins that a command whose output
// itself contains a ``` fence (e.g. !cat README.md) is wrapped in a longer
// fence and preserved verbatim rather than breaking out into Markdown.
func TestRenderShellResult_CodeFenceInOutput(t *testing.T) {
	output := "# README\n```go\nfunc main() {}\n```\ndone"
	got := renderShellResult(output, nil, false)

	// The whole result opens and closes with a 4-backtick fence (one more
	// than the 3-backtick run inside the output).
	if !strings.HasPrefix(got, "````\n") {
		t.Errorf("result should open with a 4-backtick fence:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n````") {
		t.Errorf("result should close with a 4-backtick fence:\n%s", got)
	}
	// The original output, including its inner ``` run, is preserved.
	if !strings.Contains(got, output) {
		t.Errorf("output not preserved verbatim:\n%s", got)
	}
	// A successful command still shows its exit status.
	if !strings.Contains(got, "[exit 0]") {
		t.Errorf("missing exit status line:\n%s", got)
	}
}

// TestShellStatusLine covers the timeout message (LOW) and the exit-code
// line (LOW): a timed-out command shows a clear message instead of the
// opaque "signal: killed", and a normal exit shows its code.
func TestShellStatusLine(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		// runErr is the OS-level "signal: killed"; the timeout flag must
		// override it with a clear, human message.
		got := shellStatusLine(errors.New("signal: killed"), true)
		want := "[command timed out after 30s]"
		if got != want {
			t.Errorf("shellStatusLine(timeout) = %q, want %q", got, want)
		}
	})

	t.Run("success", func(t *testing.T) {
		if got := shellStatusLine(nil, false); got != "[exit 0]" {
			t.Errorf("shellStatusLine(nil) = %q, want %q", got, "[exit 0]")
		}
	})

	t.Run("nonzero exit code", func(t *testing.T) {
		// Produce a genuine *exec.ExitError with a known non-zero code.
		runErr := exec.Command("sh", "-c", "exit 3").Run()
		if runErr == nil {
			t.Fatal("expected a non-nil error from 'exit 3'")
		}
		if got := shellStatusLine(runErr, false); got != "[exit 3]" {
			t.Errorf("shellStatusLine(exit 3) = %q, want %q", got, "[exit 3]")
		}
	})
}

// fakeBusyBridge is a command.Bridge whose prompt lock is always
// contended, used to exercise the !cmd busy-guard's 409 path.
type fakeBusyBridge struct{}

func (fakeBusyBridge) Call(context.Context, string, any) (*vibekit.RPCResponse, error) {
	return nil, nil
}
func (fakeBusyBridge) Notify(context.Context, string, any) error        { return nil }
func (fakeBusyBridge) Respond(context.Context, int64, any, error) error { return nil }
func (fakeBusyBridge) SessionID() vibekit.SessionID                     { return "" }
func (fakeBusyBridge) TryAcquireForPrompt() bool                        { return false }
func (fakeBusyBridge) ReleaseAfterPrompt()                              {}
func (fakeBusyBridge) BeginPromptCall(context.CancelFunc) uint64        { return 0 }
func (fakeBusyBridge) EndPromptCall()                                   {}
func (fakeBusyBridge) PromptGeneration() uint64                         { return 0 }
func (fakeBusyBridge) ArmCancelGrace(uint64, time.Duration) bool        { return false }

// busyGuardDeps overrides Bridge on the bench stub to hand back a
// bridge whose turn lock is already held.
type busyGuardDeps struct {
	*benchDeps
	bridge Bridge
}

func (d *busyGuardDeps) Bridge(vibekit.ChatID) Bridge { return d.bridge }

// TestHandleShellInterception_BusyReturns409 pins the busy-guard (MED
// bug): when a bridge exists and its turn lock can't be acquired (a real
// streaming turn is in flight), !cmd returns 409 so the client queue
// drains it — instead of running and broadcasting a mid-turn turn_ended.
// The 409 short-circuits before any ChatStore access, so the nil-store
// bench stub is sufficient.
func TestHandleShellInterception_BusyReturns409(t *testing.T) {
	deps := &busyGuardDeps{benchDeps: newBenchDeps(), bridge: fakeBusyBridge{}}
	cmd := &vibekit.ClientCommand{Type: "prompt", ChatID: "c1"}
	p := &vibekit.PromptCommand{Text: "!echo hi", MessageID: "m-1"}

	_, err := HandleShellInterception(t.Context(), promptRolesOf(deps), cmd, p)

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (busy)", statusOf(err))
	}
	if !strings.Contains(errText(err), "busy") {
		t.Errorf("body = %q, want it to mention busy", errText(err))
	}
}

// shellStoreDeps is the benchDeps double with a chat store that actually invokes
// its mutate callback, which is what the shell interception needs to get past
// its "was the user message persisted" gate.
type shellStoreDeps struct {
	*benchDeps
	appended []vibekit.Message
}

func (d *shellStoreDeps) Mutate(_ context.Context, _ vibekit.ChatID, mutate func(*vibekit.Chat, bool) bool) error {
	mutate(&vibekit.Chat{}, false)
	return nil
}

func (d *shellStoreDeps) Get(context.Context, vibekit.ChatID) (*vibekit.Chat, bool) {
	return &vibekit.Chat{}, true
}

func (d *shellStoreDeps) AppendMessage(_ context.Context, _ vibekit.ChatID, m *vibekit.Message) error {
	d.appended = append(d.appended, *m)
	return nil
}

// TestHandleShellInterception_TruncatedOutputIsStillASuccessfulCommand pins the
// capture contract AT THE SITE, which the buffer's own table test never did.
//
// A `!cmd` whose output crosses ShellOutputCap must report the command's real
// outcome — exit 0 — and label the output partial. The failure this guards is the
// one procout's package doc is written about: a capping writer that reports the
// bytes it KEPT makes os/exec's io.Copy return io.ErrShortWrite, which Cmd.Wait
// hands back as the command's error even though the child exited 0, so a
// successful chatty command renders as "[error: short write]" (or, when the child
// is still writing, as "[error: signal: broken pipe]" with the process killed
// part-way). Both shapes are measured in procout's own regression test; this one
// checks the shell path is wired to the type that has them.
func TestHandleShellInterception_TruncatedOutputIsStillASuccessfulCommand(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not available: %v", err)
	}
	deps := &shellStoreDeps{benchDeps: newBenchDeps()}
	cmd := &vibekit.ClientCommand{Type: "prompt", ChatID: "c1"}
	// 1,100,000 bytes, past the 1 MiB cap, in 1100 printf calls (measured at
	// 4 ms). The unit is 1000 rather than 1024 deliberately: 1 MiB is an exact
	// multiple of both 1024 and io.Copy's 32 KiB buffer, so an aligned producer
	// fills the buffer to exactly the cap and the STRADDLING write — the one
	// that has to report the full length rather than the kept length — is never
	// reached. Red-checked: at 1024 the kept-bytes mutant passes this test, at
	// 1000 it fails it.
	p := &vibekit.PromptCommand{
		Text:      `!i=0; while [ $i -lt 1100 ]; do printf "%01000d" 0; i=$((i+1)); done`,
		MessageID: "m-1",
	}

	if _, err := HandleShellInterception(t.Context(), promptRolesOf(deps), cmd, p); err != nil {
		t.Fatalf("HandleShellInterception: %v", err)
	}
	if len(deps.appended) != 1 {
		t.Fatalf("appended %d assistant messages, want 1", len(deps.appended))
	}
	body := deps.appended[0].Content

	if !strings.Contains(body, "[exit 0]") {
		tail := body[max(len(body)-120, 0):]
		t.Errorf("a command that exited 0 was not reported as such; body tail = %q", tail)
	}
	if !strings.Contains(body, "[output truncated at 1 MiB]") {
		t.Error("output crossed the cap but was not labelled truncated")
	}
	// The kept prefix plus the fence, the status line and the note — not the
	// whole 1100 KiB the child produced.
	if len(body) > ShellOutputCap+1024 {
		t.Errorf("assistant body is %d bytes, want at most the cap plus the trailer", len(body))
	}
}
