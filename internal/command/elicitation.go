package command

// Elicitation response forwarding: the user answered (or dismissed) an
// MCP elicitation form; relay the result to kiro-cli on the same
// JSON-RPC id the elicitation/create request carried. Mirrors
// CmdPermission — the elicitation flow is request/response shaped just
// like permission prompts.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// validElicitationAction reports whether a is an accepted MCP
// ElicitResult action.
func validElicitationAction(a string) bool {
	switch a {
	case vibekit.ElicitationActionAccept, vibekit.ElicitationActionDecline, vibekit.ElicitationActionCancel:
		return true
	}
	return false
}

// CmdElicitationResponse forwards the user's elicitation form answer to
// kiro-cli as the elicitation/create response.
func CmdElicitationResponse(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, cmd *vibekit.ClientCommand) (any, error) {
	sb := bridges.GetBridge(cmd.ChatID)
	if sb == nil {
		return nil, StatusError(http.StatusBadRequest, errNoBridge)
	}
	var p vibekit.ElicitationResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validElicitationAction(p.Action) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// Take before responding, for the same reason CmdPermission does: an
	// elicitation form open in two tabs can be submitted twice, and the second
	// ElicitResult is dropped by the MCP server's caller without a word.
	if !perms.TakePendingPerm(p.RequestID, vibekit.SettledByUser) {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	result := vibekit.ElicitationResult{Action: p.Action}
	// Content only travels on accept; decline/cancel carry no values.
	if p.Action == vibekit.ElicitationActionAccept {
		result.Content = p.Content
	}
	if err := sb.Respond(ctx, p.RequestID, result, nil); err != nil {
		slog.Error("elicitation response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	return responseOK, nil
}
