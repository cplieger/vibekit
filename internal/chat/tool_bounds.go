package chat

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// toolBudget is how much of one tool call's three bulky fields a consumer keeps. Two exist,
// and the ORDER between them is the contract: the preview must never be bigger than what the
// store persisted, or the card promises a reveal the record cannot serve.
type toolBudget struct {
	// outputLines is kept from each END: a command's first lines say what it did and its last
	// say how it ended, so a prefix would lose the error.
	outputLines int
	outputBytes int
	// diffBytes bounds the diffs, which are kept WHOLE or not at all.
	diffBytes int
	// inputMember bounds ONE member of the input object: the claim line is built from the small
	// members, and the bulk is a file's whole `text` or an edit's `oldStr`/`newStr`.
	inputMember int
	// inputTotal bounds the object as a WHOLE: a hundred 3 KiB members clear the per-member cap.
	inputTotal int
}

// previewBudget is what the transcript carries at rest, set by what the card SHOWS: output
// windowed to 20 lines each end (`strings.ts` windowOutput) and a diff to 24 hunk rows
// (`diff.ts` windowHunks), so twice that depth satisfies every resting card. The byte caps
// bound the preview whatever the shape: a single 3.8 MB line walks through a line budget.
var previewBudget = toolBudget{
	outputLines: 40,
	outputBytes: 8 << 10,
	diffBytes:   16 << 10,
	inputMember: 4 << 10,
	// Four members' worth, which is more than any claim line reads.
	inputTotal: 4 * (4 << 10),
}

// persistBudget is what the RECORD keeps forever, and the only bound on what one turn ADDS:
// one call to about 112 KiB of bulk. It does not bound the CHAT. Every number floors at its
// previewBudget twin, or the reveal would show less than the resting card; diffs take 4x
// because a ToolDiff cannot be shortened, and the median measured diff is 26,480 bytes.
var persistBudget = toolBudget{
	outputLines: 2 * previewBudget.outputLines,
	outputBytes: 2 * previewBudget.outputBytes,
	diffBytes:   4 * previewBudget.diffBytes,
	inputMember: 2 * previewBudget.inputMember,
	inputTotal:  2 * previewBudget.inputTotal,
}

// storeChat returns chat bounded to persistBudget, copy-on-write all the way down: the common
// chat is written with no clone at all, and a caller holding its own Message never sees the cut.
func storeChat(chat *vibekit.Chat) *vibekit.Chat {
	var bounded []vibekit.Message
	for i := range chat.Messages {
		m, cut := boundMessage(&chat.Messages[i], storeToolCall)
		if !cut {
			continue
		}
		if bounded == nil {
			bounded = slices.Clone(chat.Messages)
		}
		bounded[i] = m
	}
	if bounded == nil {
		return chat
	}
	out := *chat
	out.Messages = bounded
	return &out
}

// storeToolCall returns tc bounded to persistBudget with its Truncated record set, and whether
// anything was cut. It must stay idempotent: every mutation loads the persisted chat and writes
// it back, so an already-bounded call arrives here again.
func storeToolCall(tc *vibekit.ToolCall) (vibekit.ToolCall, bool) {
	out, cut := boundToolCall(tc, persistBudget)
	if cut == (vibekit.ToolTruncation{}) {
		return out, false
	}
	out.Truncated = mergeTruncation(tc.Truncated, cut)
	return out, true
}

// mergeTruncation keeps whichever measurement is larger per field, so the
// ORIGINAL size survives every later rewrite of an already-bounded call.
func mergeTruncation(prior *vibekit.ToolTruncation, cut vibekit.ToolTruncation) *vibekit.ToolTruncation {
	if prior == nil {
		return &cut
	}
	merged := *prior
	merged.OutputBytes = max(merged.OutputBytes, cut.OutputBytes)
	merged.InputBytes = max(merged.InputBytes, cut.InputBytes)
	merged.DiffBytes = max(merged.DiffBytes, cut.DiffBytes)
	merged.DiffCount = max(merged.DiffCount, cut.DiffCount)
	return &merged
}

