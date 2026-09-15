package command

import (
	"context"
	"encoding/json"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// dischargeVerdict says whether one command is the user ANSWERING a question the
// chat's own agent asked, which is what discharges a retained waiting_on_user status.
type dischargeVerdict int

const (
	// dischargeNo: this command answers no question the agent asked. The zero value,
	// so an unclassified command is silent rather than wrong; the completeness test is
	// what catches it.
	dischargeNo dischargeVerdict = iota
	// dischargeYes: the dispatcher discharges once the handler has succeeded.
	dischargeYes
	// dischargeBySource: the TURN's source decides (vibekit.TurnOpenSource.UserAnswered),
	// because one command covers both a prompt and a `!cmd`.
	dischargeBySource
	// dischargeByAnswer: the command's own PAYLOAD decides, because one command type
	// carries both the user supplying an answer and the user walking away from the
	// card. answersAgent owns that rule and answers false for anything it cannot read
	// as an answer, so an absent, malformed or unknown payload keeps the claim.
	dischargeByAnswer
)

// commandDischarges classifies every command in vibekit's vocabulary. The line the
// table applies: does the event carry the user ANSWERING this agent — their own prose,
// or a decision supplied through one of the structured channels the agent asks on? The
// prompt's own source then decides which turns it opened count, and the three
// structured channels defer to their payload, because each carries a walk-away as well
// as an answer. Total by test: TestCommandDischarges_ClassifiesEveryCommand reads the
// constant block and fails for a command that is not listed.
var commandDischarges = map[vibekit.CommandType]dischargeVerdict{
	vibekit.CmdPrompt: dischargeBySource,
	vibekit.CmdSteer:  dischargeYes,
	// The agent's OWN structured question (_kiro/userInput), so an answer to it is the
	// user answering this agent as squarely as a prompt is. Was dischargeNo on the
	// ground that a menu the agent wrote is not the user's prose — true, and the wrong
	// line: an agent that asks through a card and gets an answer is not still waiting,
	// yet nothing invalidated the claim, so the amber dot outlived every such turn.
	vibekit.CmdUserInputResponse: dischargeByAnswer,
	// An authorization or a file review rather than a question — but it is one of the
	// channels an agent that declared waiting_on_user asks on, and selecting an option
	// the request itself advertised is the user deciding.
	vibekit.CmdPermissionResponse: dischargeByAnswer,
	// An MCP server asked, mid-tool-call, not the agent — reached anyway, because the
	// agent's own tool call is what raised the form and the user filling it in is the
	// answer that unblocks the turn.
	vibekit.CmdElicitationResponse: dischargeByAnswer,
	// A rewind or a compact removes the claim's QUESTION from history rather than
	// answering it, so both leave a stale-TRUE the next prompt discharges.
	vibekit.CmdRewindChat: dischargeNo,
	vibekit.CmdCompact:    dischargeNo,

	vibekit.CmdCreateChat:        dischargeNo,
	vibekit.CmdResumeSession:     dischargeNo,
	vibekit.CmdForkChat:          dischargeNo,
	vibekit.CmdCancel:            dischargeNo,
	vibekit.CmdDeleteChat:        dischargeNo,
	vibekit.CmdSwitchModel:       dischargeNo,
	vibekit.CmdSetEffort:         dischargeNo,
	vibekit.CmdSetDraft:          dischargeNo,
	vibekit.CmdSetAttachments:    dischargeNo,
	vibekit.CmdSetMode:           dischargeNo,
	vibekit.CmdCreateHook:        dischargeNo,
	vibekit.CmdSetSupervisedMode: dischargeNo,
	vibekit.CmdSteerClear:        dischargeNo,
	vibekit.CmdOpenTab:           dischargeNo,
	vibekit.CmdCloseTab:          dischargeNo,
	vibekit.CmdReorderTabs:       dischargeNo,
	vibekit.CmdPinTab:            dischargeNo,
}

// ChatStatus is the one thing a command handler needs of the agent's self-declared
// chat status: end the retained waiting_on_user claim, because this command IS the
// user answering. Write-only, like SteerRecorder.
type ChatStatus interface {
	DischargeWaiting(ctx context.Context, chatID vibekit.ChatID)
}

// answersAgent reports whether cmd's payload states the user SUPPLYING an answer on one
// of the structured channels the agent asks on. It is the dischargeByAnswer rule, and it
// FAILS TOWARD KEEPING the claim: every arm reads one field the payload must state
// affirmatively, so an absent payload (json.Unmarshal of a nil RawMessage errors), a
// malformed one, an action value from outside the channel's vocabulary, and a walk-away
// all answer false and leave the claim standing. A wrongly-kept claim is an amber dot the
// next prompt clears; a wrongly-cleared one hides that the agent needs somebody.
//
// Read only after the handler succeeded, so each arm is reading a payload the handler
// already validated and forwarded. The checks are not redundant with that: they are what
// tells an answer from the walk-away the same handler accepts.
func answersAgent(cmd *vibekit.ClientCommand) bool {
	switch cmd.Type {
	case vibekit.CmdUserInputResponse:
		// A dismissal advances the agent to its next phase without the user answering,
		// so whether they still owe one is exactly the ambiguity that keeps the claim.
		var p vibekit.UserInputResponseCommand
		if json.Unmarshal(cmd.Payload, &p) != nil {
			return false
		}
		return p.Action == vibekit.UserInputActionAnswered && p.Answer != ""
	case vibekit.CmdElicitationResponse:
		// accept is the only action carrying the form's values; decline and cancel
		// resolve the request having answered nothing it asked.
		var p vibekit.ElicitationResponseCommand
		if json.Unmarshal(cmd.Payload, &p) != nil {
			return false
		}
		return p.Action == vibekit.ElicitationActionAccept
	case vibekit.CmdPermissionResponse:
		// The option's KIND is on the REQUEST, never on this reply, so allow and reject
		// are indistinguishable here — and both are the user deciding, which is what
		// ends the wait. What the reply does prove is that a selection was made: the
		// handler refuses an id the request did not advertise, so a non-empty option id
		// past that gate is one of the agent's own options.
		var p vibekit.PermissionResponseCommand
		if json.Unmarshal(cmd.Payload, &p) != nil {
			return false
		}
		return p.OptionID != ""
	}
	return false
}

// discharges reads the table's verdict for cmd, deferring to the turn source (which the
// prompt path applies at StartTurn, not here) and to the payload where the verdict says
// so. False for a command the table does not classify, which is the same fail-toward-
// keeping direction dischargeNo's zero value carries.
func discharges(cmd *vibekit.ClientCommand) bool {
	switch commandDischarges[cmd.Type] {
	case dischargeYes:
		return true
	case dischargeByAnswer:
		return answersAgent(cmd)
	}
	return false
}

// noteAnswer discharges the chat's waiting_on_user claim when this command answered
// the agent. Called only after a handler succeeded: a refused prompt, a dropped steer
// or a permission answer the tracker rejected answered nothing.
//
// The run verbs (Runs.AnswerInput, Runs.SetStepStatus) are NOT commands and reach no
// row here: a parked step's question belongs to a different agent on a different
// session, so answering one leaves this chat's claim standing.
func (d *Dispatcher) noteAnswer(ctx context.Context, cmd *vibekit.ClientCommand) {
	if d.status == nil || cmd.ChatID == "" {
		return
	}
	if discharges(cmd) {
		d.status.DischargeWaiting(ctx, cmd.ChatID)
	}
}
