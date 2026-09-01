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
// It exists because that surface was 74 of Runtime's 223 methods over four
// fields nothing outside it touches — leases, schedules, runBounds and the
// mutex guarding them had zero non-run readers.
//
// It carries COLLABORATORS rather than a *Runtime back-pointer: run work
// reaches the bridge manager, the coordinator, the utility runtime, the chat
// store, the translator and the pending-permission tracker, so those arrive as
// named dependencies. Three methods stay on Runtime regardless (dispatch,
// dispatchRequest and closeFinishedBridge in run_host.go) because they are the
// ACP request ladder for a run's bridge, and moving them would have meant
// handing this type the chat-handler map and six responder methods.
//
// The lock protocol did not cross the new boundary: mu guards bounds, and the
// one place two locks are held (claimExpiredDeadline takes mu then the lease
// store's own) is entirely inside this type.
type Runs struct {
	chats     runChatReader
	translate runTranslator
	perms     runPermClaimer
	schedules *schedule.Store `wiring:"optional"`
	leases    *runlease.Store `wiring:"optional"`
	bridges   *bridgeManager
	coord     *BridgeCoordinator
	utility   func() *utilityRuntime
	lifecycle *lifetime
	bounds    runBoundsState
	mu        sync.Mutex
}

// runChatReader is the chat store as the run surface uses it: one method, to
// resolve a chat's session chain. Read in both directions — from a chat to the
// runs its sessions launched (cancel on close, resume on rehydrate), and from a
// run's parent session back to the chat whose bridge hosts it (hostBridge).
type runChatReader interface {
	Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool)
}

// runTranslator is the translator as the run surface uses it: the three
// run-shaped notifications, and nothing else of its wide surface.
type runTranslator interface {
	HandleRunStart(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse)
	HandleRunComplete(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse)
	RecordRunSteps(raw json.RawMessage)
}

// runPermClaimer is the pending-decision tracker as the unattended floor uses
// it: claim a request so exactly one surface answers it.
type runPermClaimer interface {
	TakePendingPerm(chatID vibekit.ChatID, requestID int64, settledBy vibekit.SettledBy) bool
}

// Runs exposes the run surface to the composition root, which starts the orphan
// sweep and hands it to the schedule runner as its schedule.Launcher.
func (rt *Runtime) Runs() *Runs { return rt.runs }