// boundMessage returns m with every tool call bound by f, or m unchanged when nothing needed
// cutting: copy-on-write, so a message with nothing over budget costs one pass and no allocation.
func boundMessage(m *vibekit.Message, f func(*vibekit.ToolCall) (vibekit.ToolCall, bool)) (vibekit.Message, bool) {
	var bounded []vibekit.ToolCall
	for i := range m.ToolCalls {
		tc, cut := f(&m.ToolCalls[i])
		if !cut {
			continue
		}
		if bounded == nil {
			bounded = slices.Clone(m.ToolCalls)
		}
		bounded[i] = tc
	}
	if bounded == nil {
		return *m, false
	}
	out := *m
	out.ToolCalls = bounded
	return out, true
}

// boundToolCall returns a copy of tc bounded to b, and what it cut: a zero ToolTruncation means
// nothing was over budget. It sets no marker of its own, because the two callers publish
// different ones — the bytes are fetchable for one of them and gone for the other.
func boundToolCall(tc *vibekit.ToolCall, b toolBudget) (vibekit.ToolCall, vibekit.ToolTruncation) {
	out := *tc
	var cut vibekit.ToolTruncation
	if text, ok := boundOutput(tc.Output, b); ok {
		out.Output = text
		// Dropped with the cut — boundOutput says why.
		out.OutputSpans = nil
		cut.OutputBytes = len(tc.Output)
	}
	if diffs, ok := boundDiffs(tc.Diffs, b); ok {
		out.Diffs = diffs
		cut.DiffCount = len(tc.Diffs)
		cut.DiffBytes = diffsBytes(tc.Diffs)
	}
	if input, ok := boundInput(tc.Input, b); ok {
		out.Input = input
		cut.InputBytes = len(tc.Input)
	}
	return out, cut
}

