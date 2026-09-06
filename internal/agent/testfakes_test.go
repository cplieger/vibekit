package agent

// In-memory ACPBridge and ChatStore fakes shared across the agent package's tests.

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
	// deliveredSeq drives the sequence a parked settle waits for.
	deliveredSeq uint64
	callResults  map[string]json.RawMessage
	callErrs     map[string]error
	// callRPCErrs is how KAS refuses in-band; callErrs is the transport failing.
	callRPCErrs  map[string]*vibekit.RPCError
	lastParams   map[string]map[string]any
	callDeadline map[string]bool
	chunksOnCall map[string][]string
	// blockOn parks Call, after recording it, until the method's channel is closed.
	blockOn   map[string]chan struct{}
	sessionID string
	modelID   string
	effort    string
	// observedEffort is the level the SESSION reported, not the one asked for.
	observedEffort string
	currentMode    string
	servedModels   []string
	modes          []vibekit.SessionMode
	models         []vibekit.SessionModel
	sessionTitle   string
	calls          []string
	// startOpts records what the most recent spawn was actually handed.
	startOpts *vibekit.StartOpts
	// startGate holds a spawn OPEN, so a bridge-ready test is not saved by the
	// forward-attach wake racing an instantaneous Start.
	startGate chan struct{}
	// notifsOnStart is the transcript a session/load replays; Start owns the
	// push-before-return ordering it depends on.
	notifsOnStart []*vibekit.RPCResponse
	mu            sync.Mutex
	responds      int
	// setModelFailures fails the next N SetModel calls, the only route to the
	// switch-by-restart fallback.
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

	// A replay rides a Start that NAMES a session, or the utility bridge sharing this
	// factory would replay a transcript of its own. Pushing before the return is
	// load-bearing: the settle barrier reads an empty channel plus a recorded load
	// result as fully drained, so a fake that pushed afterwards would make it settle
	// on frame 1, delete the projection and drop the rest of the transcript.
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
	// Blocked OUTSIDE the mutex so concurrent Calls on other methods proceed, which is
	// the real bridge's per-request behaviour.
	if blocker != nil {
		select {
		case <-blocker:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Delivered after unlocking so the chunks arrive after the caller's Call begins.
	// notifCh is buffered, so this does not block.
	for _, text := range chunks {
		b.deliver(newChunkMsg(text))
	}
	return &vibekit.RPCResponse{Result: res}, nil
}

// deliver stamps the next sequence and pushes, as the real read loop does: a counter
// incremented on receipt instead would skew silently.
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

// setCallResult re-arms one method's canned reply under the fake's own mutex, which a
// direct assignment cannot do: once OpenBridge has run, its resume sweep calls back
// into Call, which reads these maps under b.mu, so a test-goroutine write is a real
// race. Assigning the whole map BEFORE OpenBridge stays fine.
func (b *fakeBridge) setCallResult(method string, res json.RawMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.callResults == nil {
		b.callResults = map[string]json.RawMessage{}
	}
	b.callResults[method] = res
}

// setCallErr re-arms one method's TRANSPORT failure and setCallRPCErr its in-band
// refusal, both for setCallResult's reason.
func (b *fakeBridge) setCallErr(method string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.callErrs == nil {
		b.callErrs = map[string]error{}
	}
	b.callErrs[method] = err
}

func (b *fakeBridge) setCallRPCErr(method string, err *vibekit.RPCError) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.callRPCErrs == nil {
		b.callRPCErrs = map[string]*vibekit.RPCError{}
	}
	b.callRPCErrs[method] = err
}

// setStartGate parks every later Start until the returned channel is closed,
// armed under the mutex for setCallResult's reason.
func (b *fakeBridge) setStartGate(gate chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.startGate = gate
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

// CurrentMode is the mode the SESSION ended up in, not necessarily the one StartOpts
// asked for, and is settable so a test can simulate that divergence.
func (b *fakeBridge) CurrentMode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentMode
}

// SessionTitle returns whatever a test set, so the adoption guard can be exercised.
func (b *fakeBridge) SessionTitle() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionTitle
}

// Modes and Models are nil by default, which is what a freshly constructed bridge
// answers for anything a session/load result omitted.
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

// ServedModels is nil by default, which reads as "entitlement unknowable" and is
// allowed, so a test that does not care about entitlement is unaffected.
func (b *fakeBridge) ServedModels() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.servedModels
}

func (b *fakeBridge) SetModel(_ context.Context, modelID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, "session/set_config_option")
	// The fast path has to fail for the restart to run at all; the RETRY on the
	// reopened session is the behaviour under test.
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

// ObserveEffort records the level WITHOUT making EnsureEffort differs-only: a second
// copy of the real cache rule would drift, and the healEffort tests assert what the
// heal decided to call, not what a cache would have suppressed.
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

// newNoopBridge is for benchmarks where the bridge is never called.
func newNoopBridge() ACPBridge { return &fakeBridge{notifCh: make(chan vibekit.Notification)} }

// --- Fake ChatStore (delegates to testsupport.RecordingChatStore) ---

type fakeChatStore = testsupport.RecordingChatStore

func newFakeChatStore() *fakeChatStore {
	return testsupport.NewRecordingChatStore()
}
