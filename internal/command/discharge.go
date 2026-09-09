package command

import (
	"context"

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
)

// commandDischarges classifies every command in vibekit's vocabulary. The line the
// table applies: does the event carry the user's own PROSE addressed to this agent?
// Exactly two commands do, and the prompt's own source then decides which turns it
// opened count. Total by test: TestCommandDischarges_ClassifiesEveryCommand reads the
// constant block and fails for a command that is not listed.
var commandDischarges = map[vibekit.CommandType]dischargeVerdict{
	vibekit.CmdPrompt: dischargeBySource,
	vibekit.CmdSteer:  dischargeYes,
	// A choice from a menu the AGENT wrote, not the user's prose.
	vibekit.CmdUserInputResponse: dischargeNo,
	// An authorization or a file review, not a question.
	vibekit.CmdPermissionResponse: dischargeNo,
	// An MCP server asked, mid-tool-call, not the agent.
	vibekit.CmdElicitationResponse: dischargeNo,
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

// noteAnswer discharges the chat's waiting_on_user claim when this command answered
// the agent. Called only after a handler succeeded: a refused prompt or a dropped
// steer answered nothing.
//
// The run verbs (Runs.AnswerInput, Runs.SetStepStatus) are NOT commands and reach no
// row here: a parked step's question belongs to a different agent on a different
// session, so answering one leaves this chat's claim standing.
func (d *Dispatcher) noteAnswer(ctx context.Context, cmd *vibekit.ClientCommand) {
	if d.status == nil || cmd.ChatID == "" {
		return
	}
	if commandDischarges[cmd.Type] == dischargeYes {
		d.status.DischargeWaiting(ctx, cmd.ChatID)
	}
}
