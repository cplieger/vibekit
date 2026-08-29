package command

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/testsupport"
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

// heldAdmissionDeps overrides the admission try on the bench stub to refuse, the
// state a chat is in while ANY holder — a prompt's blocked spawn included — owns
// the slot.
type heldAdmissionDeps struct {
	*benchDeps
	tried int
}

func (d *heldAdmissionDeps) TryReserveTurn(vibekit.ChatID, vibekit.TurnOpenSource) bool {
	d.tried++
	return false
}

// TestHandleShellInterception_HeldAdmissionReturns409Immediately pins the shell
// door's admission: a TRY against the same per-chat reservation a prompt takes,
// never a wait — `!echo hi` during a prompt's blocked spawn answers 409 at
// once. The refusal short-circuits before any ChatStore access, so the
// nil-store bench stub is sufficient; the try counter is the load-bearing
// assertion that the door went through the reservation rather than the bridge
// slot, which a spawn-blocked chat does not hold.
func TestHandleShellInterception_HeldAdmissionReturns409Immediately(t *testing.T) {
	deps := &heldAdmissionDeps{benchDeps: newBenchDeps()}
	cmd := &vibekit.ClientCommand{Type: "prompt", ChatID: "c1"}
	p := &vibekit.PromptCommand{Text: "!echo hi", MessageID: "m-1"}

	start := time.Now()
	_, err := HandleShellInterception(t.Context(), promptRolesOf(deps), cmd, p)
	elapsed := time.Since(start)

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (busy)", statusOf(err))
	}
	if !strings.Contains(errText(err), "busy") {
		t.Errorf("body = %q, want it to mention busy", errText(err))
	}
	if deps.tried != 1 {
		t.Errorf("TryReserveTurn called %d times, want 1: the shell door admits through the reservation", deps.tried)
	}
	// A TRY, never a wait: the refusal is immediate, not held for the prompt
	// admission budget. The bound is generous — the point is "no deliberate
	// wait", not a latency budget.
	if elapsed > time.Second {
		t.Errorf("refusal took %v, want an immediate answer", elapsed)
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

// A `!cmd` on a fresh chat names the chat after the command, on the same rule
// the prompt path uses: only the first message, only while the chat still
// carries the default name. The ellipsis is appended exactly when the command
// text was cut, so a name is never marked as truncated when it is complete.
func TestAppendShellUserMessage_DerivesTheChatNameFromTheCommand(t *testing.T) {
	const eighty = "12345678901234567890123456789012345678901234567890123456789012345678901234567890"
	cases := []struct {
		name     string
		seed     bool
		text     string
		wantName string
	}{
		{name: "the command becomes the name", text: "!go test ./...", wantName: "!go test ./..."},
		{name: "eighty runes is the last length kept whole", text: eighty, wantName: eighty},
		{name: "longer text is cut and marked", text: eighty + " and then some more", wantName: eighty + "..."},
		{name: "a chat that already has a name keeps it", seed: true, text: "!go test ./...", wantName: "a chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			if tc.seed {
				seedEmptyChat(t, store, "c1")
			}
			deps := &storeDeps{benchDeps: newBenchDeps(), store: store}
			msg := &vibekit.Message{ID: "m-1", Role: vibekit.RoleUser, Content: tc.text}

			persisted, err := appendShellUserMessage(t.Context(), deps, deps, "c1", msg, tc.text)
			if err != nil {
				t.Fatalf("appendShellUserMessage: %v", err)
			}
			if !persisted {
				t.Fatal("persisted = false, want the message stored")
			}

			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat vanished")
			}
			if c.Name != tc.wantName {
				t.Errorf("name after a %d-byte command = %q, want %q", len(tc.text), c.Name, tc.wantName)
			}
		})
	}
}

// The interception's summary line is what an operator reads after the fact, so
// the two facts it carries about a command that SUCCEEDED have to be true: the
// command did not exit with an error, and persisting its output did not fail.
// A summary that reports a failure the run did not have sends the next reader
// looking at the wrong thing.
func TestHandleShellInterception_SuccessLogsTheOutcomeHonestly(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not available: %v", err)
	}
	logs := captureLogs(t)
	deps := &shellStoreDeps{benchDeps: newBenchDeps()}
	cmd := &vibekit.ClientCommand{Type: "prompt", ChatID: "c1"}
	p := &vibekit.PromptCommand{Text: "!echo hi", MessageID: "m-1"}

	if _, err := HandleShellInterception(t.Context(), promptRolesOf(deps), cmd, p); err != nil {
		t.Fatalf("HandleShellInterception = %v, want it to succeed", err)
	}

	out := logs.String()
	if !strings.Contains(out, "exit_error=false") {
		t.Errorf("summary does not report exit_error=false: %s", out)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("a successful interception logged an error: %s", out)
	}
}
