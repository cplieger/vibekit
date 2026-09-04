package chat

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// storeWith returns a store holding one chat with these messages.
func storeWith(t *testing.T, msgs []vibekit.Message) *Store {
	t.Helper()
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = msgs
		return true
	})
	return s
}

// windowedCall serves the newest transcript page and returns its one tool call.
func windowedCall(t *testing.T, msgs []vibekit.Message) vibekit.ToolCall {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1", nil)
	rec := httptest.NewRecorder()
	NewRouter(storeWith(t, msgs)).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Setup: transcript code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Messages []vibekit.Message `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Setup: unmarshal: %v", err)
	}
	if len(got.Messages) != 1 || len(got.Messages[0].ToolCalls) != 1 {
		t.Fatalf("Setup: want one message with one tool call, got %d messages", len(got.Messages))
	}
	return got.Messages[0].ToolCalls[0]
}

// callMessage wraps one tool call in an assistant message.
func callMessage(tc vibekit.ToolCall) vibekit.Message {
	return vibekit.Message{
		ID:        "m1",
		Role:      vibekit.RoleAssistant,
		Ts:        100,
		ToolCalls: []vibekit.ToolCall{tc},
	}
}

// TestTranscript_SmallToolCallIsSentWhole is the case the preview must not
// touch: the great majority of tool calls are small, and paying a second round
// trip for one would make the ladder cost more than it saves.
func TestTranscript_SmallToolCallIsSentWhole(t *testing.T) {
	in := vibekit.ToolCall{
		ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute,
		Status: vibekit.ToolCompleted,
		Output: "all done\n",
		Input:  json.RawMessage(`{"command":"ls"}`),
	}
	got := windowedCall(t, []vibekit.Message{callMessage(in)})
	if got.HasFull {
		t.Errorf("has_full = true, want false for a %d-byte output", len(in.Output))
	}
	if got.Output != in.Output {
		t.Errorf("output = %q, want %q", got.Output, in.Output)
	}
	if string(got.Input) != string(in.Input) {
		t.Errorf("input = %s, want %s", got.Input, in.Input)
	}
	if got.OutputBytes != 0 || got.DiffCount != 0 {
		t.Errorf("output_bytes = %d, diff_count = %d, want 0 and 0 when the value is whole",
			got.OutputBytes, got.DiffCount)
	}
}

// TestTranscript_BigOutputIsWindowedFromBothEnds pins where the cut is: a
// command's first lines say what it did and its last say how it ended, so a
// prefix would lose the error.
func TestTranscript_BigOutputIsWindowedFromBothEnds(t *testing.T) {
	var b strings.Builder
	for i := range 400 {
		if i == 0 {
			b.WriteString("FIRST\n")
			continue
		}
		if i == 399 {
			b.WriteString("LAST\n")
			continue
		}
		b.WriteString(strings.Repeat("m", 60) + "\n")
	}
	full := b.String()
	got := windowedCall(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute,
		Status: vibekit.ToolCompleted, Output: full,
	})})

	if !got.HasFull {
		t.Fatal("has_full = false, want true for a windowed output")
	}
	if got.OutputBytes != len(full) {
		t.Errorf("output_bytes = %d, want %d (the FULL output's length)", got.OutputBytes, len(full))
	}
	if len(got.Output) >= len(full) {
		t.Errorf("output is %d bytes, want fewer than the full %d", len(got.Output), len(full))
	}
	if !strings.HasPrefix(got.Output, "FIRST\n") {
		t.Errorf("output does not start with the first line: %q", got.Output[:min(40, len(got.Output))])
	}
	if !strings.HasSuffix(got.Output, "LAST\n") {
		t.Error("output does not end with the last line, so a failing command's error is lost")
	}
}

// TestTranscript_OneEnormousLineIsStillBounded is the case a line budget alone
// cannot reach, and the one that leaves the tail unbounded without it: the live
// volume holds a 9.1 MB single message and a 3.8 MB single message.
func TestTranscript_OneEnormousLineIsStillBounded(t *testing.T) {
	full := strings.Repeat("y", 200_000)
	got := windowedCall(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute,
		Status: vibekit.ToolCompleted, Output: full,
	})})
	if !got.HasFull {
		t.Fatal("has_full = false, want true")
	}
	if len(got.Output) > toolPreviewBytes+1 {
		t.Errorf("output is %d bytes, want at most %d — a single line walked through the budget",
			len(got.Output), toolPreviewBytes+1)
	}
}

// TestTranscript_PreviewedOutputCarriesNoSpans pins the documented trade: spans
// are absolute UTF-16 offsets into the whole output, so a windowed output ships
// plain and the bulk brings the styled text back with them.
func TestTranscript_PreviewedOutputCarriesNoSpans(t *testing.T) {
	got := windowedCall(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute,
		Status:      vibekit.ToolCompleted,
		Output:      strings.Repeat("z", 20_000),
		OutputSpans: []vibekit.TextSpan{{Start: 0, End: 5, Attrs: 1}},
	})})
	if len(got.OutputSpans) != 0 {
		t.Errorf("output_spans = %v, want none on a windowed output", got.OutputSpans)
	}
}

// TestTranscript_OversizeDiffIsDroppedWholesale pins why a diff is never
// truncated: it is a before/after pair the client runs its own line diff over,
// so half a pair renders hunks describing an edit nobody made.
func TestTranscript_OversizeDiffIsDroppedWholesale(t *testing.T) {
	got := windowedCall(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Edit", Kind: vibekit.ToolKindEdit,
		Status: vibekit.ToolCompleted,
		Diffs: []vibekit.ToolDiff{
			{Path: "small.go", OldText: "a", NewText: "b"},
			{Path: "huge.go", OldText: strings.Repeat("o", 40_000), NewText: strings.Repeat("n", 40_000)},
		},
	})})
	if !got.HasFull {
		t.Fatal("has_full = false, want true")
	}
	if got.DiffCount != 2 {
		t.Errorf("diff_count = %d, want 2 (the FULL count)", got.DiffCount)
	}
	if len(got.Diffs) != 1 || got.Diffs[0].Path != "small.go" {
		t.Errorf("diffs = %v, want only the one that fit, whole", got.Diffs)
	}
}

// TestTranscript_InputKeepsItsSmallMembers is what stops the preview blanking a
// card's claim line: `pickFilePath` and `extractSubtitle` read the small members
// while the bulk is one of them (a write's whole `text`).
func TestTranscript_InputKeepsItsSmallMembers(t *testing.T) {
	in, err := json.Marshal(map[string]any{
		"path":        "internal/app/main.go",
		"explanation": "rewrite the entry point",
		"text":        strings.Repeat("L", 30_000),
	})
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	got := windowedCall(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Write", Kind: vibekit.ToolKindWrite,
		Status: vibekit.ToolCompleted, Input: in,
	})})
	if !got.HasFull {
		t.Fatal("has_full = false, want true")
	}
	var kept map[string]any
	if err := json.Unmarshal(got.Input, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got.Input)
	}
	if kept["path"] != "internal/app/main.go" {
		t.Errorf("path = %v, want it kept — the claim line is built from it", kept["path"])
	}
	if kept["explanation"] != "rewrite the entry point" {
		t.Errorf("explanation = %v, want it kept", kept["explanation"])
	}
	if _, ok := kept["text"]; ok {
		t.Error("the oversized `text` member survived the preview")
	}
}

// The per-member cap does not bound the OBJECT, so there is a second one.
//
// An input over budget only in aggregate used to pass through whole: forty members
// of 3 KiB each is 120 KiB with nothing over the 4 KiB member cap, and `has_full`
// would say the transcript carried the input entire — so the bound `ToolCall.HasFull`
// documents was a bound on the shape of the bulk rather than on its size. The
// largest members go first, so the small ones the claim line reads survive.
func TestTranscript_AWideInputIsBoundedInAggregate(t *testing.T) {
	members := map[string]any{
		"path":    "internal/app/main.go",
		"command": "go build ./...",
	}
	// Each member fits the per-member cap; together they are far over the object's.
	for i := range 40 {
		members["blob"+strconv.Itoa(i)] = strings.Repeat("B", 3_000)
	}
	in, err := json.Marshal(members)
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	if len(in) <= toolPreviewInputTotalBytes {
		t.Fatalf("Setup: fixture is %d bytes, needs to exceed the %d-byte object budget "+
			"or this test asserts nothing", len(in), toolPreviewInputTotalBytes)
	}

	got := windowedCall(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Write", Kind: vibekit.ToolKindWrite,
		Status: vibekit.ToolCompleted, Input: in,
	})})

	if !got.HasFull {
		t.Fatal("has_full = false for an input the transcript did not carry whole")
	}
	if len(got.Input) > toolPreviewInputTotalBytes*2 {
		t.Errorf("preview input is %d bytes, want it bounded near the %d-byte budget",
			len(got.Input), toolPreviewInputTotalBytes)
	}
	var kept map[string]any
	if err := json.Unmarshal(got.Input, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got.Input)
	}
	// The claim line's members are the smallest, so they are the last to go.
	if kept["path"] != "internal/app/main.go" {
		t.Errorf("path = %v, want it kept: the aggregate cut took a claim-line member "+
			"before the blobs", kept["path"])
	}
	if kept["command"] != "go build ./..." {
		t.Errorf("command = %v, want it kept", kept["command"])
	}
}

// The cut is DETERMINISTIC, or a card gains and loses fields between reloads.
//
// Map iteration order is randomised, so without an explicit tie-break the members
// dropped from an over-budget object would differ per request for the same input —
// two reads of one transcript disagreeing about what a tool call's input was.
func TestTranscript_TheAggregateCutIsTheSameEveryTime(t *testing.T) {
	members := make(map[string]json.RawMessage, 40)
	for i := range 40 {
		// Same size, so only the tie-break can order them.
		members["m"+strconv.Itoa(i)] = json.RawMessage(`"` + strings.Repeat("B", 3_000) + `"`)
	}
	first := ""
	for range 8 {
		kept := maps.Clone(members)
		if !trimInputToTotal(kept, 40*3_010) {
			t.Fatal("Setup: the fixture did not exceed the object budget")
		}
		names := slices.Sorted(maps.Keys(kept))
		joined := strings.Join(names, ",")
		if first == "" {
			first = joined
		}
		if joined != first {
			t.Fatalf("kept %v, want the same members as the first pass (%v): the cut "+
				"depends on map iteration order", names, first)
		}
	}
}

// TestToolBulk_ServesTheWholeCall is the other half of the ladder.
func TestToolBulk_ServesTheWholeCall(t *testing.T) {
	full := strings.Repeat("q", 30_000)
	in := json.RawMessage(`{"command":"build"}`)
	s := storeWith(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute,
		Status: vibekit.ToolCompleted, Output: full, Input: in,
		OutputSpans: []vibekit.TextSpan{{Start: 0, End: 3, Attrs: 2}},
		Diffs:       []vibekit.ToolDiff{{Path: "a.go", NewText: "x"}},
	})})

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/tools/tc1", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got vibekit.ToolCallBulk
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "tc1" {
		t.Errorf("id = %q, want %q", got.ID, "tc1")
	}
	if got.Output != full {
		t.Errorf("output is %d bytes, want the whole %d", len(got.Output), len(full))
	}
	if string(got.Input) != string(in) {
		t.Errorf("input = %s, want %s", got.Input, in)
	}
	if len(got.OutputSpans) != 1 {
		t.Errorf("output_spans = %v, want the one the preview dropped", got.OutputSpans)
	}
	if len(got.Diffs) != 1 {
		t.Errorf("diffs = %v, want one", got.Diffs)
	}
}

// TestToolBulk_Rejections pins the boundary: an unknown call and a malformed id
// answer differently, because one is a miss and the other is a bad request.
func TestToolBulk_Rejections(t *testing.T) {
	s := storeWith(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute, Status: vibekit.ToolCompleted,
	})})
	cases := []struct {
		name string
		path string
		want int
	}{
		{"an unknown tool call is a miss", "/api/chats/c1/tools/nope", http.StatusNotFound},
		{"an unknown chat is a miss", "/api/chats/c9/tools/tc1", http.StatusNotFound},
		{
			"a tool id outside the safe character set is refused",
			"/api/chats/c1/tools/a%20b", http.StatusBadRequest,
		},
		{"an empty tool id is refused", "/api/chats/c1/tools/", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			rec := httptest.NewRecorder()
			NewRouter(s).handleOne(rec, req)
			if rec.Code != c.want {
				t.Errorf("GET %s = %d, want %d (body %s)", c.path, rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestToolBulk_RejectsNonGet keeps the sub-resource read-only.
func TestToolBulk_RejectsNonGet(t *testing.T) {
	s := storeWith(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Execute", Kind: vibekit.ToolKindExecute, Status: vibekit.ToolCompleted,
	})})
	req := httptest.NewRequest(http.MethodPost, "/api/chats/c1/tools/tc1", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// TestPreviewMessage_LeavesASmallMessageAlone pins the copy-on-write: the
// conversations that were never the problem pay one pass and no allocation.
func TestPreviewMessage_LeavesASmallMessageAlone(t *testing.T) {
	m := callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Read", Kind: vibekit.ToolKindRead,
		Status: vibekit.ToolCompleted, Output: "two lines\nhere\n",
	})
	got := previewMessage(&m)
	if &got.ToolCalls[0] != &m.ToolCalls[0] {
		t.Error("previewMessage copied a message that needed no cutting")
	}
}

// TestPreviewOutput_CutsOnARuneBoundary: a byte cut through a multi-byte rune
// would put a replacement glyph on screen.
func TestPreviewOutput_CutsOnARuneBoundary(t *testing.T) {
	// One long line of 3-byte runes, so every candidate byte cut lands mid-rune.
	full := strings.Repeat("\u4e16", 20_000)
	got, cut := previewOutput(full)
	if !cut {
		t.Fatal("cut = false, want true")
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("the preview holds a replacement rune: %q", got[:min(40, len(got))])
		}
	}
}
