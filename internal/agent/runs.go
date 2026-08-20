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
	// --- owned outright ---

	// schedules is the durable schedule store, nil when scheduling is off.
	// Unlike leases there IS a "scheduling off" mode, so every reader checks.
	schedules *schedule.Store `wiring:"optional"`
	// leases is what vibekit knows about the runs it put on the wire. Reach it
	// through leaseStore(), which supplies an in-memory registry when the
	// durable store was not wired: a lease carries the run's wall clock, so
	// there is no "leases off" mode.
	//
	// Optional at the FIELD and required at the READ, which is why leaseStore()
	// exists — the fallback is the reason a nil here is not the wiring mistake the
	// guard hunts.
	leases *runlease.Store `wiring:"optional"`
	// bounds holds the ceiling arms, the termination claims and the recorded
	// abnormal terminations that let a run's row say what happened to it.
	bounds runBoundsState
	// mu guards bounds. Taken before the lease store's own mutex in
	// claimExpiredDeadline, the only place both are held.
	mu sync.Mutex

	// --- collaborators ---

	// bridges is the per-chat bridge registry; a run gets one keyed on its
	// synthetic run chat id.
	bridges *bridgeManager
	// coord supplies CloseBridge and Forward for a run's own bridge.
	coord *BridgeCoordinator
	// utility is a THUNK, not a value: the utility runtime is built under a
	// sync.Once whose hooks point back into runtime service surfaces (see
	// permissions_policy.go), so this type must ask for it rather than hold it.
	utility func() *utilityRuntime
	// lifecycle supplies the process lifetime, the workspace dir and the config
	// dir. One collaborator rather than three scalars, because it is the same
	// struct that already owns all three.
	lifecycle *lifetime
	// chats is read-only here: two methods call Get and nothing mutates.
	chats runChatReader
	// translate receives the three run-shaped notifications.
	translate runTranslator
	// perms lets an unattended deadline claim a pending permission before it
	// answers on the machine's behalf.
	perms runPermClaimer
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

// Runs exposes the run plane to the composition root, which starts the orphan
// sweep and hands the plane to the schedule runner as its schedule.Launcher.
//
// One accessor rather than two Runtime forwards. A forward would put the runtime back in
// the path of calls that have nothing to do with it, which is the shape this
// split exists to remove; the two callers want the run surface, so they get it.
//
// the type is package-internal on purpose and the caller only forwards it on.
//
//nolint:revive // unexported-return: same reason as BridgeCoordinator.Bridge —
func (rt *Runtime) Runs() *Runs { return rt.runs }
