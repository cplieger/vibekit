// Package command intercepts `/compact` before it reaches KAS, since typed
// slash commands are not parsed there and would otherwise reach the model
// as prose. It calls the real `_kiro/session/compact` verb instead.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// errCompactRefused is the one failure a caller can surface. KAS returns a
// bare {success: false} both for a turn in flight and for a compaction
// already running, with no field distinguishing them.
var errCompactRefused = errors.New("can't compact right now — finish or cancel the current turn and try again")

// CmdCompact compacts the chat's context through KAS's native verb. Requires
// a live resident session, since compaction operates on the session's own
// message log.
func CmdCompact(ctx context.Context, bridges BridgeAccess, cmd *vibekit.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	bridge := bridges.Bridge(cmd.ChatID)
	if bridge == nil {
		return nil, StatusError(http.StatusConflict, errNoBridge)
	}

	resp, err := bridge.Call(ctx, vibekit.MethodSessionCompact, SessionParams(bridge))
	if err != nil {
		slog.Warn("compact: call failed", "chat", cmd.ChatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, err)
	}
	var result struct {
		Success bool `json:"success"`
	}
	if resp != nil && resp.Result != nil {
		_ = json.Unmarshal(resp.Result, &result)
	}
	if !result.Success {
		slog.Info("compact refused", "chat", cmd.ChatID)
		return nil, StatusError(http.StatusConflict, errCompactRefused)
	}

	// Reports ACCEPTANCE, never compaction: `{success: true}` covers five
	// outcomes and nothing on the wire separates them, three of which compacted
	// nothing. The transcript boundary rides the wire's own summarization frame,
	// which this verb can withhold on a committed compaction — so its absence is
	// not an error and must not be synthesized from `success`. The narrow
	// `BridgeAccess` parameter is what enforces that: no store, no broadcaster,
	// so none of it is expressible here. Keep it narrow. Outcomes and the two
	// withholding paths: `vibekit-acp.md` "Upstream 2.21.1".
	slog.Info("compact accepted", "chat", cmd.ChatID)
	return responseWith(nil), nil
}
