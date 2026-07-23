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

	"github.com/cplieger/vibekit/internal/api"
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
func CmdUserInputResponse(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	sb := d.Bridge().GetBridge(cmd.ChatID)
	if sb == nil {
		d.RespondErr(w, http.StatusBadRequest, errNoBridge)
		return
	}
	var p api.UserInputResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	// An "answered" action needs answer text (KAS ignores an empty answer
	// and would advance anyway — reject so the client bug is visible);
	// "dismissed" carries none.
	switch p.Action {
	case api.UserInputActionAnswered:
		if p.Answer == "" {
			d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
			return
		}
	case api.UserInputActionDismissed:
	default:
		d.RespondErr(w, http.StatusBadRequest, ErrInvalidPayload)
		return
	}
	result := userInputResult{Action: p.Action}
	if p.Action == api.UserInputActionAnswered {
		result.Answer = p.Answer
	}
	if err := sb.Respond(ctx, p.RequestID, result, nil); err != nil {
		slog.Error("user input response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	d.Supervised().RemovePendingPerm(p.RequestID)
	d.RespondOK(w, cmd.RequestID)
}