// boundOutput windows an output to its first and last b.outputLines lines, hard-capped at
// b.outputBytes. The caller drops OutputSpans with the cut deliberately: TextSpan offsets are
// UTF-16 code units into the WHOLE output, so remapping them onto a two-slice window would be a
// second implementation of the client's windowSpans in another offset unit.
func boundOutput(s string, b toolBudget) (text string, cut bool) {
	if len(s) <= b.outputBytes {
		return "", false
	}
	lines := strings.Count(s, "\n") + 1
	if lines > b.outputLines*2 {
		head := nthIndex(s, '\n', b.outputLines)
		tail := lastNthIndex(s, '\n', b.outputLines)
		s = s[:head] + "\n" + s[tail+1:]
		if len(s) <= b.outputBytes {
			return s, true
		}
	}
	// Still over budget, so the content is not line-shaped. Cut on a rune boundary from each end
	// rather than mid-character, which would put a replacement glyph on screen.
	half := b.outputBytes / 2
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

// cutRunesFront trims leading continuation bytes off s, the same cut from the other end.
func cutRunesFront(s string) string {
	for s != "" && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// diffsBytes is what a diff slice costs in content, the measure boundDiffs spends.
func diffsBytes(diffs []vibekit.ToolDiff) int {
	total := 0
	for i := range diffs {
		total += len(diffs[i].Path) + len(diffs[i].OldText) + len(diffs[i].NewText)
	}
	return total
}

// boundDiffs keeps the leading diffs that fit b.diffBytes, WHOLE or not at all: a ToolDiff is a
// before/after pair the client runs its own line-diff over, so a truncated pair yields hunks
// describing an edit that never happened.
func boundDiffs(diffs []vibekit.ToolDiff, b toolBudget) ([]vibekit.ToolDiff, bool) {
	spent := 0
	for i := range diffs {
		size := len(diffs[i].Path) + len(diffs[i].OldText) + len(diffs[i].NewText)
		if spent+size > b.diffBytes {
			// Capacity clipped so the returned slice cannot append into the persisted chat's array.
			return diffs[:i:i], true
		}
		spent += size
	}
	return nil, false
}

// boundInput drops the members of an input object that are over budget, keeping every small one,
// then drops the largest of what is left until the object fits. Per MEMBER rather than per
// object because the claim line is built from the small members while the bulk is one, so
// dropping the object wholesale would blank the claim line to save the same bytes.
func boundInput(raw json.RawMessage, b toolBudget) (json.RawMessage, bool) {
	// Measured as the ENCODER will write it, because a raw length bounds neither direction: gating
	// on raw bytes let a 4,010-byte object of `<` through at roughly 24 KiB on the wire. The
	// conversion is idempotent, so one Marshal serves the gate and the walk.
	raw = inputWireBytes(raw)
	if len(raw) <= b.inputMember {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		// Not an object and over budget: nothing here can name a part of it to keep.
		return json.RawMessage("null"), true
	}
	kept := make(map[string]json.RawMessage, len(obj))
	cut := false
	// The enclosing braces, less the comma inputMemberBytes charges to the last member: for a
	// non-empty object the members' costs then sum to EXACTLY the marshalled length.
	spent := len("{}") - len(",")
	for k, v := range obj {
		v = inputWireBytes(v)
		if len(v) > b.inputMember {
			cut = true
			continue
		}
		kept[k] = v
		spent += inputMemberBytes(k, v)
	}
	if trimInputToTotal(kept, spent, b) {
		cut = true
	}
	if !cut {
		// Nothing was over either budget, so the object goes through as it is.
		return nil, false
	}
	out, err := json.Marshal(kept)
	if err != nil {
		// Cannot fail on values just unmarshalled; answer with no input, not the oversized original.
		return json.RawMessage("null"), true
	}
	return out, true
}

// inputWireBytes is v as it will appear in the marshalled object. encoding/json compacts a
// RawMessage on the way out AND HTML-escapes it — `<`, `>` and `&` go from one byte to six,
// U+2028 and U+2029 from three to six — so a raw length is no upper bound on what a value costs.
// The conversion is idempotent, so what the caller marshals is byte-identical to what was
// measured here.
func inputWireBytes(v json.RawMessage) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		// Unreachable: v came out of a successful Unmarshal, so leave it raw for the caller's Marshal.
		return v
	}
	return out
}

// inputMemberBytes is what one member costs in the MARSHALLED object: its key as JSON, the
// colon, the value, and the comma before the next member. Without the punctuation the aggregate
// budget bounds the sum of the CONTENTS and the object Marshal produces exceeds it by four bytes
// a member. The key is marshalled here for the same reason: escaped is longer than raw.
func inputMemberBytes(k string, v json.RawMessage) int {
	key, err := json.Marshal(k)
	if err != nil {
		// A Go string cannot fail to marshal; charge the unescaped spelling.
		return len(k) + len(`"":,`) + len(v)
	}
	return len(key) + len(":,") + len(v)
}

// trimInputToTotal drops members of kept, largest first, until the object they marshal to fits
// b.inputTotal, and reports whether it dropped any. Largest first so the small members the claim
// line reads survive; the name breaks a size tie so two requests over one input cut the same
// members, instead of map iteration order making a card gain and lose fields between reloads.
func trimInputToTotal(kept map[string]json.RawMessage, spent int, b toolBudget) bool {
	if spent <= b.inputTotal {
		return false
	}
	keys := slices.Collect(maps.Keys(kept))
	slices.SortFunc(keys, func(x, y string) int {
		return cmp.Or(
			cmp.Compare(inputMemberBytes(y, kept[y]), inputMemberBytes(x, kept[x])),
			cmp.Compare(x, y),
		)
	})
	for _, k := range keys {
		if spent <= b.inputTotal {
			break
		}
		spent -= inputMemberBytes(k, kept[k])
		delete(kept, k)
	}
	return true
}
