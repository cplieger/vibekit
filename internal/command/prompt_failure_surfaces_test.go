package command

// `ErrorPayload.TurnScoped` lets the client drop the toast for a chat already on
// screen, on the grounds that the turn's own card holds the reason. It is a property
// of the EMISSION rather than of the code: `prompt_failed` has three emitters and
// `recovery_failed` two, and three of the five open no turn at all, so for those the
// toast is the only surface a per-code answer would silence.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// surfaceDeps records the two durable surfaces a failure can reach — the error
// frames it broadcasts and the messages it appends. It embeds benchDeps and
// overrides only what it records, so an emitter reaching a surface this double does
// not model shows up as a missing observation rather than a compile error.
type surfaceDeps struct {
	*benchDeps
	mu       sync.Mutex
	errors   []vibekit.ErrorPayload
	appended []vibekit.Message
	// spawnErr, when set, is what OpenBridge answers: the respawn-failure path.
	spawnErr error
	// slotHeld makes TryAcquireForPrompt refuse, which is the held-bridge-slot path.
	slotHeld bool
	// deadEpoch makes StartTurn answer 0, the cancelled-during-spawn path.
	deadEpoch bool
	// callErr, when set, fails the prompt Call itself.
	callErr error
}

func newSurfaceDeps() *surfaceDeps {
	return &surfaceDeps{benchDeps: newBenchDeps()}
}

func (d *surfaceDeps) Broadcast(_ context.Context, e vibekit.ServerEvent) {
	p, ok := e.Payload.(vibekit.ErrorPayload)
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.errors = append(d.errors, p)
}

func (d *surfaceDeps) AppendMessage(_ context.Context, _ vibekit.ChatID, m *vibekit.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.appended = append(d.appended, *m)
	return nil
}

func (d *surfaceDeps) OpenBridge(context.Context, vibekit.ChatID, string) (Bridge, error) {
	if d.spawnErr != nil {
		return nil, d.spawnErr
	}
	return &surfaceBridge{deps: d}, nil
}

func (d *surfaceDeps) StartTurn(context.Context, vibekit.ChatID, vibekit.TurnOpenSource) vibekit.TurnEpoch {
	if d.deadEpoch {
		return 0
	}
	return 7
}

func (d *surfaceDeps) ReserveTurnForPrompt(context.Context, vibekit.ChatID, time.Duration) AdmissionOutcome {
	return AdmissionAcquired
}

func (d *surfaceDeps) TryReserveTurn(vibekit.ChatID, vibekit.TurnOpenSource) bool { return true }

func (d *surfaceDeps) TurnOpenedAfter(vibekit.ChatID, vibekit.TurnEpoch) bool { return false }

func (d *surfaceDeps) AwaitTurn(context.Context, vibekit.ChatID, vibekit.TurnEpoch) (vibekit.TurnResult, error) {
	return vibekit.TurnResult{}, vibekit.ErrNoSuchTurn
}

// onlyError fails when the run produced other than one error frame: a second frame
// would mean two surfaces claiming one failure, which is what this file is about.
func (d *surfaceDeps) onlyError(t *testing.T) vibekit.ErrorPayload {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.errors) != 1 {
		t.Fatalf("broadcast %d error frames, want exactly 1: %+v", len(d.errors), d.errors)
	}
	return d.errors[0]
}

// surfaceBridge answers a prompt the way the deps dictate.
type surfaceBridge struct{ deps *surfaceDeps }

func (b *surfaceBridge) Call(context.Context, string, any) (*vibekit.RPCResponse, error) {
	return &vibekit.RPCResponse{}, b.deps.callErr
}

func (b *surfaceBridge) CallAt(context.Context, string, any) (*vibekit.RPCResponse, uint64, error) {
	return &vibekit.RPCResponse{}, 0, b.deps.callErr
}

func (*surfaceBridge) Notify(context.Context, string, any) error        { return nil }
func (*surfaceBridge) Respond(context.Context, int64, any, error) error { return nil }
func (*surfaceBridge) SessionID() vibekit.SessionID                     { return "s1" }
func (b *surfaceBridge) TryAcquireForPrompt() bool                      { return !b.deps.slotHeld }
func (*surfaceBridge) ReleaseAfterPrompt()                              {}
func (*surfaceBridge) BeginPromptCall(context.CancelFunc) uint64        { return 1 }
func (*surfaceBridge) EndPromptCall()                                   {}
func (*surfaceBridge) ArmCancelGrace(uint64, time.Duration) bool        { return true }
func (*surfaceBridge) PromptGeneration() uint64                         { return 1 }

// A turn WAS finalized, so the reason is on its card and the toast may stand down.
func TestReportPromptFailure_MarksTheFrameTurnScoped(t *testing.T) {
	deps := newSurfaceDeps()
	reportPromptFailure(t.Context(), promptRolesOf(deps), "c1", 7,
		errors.New("connection reset"), time.Second)

	got := deps.onlyError(t)
	if got.Code != vibekit.ErrCodePromptFailed {
		t.Errorf("code = %q, want %q", got.Code, vibekit.ErrCodePromptFailed)
	}
	if !got.TurnScoped {
		t.Error("TurnScoped = false, want true: AbandonInFlightTurn stamps this same " +
			"reason on the turn's carrier, so the card says it durably and a toast for " +
			"the chat on screen is a second copy of it")
	}
}

