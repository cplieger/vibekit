package agent

// Test fakes: in-memory implementations of ACPBridge and ChatStore
// interfaces for use across agent package tests.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// --- Fake ACP bridge ---

type fakeBridge struct {
	notifCh chan vibekit.Notification
	// deliveredSeq stamps each delivered frame, the way the real read loop does, so
	// a test can drive the sequence the parked settle waits for.
	deliveredSeq uint64
	callResults  map[string]json.RawMessage
	callErrs     map[string]error
	// callRPCErrs makes Call return a REPLY carrying a JSON-RPC error, which is
	// how KAS actually refuses: the transport succeeds and the reason travels
	// in-band. callErrs is the other channel — the transport itself failing.
	callRPCErrs  map[string]*vibekit.RPCError
	lastParams   map[string]map[string]any
	callDeadline map[string]bool
	chunksOnCall map[string][]string
	// blockOn optionally makes Call block (after recording the call) until
	// the method's channel is closed — for tests proving concurrency
	// properties (e.g. RPC reads completing while a text turn is in flight).
	blockOn   map[string]chan struct{}
	sessionID string
	modelID   string
	effort    string
	// observedEffort is the last level ObserveEffort was handed — the level the
	// SESSION reported, which the real bridge folds into its differs-only cache.
	observedEffort string
	currentMode    string
	servedModels   []string
	modes          []vibekit.SessionMode
	models         []vibekit.SessionModel
	sessionTitle   string
	calls          []string
	// startOpts records the StartOpts of the most recent Start, so a test can
	// assert what a spawn was actually handed (e.g. that the utility bridge
	// gets no operator launch flags).
	startOpts *vibekit.StartOpts
	// startGate, when non-nil, parks Start until closed — the seam that holds a
	// spawn OPEN, so a test of the bridge-ready transition is not saved by the
	// forward-attach wake racing an instantaneous Start.
	startGate chan struct{}
	// notifsOnStart is the transcript a session/load replays. Start pushes these
	// BEFORE it returns, which is the ordering the real bridge has and the one
	// the settle barrier's correctness depends on — see Start.
	notifsOnStart []*vibekit.RPCResponse
	mu            sync.Mutex
	responds      int
	// setModelFailures makes the next N SetModel calls fail. The switch-by-restart
	// fallback is only reachable when the fast path fails, so a test that wants it
	// arms this with 1.
	setModelFailures int
	stopped          bool
	started          bool
}

func newFakeBridge() *fakeBridge {
	return &fakeBridge{
		sessionID: "fake-sess-" + time.Now().Format("150405.000"),
		modelID:   "fake-model",
		notifCh:   make(chan vibekit.Notification, 16),
	}
}

func (b *fakeBridge) Start(ctx context.Context, opts *vibekit.StartOpts) error {
	b.mu.Lock()
	gate := b.startGate
	b.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.mu.Lock()
	b.started = true
	b.startOpts = opts
	if opts.SessionID != "" {
		b.sessionID = opts.SessionID
	}
	notifs := b.notifsOnStart
	b.mu.Unlock()

	// A replay belongs to session/load, so these ride a Start that NAMES a
	// session and never a session/new — otherwise the utility bridge, which
	// shares this factory, would replay a transcript of its own.
	//
	// Pushing before the return is what makes the fake honest, not a detail:
	// the settle barrier (bridge_coord.go's Forward) treats an empty channel
	// plus a recorded load result as "the replay is fully drained", which is
	// sound ONLY because every replay frame was pushed before that result. A
	// fake that returns first and lets the test push afterwards inverts that,
	// and the barrier then settles on frame 1, deletes the projection, and
	// drops the rest of the transcript.
	if opts.SessionID == "" {
		return nil
	}
	for _, n := range notifs {
		b.deliver(n)
	}
	return nil
}

// lastStartOpts returns the StartOpts of the most recent Start, or nil.
func (b *fakeBridge) lastStartOpts() *vibekit.StartOpts {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startOpts
}

