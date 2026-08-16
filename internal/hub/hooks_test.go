package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	// No timeout in the fixture: KAS's list projection emits {type, command}
	// only, so a case feeding one asserted a wire shape that cannot arrive.
	cmd := h.toHookInfo(&kasHook{
		ID:     fp + "#hook-0",
		Name:   "greet",
		Action: kasHookAction{Type: actionRunCommand, Command: "echo hi"},
		Meta:   kasHookMeta{Trigger: "Manual", Matcher: ".*", FilePath: fp, Enabled: true},
	})
	if cmd.ActionType != actionRunCommand || cmd.Command != "echo hi" {
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
