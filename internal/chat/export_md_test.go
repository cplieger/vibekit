package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestRenderChatMarkdown_FullTranscript(t *testing.T) {
	c := &vibekit.Chat{
		ID:            "abc123",
		Name:          "My Chat",
		Model:         "claude-x",
		CurrentModeID: "vibe",
		CreatedAt:     1_700_000_000_000,
		UpdatedAt:     1_700_000_100_000,
		Messages: []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleUser, Content: "hello there", Ts: 1_700_000_000_000},
			{
				ID:        "m2",
				Role:      vibekit.RoleAssistant,
				Content:   "hi back",
				Reasoning: "let me think about this",
				Plan: []vibekit.PlanEntry{
					{Content: "step one", Status: vibekit.PlanCompleted},
					{Content: "step two", Status: vibekit.PlanInProgress},
					{Content: "step three", Status: vibekit.PlanPending},
				},
				ToolCalls: []vibekit.ToolCall{{
					ID:        "t1",
					Title:     "read file",
					Kind:      vibekit.ToolKindRead,
					Status:    vibekit.ToolCompleted,
					Output:    "file contents here",
					Input:     json.RawMessage(`{"path":"a.go"}`),
					Locations: []vibekit.ToolLocation{{Path: "a.go", Line: 3}},
				}},
				Ts: 1_700_000_050_000,
			},
			{ID: "m3", Role: vibekit.RoleEvent, EventKind: vibekit.EventInterrupted, Content: "interrupted by restart", Ts: 1_700_000_060_000},
		},
	}

	md := renderChatMarkdown(c)

	wants := []string{
		"# My Chat",
		"**Model:** claude-x",
		"**Mode:** vibe",
		"**Messages:** 3",
		"UTC", // timestamps rendered
		"## User",
		"hello there",
		"## Assistant",
		"hi back",
		"<summary>Reasoning</summary>",
		"let me think about this",
		"**Plan**",
		"- [x] step one",
		"- [ ] step two _(in progress)_",
		"- [ ] step three",
		"<summary>Tool: read file — completed</summary>",
		"`a.go:3`",
		"file contents here",
		`"path"`, // pretty-printed JSON input
		"## Event: interrupted",
		"interrupted by restart",
	}
	for _, want := range wants {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
}

func TestRenderChatMarkdown_EmptyMessages(t *testing.T) {
	md := renderChatMarkdown(&vibekit.Chat{ID: "c1", Name: "Empty"})
	if !strings.Contains(md, "# Empty") {
		t.Errorf("missing title: %q", md)
	}
	if !strings.Contains(md, "_No messages._") {
		t.Errorf("missing empty-state marker: %q", md)
	}
}

func TestRenderChatMarkdown_FallbackTitleAndOneLineName(t *testing.T) {
	// Empty name → fallback title; CR/LF in a name must not break the heading.
	if md := renderChatMarkdown(&vibekit.Chat{ID: "c1"}); !strings.Contains(md, "# Untitled chat") {
		t.Errorf("missing fallback title: %q", md)
	}
	md := renderChatMarkdown(&vibekit.Chat{ID: "c1", Name: "line1\nline2"})
	if !strings.Contains(md, "# line1 line2") {
		t.Errorf("newline in name not collapsed: %q", md)
	}
}

func TestRenderChatMarkdown_SanitisesToolOutput(t *testing.T) {
	// A hidden bidi-control codepoint in tool output must be scrubbed by the
	// sanitize.Output pass the renderer applies.
	c := &vibekit.Chat{
		ID: "c1", Name: "S",
		Messages: []vibekit.Message{{
			ID: "m1", Role: vibekit.RoleAssistant,
			ToolCalls: []vibekit.ToolCall{{
				ID: "t1", Title: "run", Status: vibekit.ToolCompleted,
				Output: "safe\u202etext", // U+202E RIGHT-TO-LEFT OVERRIDE
			}},
		}},
	}
	md := renderChatMarkdown(c)
	if strings.ContainsRune(md, '\u202e') {
		t.Errorf("tool output not sanitised; bidi control leaked into export")
	}
	if !strings.Contains(md, "safe") {
		t.Errorf("expected sanitised output to keep visible text: %q", md)
	}
}

func TestFencedCode_ExpandsFenceForEmbeddedBackticks(t *testing.T) {
	// Content containing a triple-backtick run must be wrapped in a longer
	// (4-backtick) fence so it can't close the block early.
	out := fencedCode("before ``` after", "")
	if !strings.HasPrefix(out, "````\n") {
		t.Errorf("want 4-backtick fence, got prefix %q", out[:min(8, len(out))])
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "````") {
		t.Errorf("closing fence not expanded: %q", out)
	}
}

func TestFencedCode_DefaultThreeBacktickFence(t *testing.T) {
	out := fencedCode("plain content", "json")
	if !strings.HasPrefix(out, "```json\n") {
		t.Errorf("want ```json fence, got %q", out[:min(10, len(out))])
	}
}

func TestMdTimestamp_ZeroIsEmpty(t *testing.T) {
	if got := mdTimestamp(0); got != "" {
		t.Errorf("mdTimestamp(0) = %q, want empty", got)
	}
	if got := mdTimestamp(1_700_000_000_000); !strings.HasSuffix(got, "UTC") {
		t.Errorf("mdTimestamp = %q, want UTC-suffixed datetime", got)
	}
}
