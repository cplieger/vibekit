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

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/pending"
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

	// Every key is chat-prefixed (fs-<chatID>-...) so two chats sharing a
	// per-bridge msg.ID can't collide in the process-wide store.
	const chatID api.ChatID = "c1"

	cases := []struct {
		name       string
		msg        *api.RPCResponse
		want       string
		wantPrefix string
	}{
		{
			name: "prefers param field (chat-prefixed)",
			msg:  &api.RPCResponse{ID: &id99, Params: mustJSON(t, map[string]any{"toolCallId": "from-param"})},
			want: "fs-c1-from-param",
		},
		{
			name: "falls back to RPC ID",
			msg:  &api.RPCResponse{ID: &id7, Params: mustJSON(t, map[string]any{})},
			want: "fs-c1-7",
		},
		{
			name: "falls back to RPC ID when param blank",
			msg:  &api.RPCResponse{ID: &id7, Params: mustJSON(t, map[string]any{"toolCallId": ""})},
			want: "fs-c1-7",
		},
		{
			name:       "sequence fallback when no ID available",
			msg:        &api.RPCResponse{ID: nil, Params: mustJSON(t, map[string]any{})},
			wantPrefix: "fs-c1-fallback-",
		},
		{
			name: "malformed params still returns non-empty",
			msg:  &api.RPCResponse{ID: &id3, Params: json.RawMessage(`not-json`)},
			want: "fs-c1-3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractToolCallID(chatID, tc.msg)
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
	line0 := 0
	limitMax := math.MaxInt
	limit100 := 100
	limit0 := 0
	limit2 := 2

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
		{"line zero keeps full content", "a\nb\nc\n", &line0, nil, "a\nb\nc\n"},
		{"limit zero does not narrow", "a\nb\nc\n", nil, &limit0, "a\nb\nc\n"},
		{"limit without line narrows from start", "a\nb\nc\nd\n", nil, &limit2, "a\nb\n"},
		{"midrange narrow within window", "a\nb\nc\nd\n", &line2, &limit2, "b\nc\n"},
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

// TestFsErrorIsRoutine pins the routine-vs-real classification used to
// decide whether an fs error is worth logging: only the sentinel
// errIgnored is routine; nil and real errors are not.
func TestFsErrorIsRoutine(t *testing.T) {
	t.Parallel()
	if got := fsErrorIsRoutine(errIgnored); !got {
		t.Errorf("fsErrorIsRoutine(errIgnored) = %v, want true", got)
	}
	if got := fsErrorIsRoutine(nil); got {
		t.Errorf("fsErrorIsRoutine(nil) = %v, want false", got)
	}
	if got := fsErrorIsRoutine(context.Canceled); got {
		t.Errorf("fsErrorIsRoutine(context.Canceled) = %v, want false", got)
	}
}

// TestSliceByLines_offsetLimitOvershootReturnsTail pins the read-window
// offset arithmetic. When the window starts past line 1 (start > 0) and the
// requested limit is larger than the number of lines that remain, the result
// must be the clamped tail of the file, never an over-read. The window end is
// narrowed to start+limit only while limit is smaller than the remaining-line
// count (end-start); flipping that subtraction to addition (end+start) pushes
// end past the slice length and panics instead of returning the tail. The
// existing table covers limit==remaining and limit>>remaining; this pins the
// just-past-remaining point where the sign of the offset math is observable.
func TestSliceByLines_offsetLimitOvershootReturnsTail(t *testing.T) {
	t.Parallel()
	line, limit := 2, 4 // start at line 2; only 2 lines remain in a 3-line file
	got := sliceByLines("a\nb\nc\n", &line, &limit)
	if got != "b\nc\n" {
		t.Errorf("sliceByLines(\"a\\nb\\nc\\n\", line=2, limit=4) = %q, want %q (tail, no over-read)",
			got, "b\nc\n")
	}
}
