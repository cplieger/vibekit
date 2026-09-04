package chat

import (
	"cmp"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// How much of one tool call the transcript carries.
//
// The numbers are set by what the card SHOWS at rest. It windows output to the
// first and last 20 lines (`strings.ts` windowOutput) and a diff to 24 hunk rows
// (`diff.ts` windowHunks), so a preview twice that deep satisfies every resting
// card and most of the first reveal, while the byte caps are what make the
// preview bounded whatever the shape of the content — one measured chat holds a
// 9.1 MB message, and a single 3.8 MB line would walk through a line budget
// untouched.
const (
	// toolPreviewLines is kept from each END of the output, which is where the
	// information is: a command's first lines say what it did and its last say
	// how it ended. A prefix would lose the error.
	toolPreviewLines = 40
	toolPreviewBytes = 8 << 10
	// toolPreviewDiffBytes bounds the diffs, which are kept WHOLE or not at all.
	// A diff is a before/after pair the client runs its own line-diff over, so a
	// truncated pair would produce hunks that describe an edit nobody made.
	toolPreviewDiffBytes = 16 << 10
	// toolPreviewInputBytes bounds ONE member of the input object. The claim line
	// is built from the small members (`path`, `command`, `query`), and the bulk
	// is a file's whole `text` or an edit's `oldStr`/`newStr`.
	toolPreviewInputBytes = 4 << 10
	// toolPreviewInputTotalBytes bounds the object as a WHOLE, because the
	// per-member cap does not: a hundred members of 3 KiB each is 300 KiB with
	// nothing over budget, so `has_full` would say the transcript carried the
	// input entire and the bound would be a bound on nothing. Four members'
	// worth, which is more than any claim line reads.
	toolPreviewInputTotalBytes = 4 * toolPreviewInputBytes
)

// handleToolCall serves GET /api/chats/{id}/tools/{toolCallID}: the whole of one
// tool call's input, output and diffs.
//
// Sub-resource rather than a wider transcript response, because the depth ladder
// is the point: a collapsed card is a claim line, and the bulk is only worth
// bytes once a reader asks for it. It is the same ladder the UI already
// implements against content it had already paid to receive.
func (rt *Router) handleToolCall(w http.ResponseWriter, r *http.Request, chatID vibekit.ChatID, toolCallID string) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if !chatIDPattern(chatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	// Same gate a message id passes: this value is echoed back and is the key of
	// a linear scan, so an unbounded or exotic one is refused rather than
	// searched for.
	if !ids.ValidMessageID(toolCallID) {
		httpreply.BadRequest(w, "invalid tool_call_id")
		return
	}
	c, ok := rt.store.Get(r.Context(), chatID)
	if !ok {
		httpreply.NotFound(w, errMsgChatNotFound)
		return
	}
	tc, found := findToolCall(c.Messages, toolCallID)
	if !found {
		httpreply.NotFound(w, "unknown tool call")
		return
	}
	webhttp.WriteJSON(w, vibekit.ToolCallBulk{
		ID:          tc.ID,
		Input:       tc.Input,
		Output:      tc.Output,
		Diffs:       tc.Diffs,
		OutputSpans: tc.OutputSpans,
	})
}

// findToolCall locates a tool call by id, newest message first.
//
// A scan rather than an index, and newest-first because that is where a reader's
// reveal lands: the whole chat is already in memory to answer the transcript, and
// an index over tool-call ids would be a second structure to keep in step with
// the append path for one lookup per user click.
func findToolCall(msgs []vibekit.Message, id string) (*vibekit.ToolCall, bool) {
	for i := range slices.Backward(msgs) {
		for j := range msgs[i].ToolCalls {
			if msgs[i].ToolCalls[j].ID == id {
				return &msgs[i].ToolCalls[j], true
			}
		}
	}
	return nil, false
}

// previewMessage returns m with every oversized tool call replaced by its
// preview, or m unchanged when nothing needed cutting.
//
// Copy-on-write: the common message carries no tool call over budget and is
// returned as-is, so the projection costs one pass and no allocation for the
// conversations that were never the problem.
func previewMessage(m *vibekit.Message) vibekit.Message {
	var previewed []vibekit.ToolCall
	for i := range m.ToolCalls {
		tc, cut := previewToolCall(&m.ToolCalls[i])
		if !cut {
			continue
		}
		if previewed == nil {
			previewed = slices.Clone(m.ToolCalls)
		}
		previewed[i] = tc
	}
	if previewed == nil {
		return *m
	}
	out := *m
	out.ToolCalls = previewed
	return out
}

// previewToolCall returns a copy of tc bounded to the preview budgets, and
// whether anything was cut.
//
// HasFull covers all three fields together because the client fetches one bulk
// for all three: a card that lost only its input still needs the same request to
// render its diff.
func previewToolCall(tc *vibekit.ToolCall) (vibekit.ToolCall, bool) {
	out := *tc
	cut := false
	if text, ok := previewOutput(tc.Output); ok {
		out.Output = text
		// Dropped with the cut — previewOutput says why.
		out.OutputSpans = nil
		out.OutputBytes = len(tc.Output)
		cut = true
	}
	if diffs, ok := previewDiffs(tc.Diffs); ok {
		out.Diffs = diffs
		out.DiffCount = len(tc.Diffs)
		cut = true
	}
	if input, ok := previewInput(tc.Input); ok {
		out.Input = input
		cut = true
	}
	out.HasFull = cut
	return out, cut
}

// previewOutput windows an output to its first and last toolPreviewLines lines,
// hard-capped at toolPreviewBytes.
//
// The caller drops OutputSpans with the cut, and that is deliberate rather than
// an omission: TextSpan offsets are UTF-16 code units into the WHOLE output, so
// remapping them onto a two-slice window means a second implementation of the
// client's own windowSpans in a different offset unit — for the ~0.25% of
// outputs carrying an escape sequence crossed with the ones big enough to window
// at all. So a previewed output renders plain and the reveal's fetch brings the
// styled text back with its spans, on the round trip that reveal already makes.
func previewOutput(s string) (text string, cut bool) {
	if len(s) <= toolPreviewBytes {
		return "", false
	}
	lines := strings.Count(s, "\n") + 1
	if lines > toolPreviewLines*2 {
		head := nthIndex(s, '\n', toolPreviewLines)
		tail := lastNthIndex(s, '\n', toolPreviewLines)
		s = s[:head] + "\n" + s[tail+1:]
		if len(s) <= toolPreviewBytes {
			return s, true
		}
	}
	// Still over budget, so the content is not line-shaped — a single enormous
	// line, or forty of them. Cut on a rune boundary from each end rather than
	// mid-character, which would put a replacement glyph on screen.
	half := toolPreviewBytes / 2
	return cutRunes(s[:half]) + "\n" + cutRunesFront(s[len(s)-half:]), true
}

// nthIndex returns the index of the nth occurrence of b, or len(s) when there
// are fewer than n.
func nthIndex(s string, b byte, n int) int {
	for i := range len(s) {
		if s[i] != b {
			continue
		}
		n--
		if n == 0 {
			return i
		}
	}
	return len(s)
}

// lastNthIndex returns the index of the nth occurrence of b counting from the
// end, or -1 when there are fewer than n.
func lastNthIndex(s string, b byte, n int) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != b {
			continue
		}
		n--
		if n == 0 {
			return i
		}
	}
	return -1
}