func (b *fakeBridge) Stop() {
	b.mu.Lock()
	if !b.stopped {
		b.stopped = true
		close(b.notifCh)
	}
	b.mu.Unlock()
}

func (b *fakeBridge) Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error) {
	b.mu.Lock()
	b.calls = append(b.calls, method)
	if p, ok := params.(map[string]any); ok {
		if b.lastParams == nil {
			b.lastParams = map[string]map[string]any{}
		}
		b.lastParams[method] = p
	}
	if b.callDeadline == nil {
		b.callDeadline = map[string]bool{}
	}
	_, hasDeadline := ctx.Deadline()
	b.callDeadline[method] = hasDeadline
	if err, ok := b.callErrs[method]; ok {
		b.mu.Unlock()
		return nil, err
	}
	if rpcErr, ok := b.callRPCErrs[method]; ok {
		b.mu.Unlock()
		return &vibekit.RPCResponse{Error: rpcErr}, nil
	}
	res := json.RawMessage(`{"stopReason":"end_turn"}`)
	if r, ok := b.callResults[method]; ok {
		res = r
	}
	chunks := b.chunksOnCall[method]
	blocker := b.blockOn[method]
	b.mu.Unlock()
	// Block OUTSIDE the fake's mutex so concurrent Calls on other methods
	// proceed (mirrors the real bridge, whose Call blocks per-request).
	if blocker != nil {
		select {
		case <-blocker:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Deliver configured response chunks on notifCh AFTER unlocking so they
	// arrive after the caller's Call begins (the forward goroutine moves them
	// to responseCh). notifCh is buffered, so this doesn't block.
	for _, text := range chunks {
		b.deliver(newChunkMsg(text))
	}
	return &vibekit.RPCResponse{Result: res}, nil
}

// deliver stamps the next sequence on a frame and pushes it, exactly as the real
// read loop does. Stamping in the fake rather than counting on the far side is
// the same reason production does it here: a counter incremented on receipt skews
// silently.
func (b *fakeBridge) deliver(msg *vibekit.RPCResponse) {
	b.mu.Lock()
	b.deliveredSeq++
	seq := b.deliveredSeq
	b.mu.Unlock()
	b.notifCh <- vibekit.Notification{Msg: msg, Seq: seq}
}

// CallAt is Call plus the read loop position at which the response arrived.
func (b *fakeBridge) CallAt(ctx context.Context, method string, params any) (*vibekit.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	b.mu.Lock()
	seq := b.deliveredSeq
	b.mu.Unlock()
	return resp, seq, err
}

// paramsFor returns the params captured for the most recent Call to method.
func (b *fakeBridge) paramsFor(method string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastParams[method]
}

// callHadDeadline reports whether the most recent Call to method ran with a
// context deadline (proving the caller wrapped it in a timeout).
func (b *fakeBridge) callHadDeadline(method string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.callDeadline[method]
}

func (b *fakeBridge) Notify(_ context.Context, _ string, _ any) error { return nil }

func (b *fakeBridge) Respond(_ context.Context, _ int64, _ any, _ error) error {
	b.mu.Lock()
	b.responds++
	b.mu.Unlock()
	return nil
}

// respondCount reports how many A→C requests were answered on this bridge.
func (b *fakeBridge) respondCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.responds
}

// callLog snapshots the ordered method names Call received.
func (b *fakeBridge) callLog() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.calls))
	copy(out, b.calls)
	return out
}

// setCallResult re-arms one method's canned reply under the fake's own mutex.
//
// Needed rather than assigning into callResults directly, because a test that
// has already opened a bridge shares this fake with a LIVE goroutine: OpenBridge
// spawns tryLoadSession, which reaches resumeInterruptedRuns and calls back into
// Call, and Call reads every one of these maps under b.mu. An unguarded write
// from the test goroutine is then a genuine data race, and the detector reported
// it as one.
//
// Assigning the whole map before OpenBridge stays fine and is what the seed
// helpers do; this is for re-arming a reply AFTER the bridge is running.
func (b *fakeBridge) setCallResult(method string, res json.RawMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.callResults == nil {
		b.callResults = map[string]json.RawMessage{}
	}
	b.callResults[method] = res
}

