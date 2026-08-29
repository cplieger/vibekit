package translate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// baseDeps is a composable Deps implementation for tests and benchmarks.
// By default all methods are no-ops; set the hook fields to override
// specific behaviors (e.g. onBroadcast to capture events).
type baseDeps struct {
	store       ChatRecords
	bufStore    *turnBuffers
	lineTracker *buffer.LineTracker
	onBroadcast func(context.Context, vibekit.ServerEvent)
	// onSetGovernance, when set, is invoked by SetGovernance so a test can
	// assert the runtime-side cache write (mirrors onBroadcast).
	onSetGovernance func(vibekit.GovernanceStatePayload)
	// scheduledRuns are the workflow ids IsScheduled answers true for, so a
	// test can stage a scheduled run without a runtime or a scheduler.
	scheduledRuns map[string]bool
	// stepCapBreaches records every StepTurnCapExceeded call, so a test can
	// assert the per-step turn cap fired once and named the right step without a
	// agent behind it.
	stepCapBreaches []stepCapBreach
	// turnInterrupts records every InterruptTurn call, so a test can assert the
	// sentinel ended the turn exactly once and named its cause, with no bridge
	// behind it.
	turnInterrupts []turnInterrupt
	// parent is returned by ParentACPSession; zero value "" preserves the
	// historical "parent unknown" behavior for existing callers.
	parent string
	// asked records every BridgeRespond call, so a test can assert that a frame
	// vibekit declined to process was still ANSWERED on its own id, with no
	// bridge behind it. respondErr, when set, is what BridgeRespond reports.
	asked      []askAnswer
	respondErr error
	// terminals stands in for the runtime's agent-terminal registry, keyed by
	// terminal id, so adoptTerminalOutput is exercisable without one. A key
	// present with an empty text is a REGISTERED terminal that printed nothing,
	// which the real registry reports as ok — the distinction the miss warning
	// keys on.
	terminals map[string]termRendered
	// brackets records every turn-lifecycle call, so a test can assert which
	// bracket a frame drove without a turn registry behind it.
	brackets []turnBracket
}

// termRendered is one terminal's rendered output in the stub registry.
type termRendered struct {
	text  string
	spans []vibekit.TextSpan
}

func newBaseDeps() *baseDeps {
	return &baseDeps{
		store:       nopChatRecords{},
		bufStore:    newTurnBuffers(),
		lineTracker: buffer.NewLineTracker(),
		terminals:   map[string]termRendered{},
	}
}

func (d *baseDeps) Output(terminalID string) (string, []vibekit.TextSpan, bool) {
	t, ok := d.terminals[terminalID]
	return t.text, t.spans, ok
}

func (d *baseDeps) Broadcast(ctx context.Context, evt vibekit.ServerEvent) {
	if d.onBroadcast != nil {
		d.onBroadcast(ctx, evt)
	}
}

// The three GETTERS this double used to answer (ChatRecords, BufferStore,
// LineTracker) are gone with the composites: Roles holds each interface
// directly, so the double implements the methods instead of handing back a
// narrower self.
func (d *baseDeps) Get(ctx context.Context, id vibekit.ChatID) (*vibekit.Chat, bool) {
	return d.store.Get(ctx, id)
}

func (d *baseDeps) Mutate(ctx context.Context, id vibekit.ChatID, fn func(*vibekit.Chat, bool) bool) error {
	return d.store.Mutate(ctx, id, fn)
}

func (d *baseDeps) AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error {
	return d.store.AppendMessage(ctx, chatID, msg)
}

