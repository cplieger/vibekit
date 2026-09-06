package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/schedule"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Runs owns the workflow-run surface: launch, cancel, retry, the durable
// lease around a run KAS is executing, the deadline and turn-cap arms, the
// recorded abnormal terminations, and the schedule store that drives unattended
// launches.
//
// mu guards bounds; claimExpiredDeadline takes mu before the lease store's own.
type Runs struct {
	chats     runChatReader
	translate runTranslator
	perms     runPermClaimer
	bus       runBroadcaster
	schedules *schedule.Store `wiring:"optional"`
	leases    *runlease.Store `wiring:"optional"`
	bridges   *bridgeManager
	coord     *BridgeCoordinator
	utility   func() *utilityRuntime
	lifecycle *lifetime
	// Separate from pendingPerms: a run ask is durable and string-keyed. Ahead of
	// bounds for fieldalignment — bounds ends in a slice, so a pointer-bearing
	// field after it widens the GC's scan range.
	asks   pendingRunAsks
	bounds runBoundsState
	mu     sync.Mutex
}

// runChatReader is the chat store as the run surface uses it: a chat's session
// chain, and (via List) the chat owning a given run's parent session.
type runChatReader interface {
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
	List(ctx context.Context) []vibekit.ChatHeader
}

// runTranslator is the translator as the run surface uses it: the two
// run-shaped notifications it wraps, the step-session seed, and the one decode
// that turns a step's `send_message` into an answerable ask.
type runTranslator interface {
	HandleRunStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse)
	HandleRunComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse)
	RecordRunSteps(raw json.RawMessage)
	// SessionNotifyAsk derives the ask a `_kiro/session/notify` frame carries, or
	// reports false when the frame is not one. Derives only; the caller broadcasts.
	SessionNotifyAsk(msg *vibekit.RPCResponse) (vibekit.RunInputNeededPayload, bool)
}

// runBroadcaster is the event fan-out as the run surface uses it: publish an
// ask, and publish its settlement. The ask is the only run event vibekit itself
// originates, because the answer path and the take-once claim are both here.
type runBroadcaster interface {
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
}

// runPermClaimer is the pending-decision tracker as the unattended floor uses
// it: claim a request so exactly one surface answers it.
type runPermClaimer interface {
	TakePendingPerm(chatID vibekit.ChatID, requestID int64, settledBy vibekit.SettledBy) bool
}

// Runs exposes the run surface to the composition root, which starts the orphan
// sweep and hands it to the schedule runner as its schedule.Launcher.
func (rt *Runtime) Runs() *Runs { return rt.runs }
