package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func TestHookIDRoundTrip(t *testing.T) {
	// KAS ids are absolute paths with a "#hook-N" suffix — the base64url
	// handle must survive the '/' and '#' that break a URL path segment.
	cases := []string{
		"/workspace/.kiro/hooks/greet.json#hook-0",
		"/a b/c.json#hook-12",
		"simple",
	}
	for _, kasID := range cases {
		enc := encodeHookID(kasID)
		if strings.ContainsAny(enc, "/#? ") {
			t.Errorf("encoded id %q is not path-safe", enc)
		}
		got, err := decodeHookID(enc)
		if err != nil {
			t.Fatalf("decode(%q): %v", enc, err)
		}
		if got != kasID {
			t.Errorf("round-trip: got %q want %q", got, kasID)
		}
	}
}

func TestDecodeHookIDInvalid(t *testing.T) {
	if _, err := decodeHookID("not base64!!"); err == nil {
		t.Error("expected error decoding invalid base64url")
	}
}

func TestParseHookResult(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		success bool
		code    string
	}{
		{"success", `{"success":true}`, true, ""},
		{"failure", `{"success":false,"code":"hook_not_found","error":"nope"}`, false, "hook_not_found"},
		{"empty", ``, false, ""},
		{"garbage", `not json`, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := parseHookResult(json.RawMessage(tt.raw))
			if res.Success != tt.success {
				t.Errorf("Success: got %v want %v", res.Success, tt.success)
			}
			if res.Code != tt.code {
				t.Errorf("Code: got %q want %q", res.Code, tt.code)
			}
		})
	}
}