// lastCall is the most recent Call's method, or "".
func (b *fakeBridge) lastCall() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		return ""
	}
	return b.calls[len(b.calls)-1]
}

func (b *fakeBridge) SessionID() vibekit.SessionID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return vibekit.SessionID(b.sessionID)
}

func (b *fakeBridge) ModelID() vibekit.ModelID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return vibekit.ModelID(b.modelID)
}

// CurrentMode reports the mode the SESSION ended up in, which is not necessarily
// the one StartOpts asked for: applyInitialMode warns and continues when
// session/set_mode is refused. Settable so a test can simulate that divergence.
func (b *fakeBridge) CurrentMode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentMode
}

// SessionTitle reports KAS's own session title. The fake returns the value
// tests set on it so the bridge_coord adoption guard can be exercised.
func (b *fakeBridge) SessionTitle() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionTitle
}

// Modes and Models report what the fake session advertises. Nil by default,
// which is what a FRESHLY constructed bridge answers for anything a session/load
// result omitted — the shape applyLoadedSessionFacts has to survive.
func (b *fakeBridge) Modes() []vibekit.SessionMode {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modes
}

func (b *fakeBridge) Models() []vibekit.SessionModel {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.models
}

// ServedModels reports the ids this fake session advertises. Nil by default, which
// vibekit.ModelServed reads as "entitlement unknowable" and allows — so a test that
// does not care about entitlement is unaffected, and one that does sets it.
func (b *fakeBridge) ServedModels() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.servedModels
}

func (b *fakeBridge) SetModel(_ context.Context, modelID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, "session/set_config_option")
	// setModelFailures makes the first N swaps fail, which is the only way to reach
	// the switch-by-restart fallback: the fast path is what has to fail for the
	// restart to run at all, and the RETRY on the reopened session is the behaviour
	// under test.
	if b.setModelFailures > 0 {
		b.setModelFailures--
		return errors.New("fake: model swap refused")
	}
	b.modelID = modelID
	return nil
}

func (b *fakeBridge) EnsureEffort(_ context.Context, level string) error {
	b.mu.Lock()
	b.effort = level
	b.calls = append(b.calls, "session/set_config_option")
	b.mu.Unlock()
	return nil
}

// ObserveEffort records the level handed over WITHOUT making EnsureEffort
// differs-only. Deliberate: the real bridge's cache rule has one implementation
// and a second one here would drift, and the tests over healEffort assert what the
// HEAL decided to call, not what the cache would have suppressed.
func (b *fakeBridge) ObserveEffort(level string) {
	b.mu.Lock()
	b.observedEffort = level
	b.mu.Unlock()
}

// lastObservedEffort reports the level the last ObserveEffort recorded. Empty
// means the bridge was never told what the session reports.
func (b *fakeBridge) lastObservedEffort() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.observedEffort
}

// lastEffort reports the level the last SetEffort applied, for the model-switch
// re-assert tests. Empty means SetEffort was never called.
func (b *fakeBridge) lastEffort() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.effort
}

func (b *fakeBridge) NotifCh() <-chan vibekit.Notification { return b.notifCh }

// newNoopBridge returns a zero-value fakeBridge suitable for benchmarks
// where the bridge is never actually called. Replaces the former stubBridge type.
func newNoopBridge() ACPBridge { return &fakeBridge{notifCh: make(chan vibekit.Notification)} }

// --- Fake ChatStore (delegates to testsupport.RecordingChatStore) ---

type fakeChatStore = testsupport.RecordingChatStore

func newFakeChatStore() *fakeChatStore {
	return testsupport.NewRecordingChatStore()
}
