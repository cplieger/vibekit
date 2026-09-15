package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/cplieger/vibekit/internal/vibekit"
	"pgregory.net/rapid"
)

// The one invariant the index keeps: it never rejects a chat the scan matches.
// Chats are built over every span kind the scan reads (title, prose, reasoning,
// tool title/input/output/diff, attachment names, plan entries, a failure reason)
// from an alphabet whose runes change case and byte length under folding; a query
// is usually a case-flipped slice of one of those spans, so most draws match.
// The oracle is the scan itself plus titleHits, never a second trigram walk.
func TestChatFilter_NeverRejectsAChatTheScanMatches(t *testing.T) {
	alphabet := []rune("abcdeKkİiΣσßẞé✓ x\n")
	text := func(rt *rapid.T, label string, max int) string {
		return string(rapid.SliceOfN(rapid.SampledFrom(alphabet), 3, max).Draw(rt, label))
	}
	rapid.Check(t, func(rt *rapid.T) {
		c := &vibekit.Chat{ID: "c-aaaaaaaa", Name: text(rt, "name", 20)}
		n := rapid.IntRange(1, 3).Draw(rt, "messages")
		for i := range n {
			c.Messages = append(c.Messages, drawMessage(rt, fmt.Sprintf("m%d", i), text))
		}

		var spans []string
		spans = append(spans, c.Name)
		for i := range c.Messages {
			for _, seg := range messageSegments(&c.Messages[i]) {
				spans = append(spans, seg.text)
			}
		}
		var query string
		if rapid.IntRange(0, 3).Draw(rt, "unrelated") == 0 {
			query = text(rt, "query", 6)
		} else {
			query = flipCase(rt, sliceRunes(rt, rapid.SampledFrom(spans).Draw(rt, "span"), 6))
		}

		res, _ := searchChat(c.Messages, query, false)
		matched := res.Matched > 0 || titleHits(c.Name, query) > 0
		if !matched {
			return
		}
		want := queryTrigrams(parseSearchQuery(query, false).text)
		if !buildChatFilter(c).holdsAll(want) {
			rt.Fatalf("filter rejected query %q that the scan matched (%d body hits, %d title hits) in chat %+v",
				query, res.Matched, titleHits(c.Name, query), c)
		}
	})
}

// drawMessage is one message of a random shape, each shape feeding a different
// set of segment kinds.
func drawMessage(rt *rapid.T, id string, text func(*rapid.T, string, int) string) vibekit.Message {
	switch rapid.IntRange(0, 3).Draw(rt, id+"_shape") {
	case 0:
		return vibekit.Message{
			ID: id, Role: vibekit.RoleUser, Content: text(rt, id+"_content", 40),
			Attachments: []vibekit.Attachment{{Path: "x", Name: text(rt, id+"_attachment", 12)}},
		}
	case 1:
		return vibekit.Message{
			ID: id, Role: vibekit.RoleAssistant, Content: text(rt, id+"_content", 40),
			Reasoning: text(rt, id+"_reasoning", 40), Plan: []vibekit.PlanEntry{{Content: text(rt, id+"_plan", 20)}},
			TurnFailureReason: text(rt, id+"_failure", 20),
		}
	case 2:
		return vibekit.Message{ID: id, Role: vibekit.RoleAssistant, Blocks: []vibekit.Block{
			{Type: vibekit.BlockThinking, Thinking: text(rt, id+"_thinking", 40)},
			{Type: vibekit.BlockText, Text: text(rt, id+"_text", 40)},
		}}
	default:
		input, err := json.Marshal(map[string]string{"command": text(rt, id+"_input", 20)})
		if err != nil {
			rt.Fatalf("marshal input: %v", err)
		}
		return vibekit.Message{
			ID: id, Role: vibekit.RoleAssistant,
			Blocks: []vibekit.Block{{Type: vibekit.BlockToolUse, ToolCallID: "t1"}},
			ToolCalls: []vibekit.ToolCall{{
				ID: "t1", Title: text(rt, id+"_title", 20), Output: text(rt, id+"_output", 40),
				Input: input, Diffs: []vibekit.ToolDiff{{Path: "p", NewText: text(rt, id+"_diff", 40)}},
			}},
		}
	}
}

// sliceRunes is a random rune window of s, at most max runes long and at least
// three where s allows, so most windows are long enough to carry a trigram.
func sliceRunes(rt *rapid.T, s string, max int) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	start := rapid.IntRange(0, len(runes)-1).Draw(rt, "start")
	longest := min(max, len(runes)-start)
	end := start + rapid.IntRange(min(3, longest), longest).Draw(rt, "length")
	return string(runes[start:end])
}

// flipCase upper-cases a random subset of s's runes, so the query differs from the
// span it was cut from by case alone.
func flipCase(rt *rapid.T, s string) string {
	var b strings.Builder
	for i, r := range s {
		if rapid.Bool().Draw(rt, fmt.Sprintf("flip_%d", i)) {
			r = unicode.ToUpper(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}