func TestHookCappedBuffer(t *testing.T) {
	var b hookCappedBuffer
	// Write just under, then over the cap.
	first := strings.Repeat("a", hookOutputCap-10)
	if _, err := b.Write([]byte(first)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if b.truncated {
		t.Fatal("truncated too early")
	}
	if _, err := b.Write([]byte(strings.Repeat("b", 100))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !b.truncated {
		t.Error("expected truncated after exceeding cap")
	}
	if b.buf.Len() != hookOutputCap {
		t.Errorf("buffer len: got %d want %d", b.buf.Len(), hookOutputCap)
	}
}

func TestHookScopeAndPath(t *testing.T) {
	work := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	h := &Hub{lifecycle: &lifecyclePlane{workDir: work}}
	tests := []struct {
		name      string
		abs       string
		wantScope string
		wantPath  string
	}{
		{"under workdir", filepath.Join(work, ".kiro", "hooks", "g.json"), hookScopeWorkspace, ".kiro/hooks/g.json"},
		{"empty", "", hookScopeWorkspace, ""},
		// A workspace directory whose name merely BEGINS with two dots is a
		// name, not a traversal (pathinside.RelEscapes is separator-precise),
		// so its hooks stay workspace-scoped with a relative editor target.
		{"dotdot-prefixed dir under workdir", filepath.Join(work, "..drafts", "g.kiro.hook"), hookScopeWorkspace, "..drafts/g.kiro.hook"},
		// kiro-cli 2.13 global hooks: $HOME/.kiro/hooks → global scope with a
		// ~-display path (no editor link; the HOME tree is editor-blocked).
		{"global under home", filepath.Join(home, ".kiro", "hooks", "g.json"), hookScopeGlobal, "~/.kiro/hooks/g.json"},
		{"outside both", "/etc/passwd", hookScopeGlobal, "/etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope, path := h.hookScopeAndPath(tt.abs)
			if scope != tt.wantScope || path != tt.wantPath {
				t.Errorf("hookScopeAndPath(%q): got (%q, %q) want (%q, %q)",
					tt.abs, scope, path, tt.wantScope, tt.wantPath)
			}
		})
	}
}

func TestHookScopeRankOrdersWorkspaceFirst(t *testing.T) {
	if hookScopeRank(hookScopeWorkspace) >= hookScopeRank(hookScopeGlobal) {
		t.Error("workspace hooks must sort before global hooks")
	}
}

func TestToHookInfo(t *testing.T) {
	work := t.TempDir()
	h := &Hub{lifecycle: &lifecyclePlane{workDir: work}}
	fp := filepath.Join(work, ".kiro", "hooks", "greet.json")

	cmd := h.toHookInfo(&kasHook{
		ID:     fp + "#hook-0",
		Name:   "greet",
		Action: kasHookAction{Type: actionRunCommand, Command: "echo hi", Timeout: 60},
		Meta:   kasHookMeta{Trigger: "Manual", Matcher: ".*", FilePath: fp, Enabled: true},
	})
	if cmd.ActionType != actionRunCommand || cmd.Command != "echo hi" || cmd.Timeout != 60 {
		t.Errorf("runCommand flatten wrong: %+v", cmd)
	}
	if cmd.Prompt != "" {
		t.Errorf("runCommand should not carry a prompt: %q", cmd.Prompt)
	}
	if cmd.Trigger != "Manual" || cmd.Matcher != ".*" || !cmd.Enabled {
		t.Errorf("meta flatten wrong: %+v", cmd)
	}
	if cmd.FilePath != ".kiro/hooks/greet.json" {
		t.Errorf("file path not workspace-relative: %q", cmd.FilePath)
	}
	if cmd.Scope != hookScopeWorkspace {
		t.Errorf("workspace hook scope: got %q", cmd.Scope)
	}
	if decoded, _ := decodeHookID(cmd.ID); decoded != fp+"#hook-0" {
		t.Errorf("id not the encoded KAS id: decoded %q", decoded)
	}

	// kiro-cli 2.13 global hook (~/.kiro/hooks): global scope + ~-display path.
	if home, err := os.UserHomeDir(); err == nil {
		gfp := filepath.Join(home, ".kiro", "hooks", "global.json")
		global := h.toHookInfo(&kasHook{
			ID:     gfp + "#hook-0",
			Name:   "global",
			Action: kasHookAction{Type: actionRunCommand, Command: "true"},
			Meta:   kasHookMeta{Trigger: "SessionStart", FilePath: gfp, Enabled: true},
		})
		if global.Scope != hookScopeGlobal {
			t.Errorf("global hook scope: got %q", global.Scope)
		}
		if global.FilePath != "~/.kiro/hooks/global.json" {
			t.Errorf("global hook display path: got %q", global.FilePath)
		}
	}

	agent := h.toHookInfo(&kasHook{
		ID:     fp + "#hook-1",
		Name:   "ask",
		Action: kasHookAction{Type: actionAskAgent, Prompt: "do it"},
		Meta:   kasHookMeta{Trigger: "Manual", Enabled: false, DisabledReason: "untrusted-workspace"},
	})
	if agent.ActionType != actionAskAgent || agent.Prompt != "do it" || agent.Command != "" {
		t.Errorf("askAgent flatten wrong: %+v", agent)
	}
	if agent.Enabled || agent.DisabledReason != "untrusted-workspace" {
		t.Errorf("askAgent meta wrong: %+v", agent)
	}
}

func TestRunHookCommand(t *testing.T) {
	h := &Hub{lifecycle: &lifecyclePlane{workDir: t.TempDir()}}
	ctx := context.Background()

	t.Run("captures output, exit 0", func(t *testing.T) {
		res := h.runHookCommand(ctx, "printf 'hello world'", 0)
		if !res.Ran || res.ExitCode != 0 {
			t.Fatalf("got %+v", res)
		}
		if !strings.Contains(res.Output, "hello world") {
			t.Errorf("output missing: %q", res.Output)
		}
	})

	t.Run("non-zero exit", func(t *testing.T) {
		res := h.runHookCommand(ctx, "exit 3", 0)
		if res.ExitCode != 3 {
			t.Errorf("exit code: got %d want 3", res.ExitCode)
		}
	})

	t.Run("empty command is a no-op", func(t *testing.T) {
		res := h.runHookCommand(ctx, "   ", 0)
		if !res.Ran || res.ExitCode != 0 || res.Output != "" {
			t.Errorf("got %+v", res)
		}
	})

	t.Run("output is capped + truncation-marked", func(t *testing.T) {
		res := h.runHookCommand(ctx, `head -c 200000 </dev/zero | tr '\0' x`, 0)
		if len(res.Output) > hookOutputCap+64 {
			t.Errorf("output not capped: len %d", len(res.Output))
		}
		if !strings.Contains(res.Output, "[output truncated]") {
			t.Errorf("missing truncation marker")
		}
	})

	t.Run("ANSI escapes are stripped (SanitizeOutput)", func(t *testing.T) {
		res := h.runHookCommand(ctx, `printf '\033[31mred\033[0m'`, 0)
		if strings.Contains(res.Output, "\033[") {
			t.Errorf("ANSI not stripped: %q", res.Output)
		}
		if !strings.Contains(res.Output, "red") {
			t.Errorf("text lost: %q", res.Output)
		}
	})
}

// TestAnswerExecuteHook covers the security gate on the A→C executeHook
// callback: a hook command runs ONLY while a user-initiated trigger is in
// flight (expectingHookExec); otherwise the callback is refused (cancelled).
func TestAnswerExecuteHook(t *testing.T) {
	execParams := json.RawMessage(`{"command":"echo hi","timeout":5,"hookName":"g"}`)

	t.Run("refuses when no trigger is in flight", func(t *testing.T) {
		rb := newRespondingBridge()
		ran := false
		us := &utilitySession{hooks: utilitySessionHooks{runHookCommand: func(context.Context, string, int) hookRunResult {
			ran = true
			return hookRunResult{}
		}}}
		id := int64(1)
		us.answerExecuteHook(rb, &api.RPCResponse{ID: &id, Method: methodKiroHooksExecuteHook, Params: execParams})
		if ran {
			t.Fatal("command ran without an in-flight trigger")
		}
		rb.respMu.Lock()
		defer rb.respMu.Unlock()
		m, _ := rb.response.result.(map[string]any)
		if m["cancelled"] != true {
			t.Errorf("expected cancelled:true, got %v", rb.response.result)
		}
	})

	t.Run("runs + captures result when a trigger is in flight", func(t *testing.T) {
		rb := newRespondingBridge()
		us := &utilitySession{hooks: utilitySessionHooks{runHookCommand: func(_ context.Context, cmd string, _ int) hookRunResult {
			return hookRunResult{Output: "ran:" + cmd, ExitCode: 0, Ran: true}
		}}}
		us.expectingHookExec.Store(true)
		id := int64(2)
		us.answerExecuteHook(rb, &api.RPCResponse{ID: &id, Method: methodKiroHooksExecuteHook, Params: execParams})
		if run := us.lastHookRun.Load(); run == nil || run.Output != "ran:echo hi" {
			t.Fatalf("lastHookRun not captured: %+v", run)
		}
		rb.respMu.Lock()
		defer rb.respMu.Unlock()
		m, _ := rb.response.result.(map[string]any)
		if m["output"] != "ran:echo hi" {
			t.Errorf("executeHook response output wrong: %v", rb.response.result)
		}
	})
}