// The three no-turn emitters must leave TurnScoped FALSE: with no turn card, no
// footer mark and no composer report, the toast is the only surface they have.
func TestPromptFailure_NoTurnEmittersAreNotTurnScoped(t *testing.T) {
	cases := []struct {
		name string
		want vibekit.ErrorCode
		// arrange puts the double on the path that produces the emitter's failure.
		arrange func(*surfaceDeps)
		// run drives the production path.
		run func(context.Context, *promptRoles, *vibekit.PromptCommand)
	}{
		{
			// A held slot despite an owned reservation is a programming error rather
			// than a fault, but the prompt is persisted and the POST acked, so it
			// still has to report.
			name:    "the bridge slot was held despite the reservation",
			want:    vibekit.ErrCodePromptFailed,
			arrange: func(d *surfaceDeps) { d.slotHeld = true },
			run: func(ctx context.Context, roles *promptRoles, p *vibekit.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", p)
			},
		},
		{
			// The most reachable of the three: a cancel in the spawn / prime / MCP
			// window is ordinary, and a zero epoch means no ACP call and no finalize.
			name:    "the turn was cancelled before an epoch was minted",
			want:    vibekit.ErrCodePromptFailed,
			arrange: func(d *surfaceDeps) { d.deadEpoch = true },
			run: func(ctx context.Context, roles *promptRoles, p *vibekit.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", p)
			},
		},
		{
			// The turn being replaced was already finalized and the retry's epoch is
			// never opened, so this failure finalizes nothing either.
			name:    "empty-turn recovery could not respawn the session",
			want:    vibekit.ErrCodeRecoveryFailed,
			arrange: func(d *surfaceDeps) { d.spawnErr = errors.New("no such binary") },
			run: func(ctx context.Context, roles *promptRoles, p *vibekit.PromptCommand) {
				retryEmptyTurnPrompt(ctx, roles.bridges, roles.chats, roles.bus,
					roles.turnOutcome, "c1", p, map[string]any{})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newSurfaceDeps()
			tc.arrange(deps)
			tc.run(t.Context(), promptRolesOf(deps), &vibekit.PromptCommand{Text: "hi", MessageID: "m1"})

			got := deps.onlyError(t)
			if got.Code != tc.want {
				t.Errorf("code = %q, want %q", got.Code, tc.want)
			}
			if got.TurnScoped {
				t.Error("TurnScoped = true, want false: this emitter finalizes no turn, so " +
					"there is no inline row for the toast to duplicate and suppressing it " +
					"reports the failure nowhere at all")
			}
			if got.Message == "" {
				t.Error("Message is empty: the toast is this failure's only surface")
			}
		})
	}
}

// The retry ran as a turn of its own and AbandonInFlightTurn stamped the reason on
// it, so this failure IS turn-scoped.
func TestRetryEmptyTurnPrompt_MarksTheRetryFailureTurnScoped(t *testing.T) {
	deps := newSurfaceDeps()
	deps.callErr = errors.New("connection reset")
	roles := promptRolesOf(deps)

	retryEmptyTurnPrompt(t.Context(), roles.bridges, roles.chats, roles.bus,
		roles.turnOutcome, "c1", &vibekit.PromptCommand{Text: "hi", MessageID: "m1"},
		map[string]any{})

	got := deps.onlyError(t)
	if got.Code != vibekit.ErrCodeRecoveryFailed {
		t.Errorf("code = %q, want %q", got.Code, vibekit.ErrCodeRecoveryFailed)
	}
	if !got.TurnScoped {
		t.Error("TurnScoped = false, want true: the retry was a turn of its own and the " +
			"abandon stamped this reason on it")
	}
}

// A failed respawn CORRECTS the transcript, which a toast cannot do:
// refreshRetrySession has already written "Session refreshed, retrying" onto this
// turn, so without the correction the durable record claims a retry that will never
// happen.
func TestRetryEmptyTurnPrompt_RespawnFailureCorrectsTheRetryingDivider(t *testing.T) {
	deps := newSurfaceDeps()
	deps.spawnErr = errors.New("no such binary")
	roles := promptRolesOf(deps)

	retryEmptyTurnPrompt(t.Context(), roles.bridges, roles.chats, roles.bus,
		roles.turnOutcome, "c1", &vibekit.PromptCommand{Text: "hi", MessageID: "m1"},
		map[string]any{})

	deps.mu.Lock()
	appended := deps.appended
	deps.mu.Unlock()

	if len(appended) != 1 {
		t.Fatalf("appended %d messages, want exactly 1: %+v", len(appended), appended)
	}
	got := appended[0]
	if got.Role != vibekit.RoleEvent {
		t.Errorf("role = %q, want %q: a divider, not a bubble", got.Role, vibekit.RoleEvent)
	}
	if got.EventKind != vibekit.EventInterrupted {
		t.Errorf("event_kind = %q, want %q: `interrupted` is what renders as a boundary "+
			"divider and grades the turn broken", got.EventKind, vibekit.EventInterrupted)
	}
	if !strings.Contains(got.Content, "Session refresh failed") {
		t.Errorf("content = %q, want it to name the failed refresh: this row is the newest "+
			"event on the turn, so it is what the turn's own notice reads", got.Content)
	}
	if !strings.Contains(got.Content, "no such binary") {
		t.Errorf("content = %q, want the cause in it", got.Content)
	}
	if got.ID == "" {
		t.Error("id is empty: the store dedupes and orders by id")
	}
	// The frame and the divider carry the SAME prose, so the toast and the scrollback
	// read one sentence rather than two wordings.
	if frame := deps.onlyError(t); frame.Message != got.Content {
		t.Errorf("frame message %q != divider content %q: one failure, one rendering",
			frame.Message, got.Content)
	}
}