func (d *baseDeps) UpsertTurnPlan(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error {
	return d.store.UpsertTurnPlan(ctx, chatID, msg)
}

func (d *baseDeps) TurnFoldTarget(_ context.Context, chatID vibekit.ChatID) *buffer.Buffer {
	return d.bufStore.GetOrInit(chatID)
}

// The three turn-bracket operations, recorded rather than performed: the host
// owns the turn lifecycle, and what this package is responsible for is calling
// the right one on the right frame.
func (d *baseDeps) WireTurnStart(_ context.Context, chatID vibekit.ChatID) {
	d.brackets = append(d.brackets, turnBracket{chat: chatID, kind: "start"})
}

func (d *baseDeps) WireTurnEnd(_ context.Context, chatID vibekit.ChatID, stop vibekit.StopReason) {
	d.brackets = append(d.brackets, turnBracket{chat: chatID, kind: "end", stop: stop})
}

func (d *baseDeps) ReviseTurnBinding(_ context.Context, chatID vibekit.ChatID) {
	d.brackets = append(d.brackets, turnBracket{chat: chatID, kind: "revise"})
}

// turnBracket is one recorded turn-lifecycle call.
type turnBracket struct {
	chat vibekit.ChatID
	kind string
	stop vibekit.StopReason
}

// turnBuffers stands in for the host's per-turn buffers: one buffer per chat,
// created on first fold. It is NOT what production does — there a fold with no
// open turn opens a wireTurnStart turn, and the buffer belongs to that turn's
// record — but the property this package is responsible for is the same either
// way: a fold gets somewhere to land, and the same somewhere for the rest of the
// turn.
type turnBuffers struct {
	bufs map[vibekit.ChatID]*buffer.Buffer
}

func newTurnBuffers() *turnBuffers {
	return &turnBuffers{bufs: map[vibekit.ChatID]*buffer.Buffer{}}
}

func (tb *turnBuffers) GetOrInit(chatID vibekit.ChatID) *buffer.Buffer {
	if b, ok := tb.bufs[chatID]; ok {
		return b
	}
	b := buffer.New()
	tb.bufs[chatID] = b
	return b
}

func (tb *turnBuffers) Get(chatID vibekit.ChatID) *buffer.Buffer { return tb.bufs[chatID] }

func (tb *turnBuffers) Delete(chatID vibekit.ChatID) { delete(tb.bufs, chatID) }

func (d *baseDeps) RecordFromDiffs(chatID vibekit.ChatID, diffs []vibekit.ToolDiff, turn int, kind string) {
	d.lineTracker.RecordFromDiffs(chatID, diffs, turn, kind)
}
func (d *baseDeps) ParentACPSession(vibekit.ChatID) string { return d.parent }
func (d *baseDeps) IsScheduled(workflowID string) bool {
	return d.scheduledRuns[workflowID]
}

// stepCapBreach is one recorded StepTurnCapExceeded call.
type stepCapBreach struct {
	workflowID string
	nodeID     string
	turns      int
}

func (d *baseDeps) StepTurnCapExceeded(workflowID, nodeID string, turns int) {
	d.stepCapBreaches = append(d.stepCapBreaches, stepCapBreach{workflowID, nodeID, turns})
}

// turnInterrupt is one recorded InterruptTurn call.
type turnInterrupt struct {
	chatID vibekit.ChatID
	reason string
}

func (d *baseDeps) InterruptTurn(chatID vibekit.ChatID, reason string) {
	d.turnInterrupts = append(d.turnInterrupts, turnInterrupt{chatID, reason})
}

// AccumulateSpend and StageConversationTurnSummary stand in for the host's
// per-turn accounting. The double writes the chat's usage directly, which is what
// the host does for a frame that reaches it with no turn open — the only state a
// translate fixture can be in, since the turn record lives on the host.
func (d *baseDeps) AccumulateSpend(ctx context.Context, chatID vibekit.ChatID, credits float64) {
	if credits <= 0 {
		return
	}
	d.mutateUsage(ctx, chatID, func(u *vibekit.Usage) {
		u.Credits += credits
		u.HasRealData = true
	})
}

func (d *baseDeps) StageConversationTurnSummary(ctx context.Context, chatID vibekit.ChatID, elapsedMs float64) {
	d.mutateUsage(ctx, chatID, func(u *vibekit.Usage) {
		u.TurnCount++
		if elapsedMs > 0 {
			u.LastTurnMs = elapsedMs
		}
	})
}

func (d *baseDeps) mutateUsage(ctx context.Context, chatID vibekit.ChatID, apply func(*vibekit.Usage)) {
	_ = d.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		apply(&c.Usage)
		return true
	})
}

func (d *baseDeps) WorkDir() string { return "/tmp" }
func (d *baseDeps) BridgeNotify(context.Context, vibekit.ChatID, string, map[string]any) error {
	return nil
}

