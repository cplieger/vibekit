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
// It exists because that surface was 74 of Runtime's 223 methods — a third of the
// receiver — over four fields NOTHING outside it touches. The measurement is
// what made the split obvious rather than aesthetic: `leases`, `schedules`,
// `runBounds` and the mutex guarding them had zero non-run readers, so they were
// already a separate object sharing a struct with everything else.
//
// It follows lifetime and bus, with one difference worth naming: those
// two are field bags that grew methods, and this one carries COLLABORATORS. Run
// work reaches the bridge manager, the coordinator, the utility runtime, the chat
// store, the translator and the pending-permission tracker, so those arrive as
// named dependencies instead of through a *Runtime back-pointer. That was a choice
// with a cost attached: three methods stay on Runtime for it (see dispatch,
// dispatchRequest and closeFinishedBridge in run_host.go), because they are
// the ACP request ladder for a run's bridge — bridge plumbing reached on a run
// topic rather than run logic — and moving them would have meant handing this
// type the chat-handler map and six responder methods, which is a back-reference
// with extra steps.
//
// The lock protocol did not cross the new boundary: mu guards bounds, and the one
// place two locks are held (claimExpiredDeadline takes mu then the lease store's
// own) is entirely inside this type.
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
// resolve a chat's session chain when cancelling its runs.
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
	TakePendingPerm(requestID int64, settledBy vibekit.SettledBy) bool
}

// Runs exposes the run surface to the composition root, which starts the orphan
// sweep and hands it to the schedule runner as its schedule.Launcher.
//
// One accessor rather than two Runtime forwards. A forward would put the runtime back in
// the path of calls that have nothing to do with it, which is the shape this
// split exists to remove; the two callers want the run surface, so they get it.
func (rt *Runtime) Runs() *Runs { return rt.runs }
