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
func CmdElicitationResponse(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	sb := d.Bridge().GetBridge(cmd.ChatID)
	if sb == nil {
		d.RespondErr(w, http.StatusBadRequest, errNoBridge)
		return
	}
	var p vibekit.ElicitationResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	if !validElicitationAction(p.Action) {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	// Take before responding, for the same reason CmdPermission does: an
	// elicitation form open in two tabs can be submitted twice, and the second
	// ElicitResult is dropped by the MCP server's caller without a word.
	if !d.PendingPerms().TakePendingPerm(p.RequestID, vibekit.SettledByUser) {
		d.RespondErr(w, http.StatusConflict, errAlreadyAnswered)
		return
	}
	result := vibekit.ElicitationResult{Action: p.Action}
	// Content only travels on accept; decline/cancel carry no values.
	if p.Action == vibekit.ElicitationActionAccept {
		result.Content = p.Content
	}
	if err := sb.Respond(ctx, p.RequestID, result, nil); err != nil {
		slog.Error("elicitation response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	d.RespondOK(w, cmd.RequestID)
}
