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
	// loadSeq is the position a `session/load` answered at, recorded after the replay
	// frames as on the real bridge, because they precede the result on the wire.
	loadSeq     uint64
	callResults map[string]json.RawMessage
	callErrs    map[string]error
	// callRPCErrs makes Call return a REPLY carrying a JSON-RPC error, which is how KAS
	// refuses; callErrs is the other channel, the transport itself failing.
	callRPCErrs  map[string]*vibekit.RPCError
	lastParams   map[string]map[string]any
	callDeadline map[string]bool
	chunksOnCall map[string][]string
	// notifsOnCall are whole frames delivered after a named Call, unstamped by this
	// bridge's session id — which is what a `session/load` replay is. chunksOnCall
	// stamps its own id, so it can only produce frames the own-session screen drops.
	notifsOnCall map[string][]*vibekit.RPCResponse
	// blockOn makes Call block after recording, until the method's channel closes — for
	// concurrency properties like an RPC read completing during a text turn.
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
	// startOpts records the most recent Start's opts, so a test can assert what a spawn
	// was handed — that the utility bridge gets no operator launch flags, say.
	startOpts *vibekit.StartOpts
	// startGate, when non-nil, parks Start until closed, holding a spawn OPEN so a
	// bridge-ready test is not saved by the forward-attach wake racing a fast Start.
	startGate chan struct{}
	// startErr, when non-nil, fails every spawn — a fault on this server rather than a
	// statement about the run, which the REST layer classifies apart (errRunHostStart).
	startErr error
	// starts counts spawns. One factory serves the utility session AND every run bridge,
	// so "was a process started" is only meaningful as a DELTA across the call.
	starts int
	// notifsOnStart is the transcript a session/load replays. Start pushes them BEFORE it
	// returns, the ordering the settle barrier depends on — see Start.
	notifsOnStart []*vibekit.RPCResponse
	mu            sync.Mutex
	responds      int
	// setModelFailures makes the next N SetModel calls fail; the switch-by-restart
	// fallback is only reachable when the fast path does.
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
	startErr := b.startErr
	b.mu.Unlock()
	if startErr != nil {
		return startErr
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.mu.Lock()
	b.started = true
	b.starts++
	b.startOpts = opts
	if opts.SessionID != "" {
		b.sessionID = opts.SessionID
	}
	notifs := b.notifsOnStart
	b.mu.Unlock()

	// A replay belongs to session/load, so these ride a Start that NAMES a session:
	// otherwise the utility bridge, which shares this factory, would replay a transcript
	// of its own. Pushing BEFORE the return is what makes the fake honest — a fake that
	// returned first would answer at a position the consumer has already passed, and the
	// barrier would settle on frame 1 and drop the rest of the transcript.
	if opts.SessionID == "" {
		return nil
	}
	for _, n := range notifs {
		b.deliver(n)
	}
	// AFTER the replay, as on the real bridge: CallAt stamps the position already
	// delivered when the response arrived, and every replay frame precedes it.
	b.mu.Lock()
	b.loadSeq = b.deliveredSeq
	b.mu.Unlock()
	return nil
}

// SessionLoadSeq is the read loop position the `session/load` response arrived at.
// Zero until a Start naming a session has run, exactly as on the real bridge.
func (b *fakeBridge) SessionLoadSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loadSeq
}

// lastStartOpts returns the StartOpts of the most recent Start, or nil.
func (b *fakeBridge) lastStartOpts() *vibekit.StartOpts {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startOpts
}

// startCount is how many spawns this factory has served. Read as a delta around
// the call under test — see the `starts` field.
func (b *fakeBridge) startCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.starts
}

// isStopped reports whether Stop has been called. The re-host's lost-race arm is
// the one place a caller must be able to see that a bridge it was NOT handed was
// torn down, and presence in the bridge map cannot answer it — the loser's bridge
// was never in the map.
func (b *fakeBridge) isStopped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped
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
	frames := b.notifsOnCall[method]
	blocker := b.blockOn[method]
	sessionID := b.sessionID
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
	// After unlocking, so the chunks arrive once the caller's Call has begun; notifCh is
	// buffered, so this cannot block. Stamped with this bridge's OWN session id, because
	// an unstamped frame is one the own-session screen is right to drop.
	for _, text := range chunks {
		b.deliver(newSessionChunkMsg(sessionID, text))
	}
	// Delivered BEFORE the return, the ordering the settle barrier rests on: CallAt's
	// position below counts these, so reaching it means they have all been folded.
	for _, f := range frames {
		b.deliver(f)
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

// setCallResult re-arms one method's canned reply under the fake's own mutex, for use AFTER
// the bridge is running: OpenBridge spawns tryLoadSession, which calls back into Call, and
// Call reads these maps under b.mu, so an unguarded write from the test goroutine is a real
// data race. Assigning the whole map before OpenBridge stays fine.
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
	// The only way to reach the switch-by-restart fallback: the fast path has to fail for
	// the restart to run, and the RETRY on the reopened session is the behaviour under test.
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
