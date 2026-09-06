package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/schedule"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Runs owns the workflow-run surface: launch, cancel, retry, the durable lease, the
// deadline and turn-cap arms, and the schedule store that drives unattended launches.
// It carries COLLABORATORS rather than a *Runtime back-pointer; the ACP request
// ladder for a run's bridge stays on Runtime. `mu` guards bounds.
type Runs struct {
	chats     runChatReader
	translate runTranslator
	perms     runPermClaimer
	bus       runBroadcaster
	// tabs opens the tab a starting run offers its launching chat. REQUIRED, not
	// optional: offerRunTab's nil check is for a bare &Runs{} in a test.
	tabs      runTabOpener
	schedules *schedule.Store `wiring:"optional"`
	leases    *runlease.Store `wiring:"optional"`
	bridges   *bridgeManager
	coord     *BridgeCoordinator
	utility   func() *utilityRuntime
	lifecycle *lifetime
	// asks holds the questions a step asked and nobody answered — its own registry
	// because a run ask is durable where a permission dies with its bridge. run_ask.go.
	asks pendingRunAsks
	// stepReplays holds the step-transcript reads in flight (step_replay.go). HERE
	// rather than on the utility session, which must stay ignorant of workflows.
	stepReplays stepReplays
	// carriers counts which run carriers a verb is holding right now — the one fact
	// the kept-carrier bound cannot infer from a timeout (run_host.go).
	carriers carrierUse
	bounds   runBoundsState
	mu       sync.Mutex
}

// runChatReader is the chat store as the run surface uses it: resolve a chat's
// session chain. Read in both directions — from a chat to the runs its sessions
// launched, and from a run's parent session back to the chat whose bridge hosts it.
type runChatReader interface {
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
}

// runTranslator is the translator as the run surface uses it: the two run-shaped
// notifications it wraps, the step-session seed, and one ask decode.
type runTranslator interface {
	HandleRunStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse)
	HandleRunComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse)
	RecordRunSteps(raw json.RawMessage)
	// ForgetRunSteps drops a run's step-session registry entries. The GATE is this
	// side's, because `paused` reaches `run_complete` on a run still going.
	ForgetRunSteps(workflowID string)
	// SessionNotifyAsk derives the ask a `_kiro/session/notify` frame carries, or
	// reports false. A DERIVATION: this surface owns the ask's whole lifecycle.
	SessionNotifyAsk(msg *vibekit.RPCResponse) (vibekit.RunInputNeededPayload, bool)
}

// runBroadcaster is the event fan-out as the run surface uses it: publish an ask and
// its settlement. The ask is the only run event vibekit itself originates.
type runBroadcaster interface {
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
}

// runPermClaimer is the pending-decision tracker as the run surface uses it: claim a
// request so exactly one surface answers it, and drop a run's unanswered decisions
// when it ends. The run-terminal clear is here rather than on the chat-scoped door
// because a step's ask is keyed to the LAUNCHING chat, which outlives the run.
type runPermClaimer interface {
	TakePendingPerm(chatID vibekit.ChatID, requestID int64, settledBy vibekit.SettledBy) bool
	ClearPendingPermsForRun(workflowID string)
}

// Runs exposes the run surface to the composition root, which starts the orphan
// sweep and hands it to the schedule runner as its schedule.Launcher.
func (rt *Runtime) Runs() *Runs { return rt.runs }