// cutRunes trims a trailing partial UTF-8 sequence off s, so a byte cut cannot
// put a replacement glyph on screen. A genuine U+FFFD in the text survives:
// DecodeLastRuneInString reports size 1 only for a byte that decodes to nothing.
func cutRunes(s string) string {
	for s != "" {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size > 1 {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}

// cutRunesFront trims leading continuation bytes off s, the same cut from the
// other end.
func cutRunesFront(s string) string {
	for s != "" && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// previewDiffs keeps the leading diffs that fit toolPreviewDiffBytes, whole.
//
// Whole or not at all: a ToolDiff is a before/after pair the client runs its own
// line-diff over, so a truncated pair yields hunks describing an edit that never
// happened — worse than no preview, because it looks like one.
func previewDiffs(diffs []vibekit.ToolDiff) ([]vibekit.ToolDiff, bool) {
	spent := 0
	for i := range diffs {
		size := len(diffs[i].Path) + len(diffs[i].OldText) + len(diffs[i].NewText)
		if spent+size > toolPreviewDiffBytes {
			// Capacity clipped so the returned slice cannot append into the
			// persisted chat's backing array.
			return diffs[:i:i], true
		}
		spent += size
	}
	return nil, false
}

// previewInput drops the members of an input object that are over budget, keeping
// every small one, then drops the largest of what is left until the object fits.
//
// Per MEMBER rather than per object, because the claim line is built from the small
// members — `pickFilePath` reads `path`/`targetFile`/`sourcePath` — while the bulk
// is one member: a write's whole `text`, or an edit's `oldStr`/`newStr`. Dropping
// the object wholesale would blank the claim line to save the same bytes.
//
// A non-object input is kept or dropped whole on the same budget.
func previewInput(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) <= toolPreviewInputBytes {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		// Not an object, and over budget: nothing here can name a part of it to
		// keep, so it goes whole and the bulk carries it.
		return json.RawMessage("null"), true
	}
	kept := make(map[string]json.RawMessage, len(obj))
	cut := false
	// The enclosing braces, less the comma inputMemberBytes charges to the last
	// member. Both halves are what make `spent` the MARSHALLED size rather than a
	// sum of contents, so the budget bounds the bytes that go on the wire.
	spent := len("{}") - len(",")
	for k, v := range obj {
		if len(v) > toolPreviewInputBytes {
			cut = true
			continue
		}
		kept[k] = v
		spent += inputMemberBytes(k, v)
	}
	if trimInputToTotal(kept, spent) {
		cut = true
	}
	if !cut {
		// Nothing was over either budget, so the object goes through as it is.
		return nil, false
	}
	out, err := json.Marshal(kept)
	if err != nil {
		// Re-marshalling values that were just unmarshalled cannot fail; answer
		// with no input rather than the oversized original.
		return json.RawMessage("null"), true
	}
	return out, true
}

// inputMemberBytes is what one member costs in the MARSHALLED object: its key as
// JSON, the colon, the value, and the comma before the next member.
//
// Charging a comma to every member over-states the object by one byte once the two
// braces are added, which is the direction a bound has to err in. Without this
// accounting `toolPreviewInputTotalBytes` bounded the sum of the CONTENTS, and the
// object json.Marshal produced from them exceeded it by every quote, colon and
// comma — four bytes a member, so a wide input beat the budget by the shape of JSON
// rather than by its own size.
//
// The key is marshalled rather than measured, because one needing an escape is
// longer quoted than raw. The value is measured, because encoding/json COMPACTS a
// RawMessage on the way out, so its raw length is already an upper bound.
func inputMemberBytes(k string, v json.RawMessage) int {
	key, err := json.Marshal(k)
	if err != nil {
		// A Go string cannot fail to marshal; charge the unescaped spelling.
		return len(k) + len(`"":,`) + len(v)
	}
	return len(key) + len(":,") + len(v)
}

// trimInputToTotal drops members of kept, largest first, until the object they
// marshal to fits the aggregate budget. Reports whether it dropped any.
//
// Largest first so the members the claim line reads are the ones that survive: a
// `path` is tens of bytes and whatever pushed the object over is not. The name
// breaks a size tie, so two requests over one input cut the same members —
// otherwise map iteration order would decide, and a card would gain and lose
// fields between reloads.
func trimInputToTotal(kept map[string]json.RawMessage, spent int) bool {
	if spent <= toolPreviewInputTotalBytes {
		return false
	}
	keys := slices.Collect(maps.Keys(kept))
	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(
			cmp.Compare(inputMemberBytes(b, kept[b]), inputMemberBytes(a, kept[a])),
			cmp.Compare(a, b),
		)
	})
	for _, k := range keys {
		if spent <= toolPreviewInputTotalBytes {
			break
		}
		spent -= inputMemberBytes(k, kept[k])
		delete(kept, k)
	}
	return true
}
