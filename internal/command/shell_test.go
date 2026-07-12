package command

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestShellCappedBuffer(t *testing.T) {
	tests := []struct {
		name      string
		writes    [][]byte
		wantLen   int
		wantTrunc bool
	}{
		{
			name:      "under cap",
			writes:    [][]byte{[]byte("hello")},
			wantLen:   5,
			wantTrunc: false,
		},
		{
			name:      "exactly at cap",
			writes:    [][]byte{bytes.Repeat([]byte("x"), ShellOutputCap)},
			wantLen:   ShellOutputCap,
			wantTrunc: false,
		},
		{
			name:      "single write over cap",
			writes:    [][]byte{bytes.Repeat([]byte("x"), ShellOutputCap+100)},
			wantLen:   ShellOutputCap,
			wantTrunc: true,
		},
		{
			name: "multiple writes accumulating past cap",
			writes: [][]byte{
				bytes.Repeat([]byte("a"), ShellOutputCap-10),
				bytes.Repeat([]byte("b"), 20),
			},
			wantLen:   ShellOutputCap,
			wantTrunc: true,
		},
		{
			name: "write after cap already reached",
			writes: [][]byte{
				bytes.Repeat([]byte("a"), ShellOutputCap),
				[]byte("extra"),
			},
			wantLen:   ShellOutputCap,
			wantTrunc: true,
		},
		{
			// An empty write when the buffer is exactly full must still
			// mark Truncated (remaining == 0 takes the cap-reached branch,
			// not the len(p) <= remaining fast path).
			name: "empty write at exactly-full buffer",
			writes: [][]byte{
				bytes.Repeat([]byte("x"), ShellOutputCap),
				{},
			},
			wantLen:   ShellOutputCap,
			wantTrunc: true,
		},
		{
			name:      "empty write",
			writes:    [][]byte{{}},
			wantLen:   0,
			wantTrunc: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf ShellCappedBuffer
			for _, w := range tc.writes {
				n, err := buf.Write(w)
				if err != nil {
					t.Fatalf("Write returned error: %v", err)
				}
				if n != len(w) {
					t.Fatalf("Write returned n=%d, want %d", n, len(w))
				}
			}
			if buf.Buf.Len() != tc.wantLen {
				t.Errorf("Buf.Len() = %d, want %d", buf.Buf.Len(), tc.wantLen)
			}
			if buf.Truncated != tc.wantTrunc {
				t.Errorf("Truncated = %v, want %v", buf.Truncated, tc.wantTrunc)
			}
		})
	}
}

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

func (fakeBusyBridge) Call(context.Context, string, any) (*api.RPCResponse, error) {
	return nil, nil
}
func (fakeBusyBridge) Notify(context.Context, string, any) error        { return nil }
func (fakeBusyBridge) Respond(context.Context, int64, any, error) error { return nil }
func (fakeBusyBridge) SessionID() api.SessionID                         { return "" }
func (fakeBusyBridge) TryAcquireForPrompt() bool                        { return false }
func (fakeBusyBridge) ReleaseAfterPrompt()                              {}
func (fakeBusyBridge) SetLastActive()                                   {}
func (fakeBusyBridge) SetPrompting()                                    {}
func (fakeBusyBridge) IsPrimed() bool                                   { return true }
func (fakeBusyBridge) SetPrimed()                                       {}

// busyGuardDeps overrides GetBridge on the bench stub to hand back a
// bridge whose turn lock is already held.
type busyGuardDeps struct {
	*benchDeps
	bridge Bridge
}

func (d *busyGuardDeps) GetBridge(api.ChatID) Bridge { return d.bridge }

// TestHandleShellInterception_BusyReturns409 pins the busy-guard (MED
// bug): when a bridge exists and its turn lock can't be acquired (a real
// streaming turn is in flight), !cmd returns 409 so the client queue
// drains it — instead of running and broadcasting a mid-turn turn_ended.
// The 409 short-circuits before any ChatStore access, so the nil-store
// bench stub is sufficient.
func TestHandleShellInterception_BusyReturns409(t *testing.T) {
	deps := &busyGuardDeps{benchDeps: newBenchDeps(), bridge: fakeBusyBridge{}}
	d := New(deps)
	w := httptest.NewRecorder()
	cmd := &api.ClientCommand{Type: "prompt", RequestID: "r1", ChatID: "c1"}
	p := &api.PromptCommand{Text: "!echo hi", MessageID: "m-1"}

	HandleShellInterception(d, deps, context.Background(), w, cmd, p)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (busy)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "busy") {
		t.Errorf("body = %q, want it to mention busy", w.Body.String())
	}
}
