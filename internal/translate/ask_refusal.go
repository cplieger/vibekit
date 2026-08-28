package translate

// The one convention shared by the three ask handlers: a frame carrying an id
// that vibekit declines to process gets a well-formed fail-closed ANSWER, never
// a bare return.

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// refuseAsk answers a server-to-client ask whose params did not decode.
//
// The id was verified before this runs, so the frame is provably a request, and
// KAS's sendRequest carries no timeout: returning without answering strands the
// tool batch until process teardown. The unattended floor cannot rescue it
// either — its claim looks for a tracker entry, and a frame that never decoded
// was never tracked.
//
// The answer is the kind's own fail-closed FIRST-CLASS result, never a JSON-RPC
// error. KAS's turn-approval path fails OPEN: it answers approved when the
// requestPermission call throws, so an RPC error on a turn_approval frame would
// apply every unreviewed write in the turn, which is strictly worse than the
// drop. A well-formed cancelled outcome does not take that branch.
//
// Nothing decoded from the frame is logged, and nothing decoded from it may be:
// a permission title, an elicitation message and a user-input question are all
// agent-authored untrusted text that this package defuses through displayText
// before it reaches a log or the wire. The method, the id and the decode error
// are vibekit's own.
func (t *Translator) refuseAsk(
	ctx context.Context, chatID vibekit.ChatID, method string, requestID int64, result any, cause error,
) {
	slog.Warn("ask params did not decode, answering fail-closed",
		"method", method, "request_id", requestID, "chat_id", chatID, "error", cause)
	if err := t.respond.BridgeRespond(ctx, chatID, requestID, result, nil); err != nil {
		slog.Warn("fail-closed ask answer not delivered",
			"method", method, "request_id", requestID, "chat_id", chatID, "error", err)
	}
}