// askAnswer is one recorded BridgeRespond call.
type askAnswer struct {
	chatID    vibekit.ChatID
	requestID int64
	result    any
	rpcErr    error
}

func (d *baseDeps) BridgeRespond(
	_ context.Context, chatID vibekit.ChatID, requestID int64, result any, rpcErr error,
) error {
	d.asked = append(d.asked, askAnswer{chatID, requestID, result, rpcErr})
	return d.respondErr
}
func (d *baseDeps) MCPRecorder() MCPRecorder { return nopMCPRecorder{} }
func (d *baseDeps) SetGovernance(g vibekit.GovernanceStatePayload) {
	if d.onSetGovernance != nil {
		d.onSetGovernance(g)
	}
}
func (d *baseDeps) PendingPermsAdd(int64, vibekit.ServerEvent)                           {}
func (d *baseDeps) PendingPermsRemove(int64)                                             {}
func (d *baseDeps) NotifyPush(context.Context, string, vibekit.PushKind, vibekit.ChatID) {}
func (d *baseDeps) IsHookStatusEnabled() bool                                            { return false }

var toolCallPayload = json.RawMessage(`{"toolCallId":"tc-1","title":"ReadFile","kind":"read","status":"pending","rawInput":{},"locations":[],"content":[{"type":"text","content":{"text":"reading file"}}]}`)

func BenchmarkTranslator_HandleToolCall(b *testing.B) {
	tr := New(rolesOf(newBaseDeps()), withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()
	chatID := vibekit.ChatID("bench-chat")

	for b.Loop() {
		tr.HandleToolCall(ctx, chatID, toolCallPayload, FrameAttribution{})
	}
}

// BenchmarkTranslator_HandleAssistantChunk measures per-token allocation
// overhead on the steady-state path (buffer already started).
func BenchmarkTranslator_HandleAssistantChunk(b *testing.B) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()
	chatID := vibekit.ChatID("bench-chunk")

	chunkPayload := json.RawMessage(`{"content":{"type":"text","text":"Hello world, this is a streaming token. "}}`)

	// Prime the buffer with a first chunk so subsequent iterations hit the
	// steady-state path (no message creation overhead).
	tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)

	b.ReportAllocs()
	for b.Loop() {
		tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)
	}
}

// BenchmarkTranslator_FullTurn simulates a complete streaming turn:
// 50 text chunks → 1 tool call → 1 tool call update → 50 more chunks.
// Measures end-to-end throughput including buffer management.
func BenchmarkTranslator_FullTurn(b *testing.B) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()

	chunkPayload := json.RawMessage(`{"content":{"type":"text","text":"Hello world, this is a streaming token. "}}`)
	toolCallPL := toolCallPayload
	toolUpdatePL := json.RawMessage(`{"toolCallId":"tc-1","status":"completed","content":[{"type":"text","content":{"text":"done"}}]}`)

	for b.Loop() {
		chatID := vibekit.ChatID("bench-turn")
		// Phase 1: initial streaming chunks
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)
		}
		// Phase 2: tool call
		tr.HandleToolCall(ctx, chatID, toolCallPL, FrameAttribution{})
		tr.HandleToolCallUpdate(ctx, chatID, toolUpdatePL, FrameAttribution{})
		// Phase 3: more streaming
		for range 50 {
			tr.HandleAssistantChunk(ctx, chatID, chunkPayload, false)
		}
		// Cleanup: reset buffer for next iteration
		deps.bufStore.Delete(chatID)
	}
}

func BenchmarkTranslator_HandleUsageUpdate(b *testing.B) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps), withIDGenerator(func() string { return "stub-msg-id" }))
	ctx := b.Context()
	chatID := vibekit.ChatID("bench-usage")
	raw := json.RawMessage(`{"size":100000,"used":42500}`)

	// Pre-create a chat so Mutate finds it.
	_ = deps.store.Mutate(ctx, chatID, func(_ *vibekit.Chat, _ bool) bool { return true })

	b.ReportAllocs()
	for b.Loop() {
		tr.HandleUsageUpdate(ctx, chatID, raw)
	}
}
