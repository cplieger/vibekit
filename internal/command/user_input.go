package command

// User-input answer forwarding: the user answered (or dismissed) the
// agent's structured question (_kiro/userInput, kiro-cli 2.14+); relay
// the result to kiro-cli on the same JSON-RPC id the request carried.
// Mirrors CmdElicitationResponse — the flow is request/response shaped
// just like permission prompts.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// userInputResult is the _kiro/userInput response body. KAS acts on
// {action:"answered", answer:"<text>"}; any other action (or an empty
// answer) makes the agent advance to its next phase, which is exactly
// the dismissal semantic.
type userInputResult struct {
	Action string `json:"action"`
	Answer string `json:"answer,omitempty"`
}

// CmdUserInputResponse forwards the user's answer to kiro-cli as the
// _kiro/userInput response.
func CmdUserInputResponse(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, cmd *vibekit.ClientCommand) (any, error) {
	sb := bridges.Bridge(cmd.ChatID)
	if sb == nil {
		return nil, StatusError(http.StatusBadRequest, errNoBridge)
	}
	var p vibekit.UserInputResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// An "answered" action needs answer text (KAS ignores an empty answer
	// and would advance anyway — reject so the client bug is visible);
	// "dismissed" carries none.
	switch p.Action {
	case vibekit.UserInputActionAnswered:
		if p.Answer == "" {
			return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
		}
	case vibekit.UserInputActionDismissed:
	default:
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// Take before responding, as CmdPermission does: the agent advances on the
	// FIRST answer it receives, so a second tab's answer is both discarded and
	// invisible, and the question the user actually answered stops being knowable.
	if !perms.TakePendingPerm(p.RequestID, vibekit.SettledByUser) {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	result := userInputResult{Action: p.Action}
	if p.Action == vibekit.UserInputActionAnswered {
		result.Answer = p.Answer
	}
	if err := sb.Respond(ctx, p.RequestID, result, nil); err != nil {
		slog.Error("user input response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	return responseOK, nil
}
