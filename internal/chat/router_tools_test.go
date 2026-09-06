package chat

import (
	"encoding/json"
	"fmt"
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
	// output_bytes is what the REVEAL will fetch, which is the persisted length,
	// because the store bounded this output before the preview ever saw it. The
	// original is on Truncated, and only there.
	if got.Truncated == nil || got.Truncated.OutputBytes != len(full) {
		t.Errorf("truncated = %+v, want OutputBytes = %d (the length before the store cut it)",
			got.Truncated, len(full))
	}
	if got.OutputBytes >= len(full) || got.OutputBytes <= len(got.Output) {
		t.Errorf("output_bytes = %d, want the persisted length: above the previewed %d and below the original %d",
			got.OutputBytes, len(got.Output), len(full))
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
	if len(got.Output) > previewBudget.outputBytes+1 {
		t.Errorf("output is %d bytes, want at most %d — a single line walked through the budget",
			len(got.Output), previewBudget.outputBytes+1)
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
	// Sized between the two budgets, so the STORE keeps both diffs and the
	// PREVIEW is the layer that drops one. The store's own drop is
	// TestStoreBound_OversizeDiffIsDroppedNotTruncated.
	half := previewBudget.diffBytes
	got := windowedCall(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Edit", Kind: vibekit.ToolKindEdit,
		Status: vibekit.ToolCompleted,
		Diffs: []vibekit.ToolDiff{
			{Path: "small.go", OldText: "a", NewText: "b"},
			{Path: "big.go", OldText: strings.Repeat("o", half), NewText: strings.Repeat("n", half)},
		},
	})})
	if !got.HasFull {
		t.Fatal("has_full = false, want true")
	}
	if got.Truncated != nil {
		t.Errorf("truncated = %+v, want nil: the store kept both diffs", got.Truncated)
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
	// Over BOTH budgets, so the store dropped `text` first and the preview found
	// nothing left to cut. Either layer keeps the claim line, which is the point.
	if got.Truncated == nil || got.Truncated.InputBytes != len(in) {
		t.Errorf("truncated = %+v, want InputBytes = %d", got.Truncated, len(in))
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
	if len(in) <= previewBudget.inputTotal {
		t.Fatalf("Setup: fixture is %d bytes, needs to exceed the %d-byte object budget "+
			"or this test asserts nothing", len(in), previewBudget.inputTotal)
	}

	got := windowedCall(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Write", Kind: vibekit.ToolKindWrite,
		Status: vibekit.ToolCompleted, Input: in,
	})})

	if !got.HasFull {
		t.Fatal("has_full = false for an input the transcript did not carry whole")
	}
	// The budget is a BOUND on the marshalled bytes, not a target near them. It used
	// to be asserted at 2x, which a sum-of-contents accounting satisfied while the
	// object json.Marshal produced ran over by every quote, colon and comma.
	if len(got.Input) > previewBudget.inputTotal {
		t.Errorf("preview input marshals to %d bytes, over the %d-byte budget by %d: the "+
			"aggregate cut charged the members' contents and not the JSON around them",
			len(got.Input), previewBudget.inputTotal, len(got.Input)-previewBudget.inputTotal)
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

// The aggregate budget bounds the MARSHALLED object, and this is the fixture that
// tells that apart from bounding the sum of its members' contents.
//
// Many tiny members is where JSON's own syntax dominates: two quotes, a colon and a
// separating comma is four bytes a member, so an accounting that charges only
// len(key)+len(value) under-counts by 4N — here about half the budget again. The
// 40-fat-member fixture above cannot see it, because one member is 3 KiB, so the cut
// stops a whole member short of the budget and the error hides in that granularity.
func TestTranscript_TheAggregateBudgetChargesJSONsOwnSyntax(t *testing.T) {
	// Enough one-byte values that the old accounting believed the object fit.
	const members = 4_000
	obj := make(map[string]any, members)
	for i := range members {
		obj["k"+strconv.Itoa(i)] = 1
	}
	in, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}

	got := windowedCall(t, []vibekit.Message{callMessage(vibekit.ToolCall{
		ID: "tc1", Title: "Write", Kind: vibekit.ToolKindWrite,
		Status: vibekit.ToolCompleted, Input: in,
	})})

	if !got.HasFull {
		t.Fatal("has_full = false for an input the transcript did not carry whole")
	}
	if len(got.Input) > previewBudget.inputTotal {
		t.Errorf("preview input marshals to %d bytes, over the %d-byte budget by %d: the "+
			"aggregate cut charged the members' contents and not the quotes, colons and "+
			"commas around them", len(got.Input), previewBudget.inputTotal,
			len(got.Input)-previewBudget.inputTotal)
	}
	// And it is not bounded by dropping everything: the budget still has to fit a
	// claim line's worth of members, or "bounded" would be satisfied by "empty".
	var kept map[string]any
	if err := json.Unmarshal(got.Input, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got.Input)
	}
	if len(kept) < 100 {
		t.Errorf("kept %d of %d members in a %d-byte budget, want most of what fits: the "+
			"charge per member is too high, not too low", len(kept), members,
			previewBudget.inputTotal)
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
		if !trimInputToTotal(kept, 40*3_010, previewBudget) {
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
	// Between the two budgets: the store keeps this whole, the preview cuts it,
	// so the ladder's second rung has something to serve.
	full := strings.Repeat("q", previewBudget.outputBytes+1_000)
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

// escapedValue is a JSON string value whose bytes are n copies of c, unescaped.
//
// Built by hand rather than with json.Marshal, because Marshal is what escapes: a
// fixture routed through it — or through the chat store, which writes with
// MarshalIndent — arrives already expanded and cannot see an accounting that measures
// the raw bytes. That is also why the shipped budget held by accident: the only
// caller reads values the store escaped on the way to disk, the same shape as the
// retention header's key folding.
func escapedValue(c byte, n int) json.RawMessage {
	return json.RawMessage(`"` + strings.Repeat(string(c), n) + `"`)
}

// The aggregate budget charges what ESCAPING costs, not what the raw bytes measure.
//
// encoding/json expands `<`, `>` and `&` to a six-byte `\u00xx` escape on the way
// out, so an object whose raw members sum to a third of the budget marshals to twice
// it. The reachable shapes are ordinary: a write tool's HTML or JSX content, a bash
// command carrying `&&`. The two fixtures above cannot see this — theirs are built
// from `strings.Repeat("B", …)` and small integers, neither of which escapes.
func TestPreviewInput_TheAggregateBudgetChargesWhatEscapingCosts(t *testing.T) {
	// Each member fits the per-member cap escaped (3,602 of 4,096) and the ten of
	// them fit the object budget RAW (6 KiB of 16 KiB) while marshalling to 36 KiB.
	obj := map[string]json.RawMessage{"path": json.RawMessage(`"internal/app/page.tsx"`)}
	for i := range 10 {
		obj["chunk"+strconv.Itoa(i)] = escapedValue('<', 600)
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	// json.Marshal escaped the fixture on the way in, so hand the function the
	// unescaped spelling a writer other than encoding/json would produce.
	unescaped := unescapeUnicode(t, raw)
	if len(unescaped) <= previewBudget.inputMember {
		t.Fatalf("Setup: fixture is %d bytes, needs to exceed %d or previewInput returns "+
			"early and this test asserts nothing", len(unescaped), previewBudget.inputMember)
	}
	if len(unescaped) > previewBudget.inputTotal {
		t.Fatalf("Setup: fixture is %d raw bytes, over the %d-byte object budget already, "+
			"so a raw accounting would trim it too", len(unescaped), previewBudget.inputTotal)
	}

	got, cut := boundInput(unescaped, previewBudget)
	if !cut {
		t.Fatal("cut = false for an input that marshals to over twice the object budget")
	}
	if len(got) > previewBudget.inputTotal {
		t.Errorf("preview input marshals to %d bytes, over the %d-byte budget by %d: the "+
			"aggregate cut charged the values' raw bytes and not the escapes encoding/json "+
			"writes for them", len(got), previewBudget.inputTotal,
			len(got)-previewBudget.inputTotal)
	}
	var kept map[string]any
	if err := json.Unmarshal(got, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got)
	}
	if kept["path"] != "internal/app/page.tsx" {
		t.Errorf("path = %v, want it kept: the aggregate cut took a claim-line member "+
			"before the chunks", kept["path"])
	}
}

// The per-MEMBER cap charges escaping too, and it is the half that decides whether
// one member can blow the budget on its own.
//
// Measured against a member of the same raw size that does NOT escape, so the cap is
// shown to key on what the value costs marshalled rather than on its length.
func TestPreviewInput_TheMemberCapChargesWhatEscapingCosts(t *testing.T) {
	obj := map[string]json.RawMessage{
		"path": json.RawMessage(`"internal/app/page.tsx"`),
		// 1,002 raw bytes, 6,002 marshalled: over the 4 KiB member cap.
		"escaped": escapedValue('&', 1_000),
		// 3,102 either way: under it, and larger raw than the member above.
		"plain": escapedValue('x', 3_100),
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("Setup: marshal input: %v", err)
	}
	unescaped := unescapeUnicode(t, raw)
	if len(unescaped) <= previewBudget.inputMember {
		t.Fatalf("Setup: fixture is %d bytes, needs to exceed %d or previewInput returns "+
			"early", len(unescaped), previewBudget.inputMember)
	}

	got, cut := boundInput(unescaped, previewBudget)
	if !cut {
		t.Fatal("cut = false for an object holding a member that marshals to 6 KiB")
	}
	var kept map[string]any
	if err := json.Unmarshal(got, &kept); err != nil {
		t.Fatalf("preview input is not an object: %s", got)
	}
	if _, ok := kept["escaped"]; ok {
		t.Errorf("the `escaped` member survived: it is 1,002 raw bytes and 6,002 "+
			"marshalled, so the cap measured the wrong one and one member alone can "+
			"marshal to %d bytes", 6*previewBudget.inputMember)
	}
	if kept["plain"] != strings.Repeat("x", 3_100) {
		t.Error("the `plain` member was dropped: it is LARGER raw than the escaped one " +
			"and under the cap marshalled, so the cap is cutting on the wrong measure")
	}
}

// The EARLY-OUT gate charges escaping as well, which is what makes the three
// measures one measure.
//
// The gate ran on `len(raw)` while both budgets below it ran on what encoding/json
// writes, so an object under the per-member cap RAW returned before either budget
// saw it and shipped at six times its measured size. A 4,010-byte object of
// unescaped `<` marshals to roughly 24 KiB, over the per-member cap and over the
// object budget, and used to go through whole.
//
// UNREACHABLE through the one production caller, which reads values the store
// persisted through encoding/json and therefore already escaped: this is the
// budgets agreeing on a measure, not a live leak.
func TestPreviewInput_TheEarlyOutGateChargesWhatEscapingCosts(t *testing.T) {
	// One member of 4,000 unescaped `<`: 24,006 bytes marshalled, well over both
	// budgets, and 4,010 bytes raw — inside the 4,096-byte per-member cap, so the
	// gate is what decides whether anything is measured at all.
	unescaped := json.RawMessage(`{"text":"` + strings.Repeat("<", 4_000) + `"}`)
	if len(unescaped) > previewBudget.inputMember {
		t.Fatalf("Setup: fixture is %d raw bytes, over the %d-byte cap already, so the "+
			"raw gate would have cut it and this test asserts nothing",
			len(unescaped), previewBudget.inputMember)
	}
	wire := inputWireBytes(unescaped)
	if len(wire) <= previewBudget.inputTotal {
		t.Fatalf("Setup: fixture marshals to %d bytes, inside the %d-byte object budget, "+
			"so nothing is over budget", len(wire), previewBudget.inputTotal)
	}

	got, cut := boundInput(unescaped, previewBudget)
	if !cut {
		t.Fatalf("cut = false for an input that marshals to %d bytes: the gate measured "+
			"the %d raw bytes and returned before either budget ran",
			len(wire), len(unescaped))
	}
	if len(got) > previewBudget.inputTotal {
		t.Errorf("preview input marshals to %d bytes, over the %d-byte budget",
			len(got), previewBudget.inputTotal)
	}
}

// An input already in its wire form must go through unchanged, which is the
// property that lets the gate convert before it measures: the single production
// caller hands over store-read values, and re-measuring them must not start
// trimming inputs that were within budget.
func TestPreviewInput_AnAlreadyEscapedInputWithinBudgetIsUntouched(t *testing.T) {
	// 400 escaped `<`, about 2,411 wire bytes — deliberately UNDER the 4,096-byte
	// gate, which is the only size that can exercise the pass-through and which the
	// setup guard below enforces. The escapes must not be counted twice into
	// something over it.
	obj := map[string]string{"text": strings.Repeat("<", 400)}
	wire, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("Setup: marshal: %v", err)
	}
	if len(wire) > previewBudget.inputMember {
		t.Fatalf("Setup: fixture is %d wire bytes, over the %d-byte gate, so it cannot "+
			"exercise the pass-through", len(wire), previewBudget.inputMember)
	}

	if got, cut := boundInput(wire, previewBudget); cut || got != nil {
		t.Errorf("previewInput cut an input of %d wire bytes (got %q): a value already in "+
			"its wire form is idempotent under the conversion", len(wire), got)
	}
}

// unescapeUnicode turns the `\u00xx` escapes json.Marshal wrote back into their one
// raw byte, so a fixture can carry the unescaped spelling of `<`, `>` and `&`.
func unescapeUnicode(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	s := string(raw)
	for _, c := range []byte{'<', '>', '&'} {
		s = strings.ReplaceAll(s, fmt.Sprintf(`\u%04x`, c), string(c))
	}
	out := json.RawMessage(s)
	if !json.Valid(out) {
		t.Fatalf("Setup: unescaping produced invalid JSON: %s", out)
	}
	return out
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
	got, cut := boundOutput(full, previewBudget)
	if !cut {
		t.Fatal("cut = false, want true")
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("the preview holds a replacement rune: %q", got[:min(40, len(got))])
		}
	}
}
