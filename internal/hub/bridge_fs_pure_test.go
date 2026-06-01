package hub

// Pure-function tests extracted from bridge_fs.go. These exercise the
// slicing, truncation, message-count, and tool-call-id helpers that
// bridge_fs uses on every agent fs/* round trip — and that the batch-15
// review flagged as undertested.

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/pending"
)

func TestTruncateForStaging_Table(t *testing.T) {
	t.Parallel()
	small := strings.Repeat("a", 8)
	cap := strings.Repeat("b", pending.Cap)
	over := strings.Repeat("c", pending.Cap+100)

	cases := []struct {
		name      string
		oldIn     string
		newIn     string
		wantOld   int
		wantNew   int
		truncated bool
	}{
		{"both empty", "", "", 0, 0, false},
		{"both small", small, small, len(small), len(small), false},
		{"both at cap", cap, cap, pending.Cap, pending.Cap, false},
		{"old over only", over, small, pending.Cap, len(small), true},
		{"new over only", small, over, len(small), pending.Cap, true},
		{"both over", over, over, pending.Cap, pending.Cap, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOld, gotNew, gotTruncated := truncateForStaging(tc.oldIn, tc.newIn)
			if len(gotOld) != tc.wantOld {
				t.Errorf("oldText len = %d, want %d", len(gotOld), tc.wantOld)
			}
			if len(gotNew) != tc.wantNew {
				t.Errorf("newText len = %d, want %d", len(gotNew), tc.wantNew)
			}
			if gotTruncated != tc.truncated {
				t.Errorf("truncated = %v, want %v", gotTruncated, tc.truncated)
			}
			if len(gotOld) > pending.Cap {
				t.Errorf("oldText len %d exceeds pending.Cap %d", len(gotOld), pending.Cap)
			}
			if len(gotNew) > pending.Cap {
				t.Errorf("newText len %d exceeds pending.Cap %d", len(gotNew), pending.Cap)
			}
		})
	}
}

func TestExtractToolCallID(t *testing.T) {
	t.Parallel()
	id99 := int64(99)
	id7 := int64(7)
	id3 := int64(3)

	cases := []struct {
		name       string
		msg        *api.RPCResponse
		want       string
		wantPrefix string
	}{
		{
			name: "prefers param field",
			msg:  &api.RPCResponse{ID: &id99, Params: mustJSON(t, map[string]any{"toolCallId": "from-param"})},
			want: "from-param",
		},
		{
			name: "falls back to RPC ID",
			msg:  &api.RPCResponse{ID: &id7, Params: mustJSON(t, map[string]any{})},
			want: "fs-7",
		},
		{
			name: "falls back to RPC ID when param blank",
			msg:  &api.RPCResponse{ID: &id7, Params: mustJSON(t, map[string]any{"toolCallId": ""})},
			want: "fs-7",
		},
		{
			name:       "time-based when no ID available",
			msg:        &api.RPCResponse{ID: nil, Params: mustJSON(t, map[string]any{})},
			wantPrefix: "fs-",
		},
		{
			name: "malformed params still returns non-empty",
			msg:  &api.RPCResponse{ID: &id3, Params: json.RawMessage(`not-json`)},
			want: "fs-3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractToolCallID(tc.msg)
			switch {
			case tc.wantPrefix != "":
				if !strings.HasPrefix(got, tc.wantPrefix) || got == tc.wantPrefix {
					t.Errorf("extractToolCallID = %q, want prefix %q with suffix", got, tc.wantPrefix)
				}
			default:
				if got != tc.want {
					t.Errorf("extractToolCallID = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestCurrentMessageCount(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []api.Message{
			{Role: api.RoleUser, Content: "a"},
			{Role: api.RoleAssistant, Content: "b"},
			{Role: api.RoleUser, Content: "c"},
		}
		return true
	})

	if got := h.currentMessageCount(context.Background(), "c1"); got != 3 {
		t.Errorf("currentMessageCount(existing) = %d, want 3", got)
	}
	if got := h.currentMessageCount(context.Background(), "no-such-chat"); got != 0 {
		t.Errorf("currentMessageCount(missing) = %d, want 0", got)
	}
}

// TestSliceByLines_Table consolidates the pure-function edge cases for
// sliceByLines into a single table-driven test.
func TestSliceByLines_Table(t *testing.T) {
	t.Parallel()
	line2 := 2
	line99 := 99
	limitMax := math.MaxInt
	limit100 := 100

	cases := []struct {
		name    string
		content string
		line    *int
		limit   *int
		want    string
	}{
		{"nil line and limit returns whole", "a\nb\n", nil, nil, "a\nb\n"},
		{"start beyond end returns empty", "a\nb\n", &line99, nil, ""},
		{"limit larger than remaining returns tail", "a\nb\nc\n", &line2, &limit100, "b\nc\n"},
		{"integer overflow does not panic", "a\nb\nc\n", &line2, &limitMax, "b\nc\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sliceByLines(tc.content, tc.line, tc.limit)
			if got != tc.want {
				t.Errorf("sliceByLines = %q, want %q", got, tc.want)
			}
		})
	}
}

// FuzzSliceByLines exercises the line-slicing parser with arbitrary inputs.
// Invariant: for any (content, line, limit) where line >= 1 and limit >= 1,
// the output is a substring of content (or empty).
func FuzzSliceByLines(f *testing.F) {
	f.Add("a\nb\nc\n", 2, 2)
	f.Add("a\nb\n", 99, 1)
	f.Add("a\nb\nc\n", 2, math.MaxInt)
	f.Add("", 1, 1)
	f.Add("single", 1, 1)

	f.Fuzz(func(t *testing.T, content string, line, limit int) {
		if line < 1 || limit < 1 {
			return
		}
		lp := &line
		limp := &limit
		got := sliceByLines(content, lp, limp)
		if len(got) > len(content) {
			t.Errorf("output longer than input: len(got)=%d > len(content)=%d", len(got), len(content))
		}
		if got != "" && !strings.Contains(content, got) {
			t.Errorf("output %q is not a substring of content %q", got, content)
		}
	})
}
