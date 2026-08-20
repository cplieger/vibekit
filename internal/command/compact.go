package command

// Compaction, via KAS's own verb.
//
// TYPED `/compact` PERFORMS NO COMPACTION. Probed: it returns `end_turn` in
// ~3.4s having emitted zero summarization frames, while the model cheerfully
// replies "Done — context compacted." Nothing inside KAS parses it, so the text
// reaches the MODEL as prose and the model answers as if it had happened. That is
// the worst possible failure shape — a lie the user has no way to detect.
//
// `_kiro/session/compact` does the work and emits the exact
// `summarization_completed` frame `HandleV3Summarization` already maps, which
// means vibekit's whole compaction surface (the boundary marker, the collapsible
// summary, the watermark) becomes reachable for the first time.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// errCompactRefused is the ONE failure a caller can surface.
//
// KAS returns a bare `{success: false}` for a turn in flight and for a
// compaction already running, with no field distinguishing them, and throws for
// an unknown session. Inventing three messages from one bit would be guessing at
// the user's expense, so there is one honest message naming the only cause they
// can act on.
var errCompactRefused = errors.New("can't compact right now — finish or cancel the current turn and try again")

// CmdCompact compacts the chat's context through KAS's native verb.
//
// Requires a LIVE RESIDENT session: compaction operates on the session's own
// message log, so there is nothing to compact without a bridge, and KAS throws
// rather than answering for a session it does not hold.
func CmdCompact(d *Dispatcher, bridges BridgeAccess, ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) { //nolint:revive // dispatcher handler signature
	if !d.RequireChatID(w, cmd) {
		return
	}
	bridge := bridges.GetBridge(cmd.ChatID)
	if bridge == nil {
		d.RespondErr(w, http.StatusConflict, errNoBridge)
		return
	}

	resp, err := bridge.Call(ctx, vibekit.MethodSessionCompact, SessionParams(bridge))
	if err != nil {
		slog.Warn("compact: call failed", "chat", cmd.ChatID, keyError, err)
		d.RespondErr(w, http.StatusBadGateway, err)
		return
	}
	var result struct {
		Success bool `json:"success"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}
	if !result.Success {
		slog.Info("compact refused", "chat", cmd.ChatID)
		d.RespondErr(w, http.StatusConflict, errCompactRefused)
		return
	}

	// No event is broadcast here. The summarization frames arrive on the session
	// wire and the translate layer turns them into the compaction boundary and
	// the watermark — so emitting anything from this handler would double-report
	// a completion KAS is already announcing.
	slog.Info("chat compacted", "chat", cmd.ChatID)
	d.Respond(w, responseWith(nil))
}
