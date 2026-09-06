package chat

import (
	"net/http"
	"slices"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
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
func previewMessage(m *vibekit.Message) vibekit.Message {
	out, _ := boundMessage(m, previewToolCall)
	return out
}

// previewToolCall returns a copy of tc bounded to previewBudget, and whether
// anything was cut.
//
// HasFull covers all three fields together because the client fetches one bulk
// for all three: a card that lost only its input still needs the same request to
// render its diff. It is a promise the bulk endpoint can serve the rest, which is
// why ToolTruncation is a separate record: the store's own cut is not fetchable.
func previewToolCall(tc *vibekit.ToolCall) (vibekit.ToolCall, bool) {
	out, cut := boundToolCall(tc, previewBudget)
	if cut == (vibekit.ToolTruncation{}) {
		return out, false
	}
	out.OutputBytes = cut.OutputBytes
	out.DiffCount = cut.DiffCount
	out.HasFull = true
	return out, true
}
