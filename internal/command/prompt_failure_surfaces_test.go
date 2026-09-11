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
func (*surfaceBridge) BeginPromptCall(context.CancelCauseFunc) uint64   { return 1 }
func (*surfaceBridge) EndPromptCall()                                   {}
func (*surfaceBridge) ArmCancelGrace(uint64, time.Duration) bool        { return true }
func (*surfaceBridge) PromptGeneration() uint64                         { return 1 }

// A turn WAS finalized, so the reason is on its card and the toast may stand down.
func TestReportPromptFailure_MarksTheFrameTurnScoped(t *testing.T) {
	deps := newSurfaceDeps()
	reportPromptFailure(t.Context(), promptRolesOf(deps), "c1", 7,
		errors.New("connection reset"), time.Second, false)

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

// The reader's own Stop reaches that same branch, and it must withhold the frame like
// every other cancelled exit: the retry takes the prompt slot and registers its own
// cancel precisely so the grace can unblock a retry KAS never answers, so the expiry
// trips it here. `recovery_failed` routes to a toast and TurnScoped suppresses it only
// for a reader already on that chat, so any other chat got a red toast for a stop the
// reader asked for — reading "Retry prompt failed: " and nothing after it, because a
// cancel supplies no prose.
func TestRetryEmptyTurnPrompt_WithholdsTheFrameOnAnUnackedCancel(t *testing.T) {
	deps := newSurfaceDeps()
	deps.callErr = context.Canceled
	roles := promptRolesOf(deps)

	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cancel(ErrCancelGraceExpired)

	retryEmptyTurnPrompt(ctx, roles.bridges, roles.chats, roles.bus,
		roles.turnOutcome, "c1", &vibekit.PromptCommand{Text: "hi", MessageID: "m1"},
		map[string]any{})

	deps.mu.Lock()
	frames := deps.errors
	deps.mu.Unlock()

	if len(frames) != 0 {
		t.Errorf("broadcast %d error frames, want none: %+v", len(frames), frames)
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

// EVERY prompt exit that finalizes no turn still owes that turn a carrier.
// `appendUserMessage` runs before admission deliberately, so the user row is already
// on disk by the time any of these exits is reached — and an exit that appends
// nothing leaves a turn with a trigger and no body at all. The transcript projection
// reads an absent carrier as "nothing closed this turn" and renders it as an end
// vibekit could not read, seconds after the prompt was refused. The carrier grades
// the turn from the stop that ended it and carries the real reason instead: an
// `interrupted` divider for every exit that broke, and a bare `cancelled` marker for
// the one that did not.
func TestPromptExits_AppendATurnStopCarrier(t *testing.T) {
	cases := []struct {
		name string
		// arrange puts the double on the path that produces this exit.
		arrange func(*surfaceDeps)
		// run drives the production path.
		run func(context.Context, *promptRoles, *vibekit.PromptCommand)
		// want is a substring of the row's content: the cause a reader can act on. Empty
		// means the row carries NO prose at all, which is what a cancel says.
		want string
		// kind is the marker the row must carry, DERIVED server-side from the stop.
		kind vibekit.EventKind
		// outcome is the verdict stamped on the row, so both projections read it rather
		// than inferring one from the event kind.
		outcome vibekit.TurnOutcome
		// frame is whether this exit also broadcasts an error, in which case the two
		// surfaces must read one sentence. Asserted in BOTH directions: false demands
		// zero error payloads.
		frame bool
	}{
		{
			name:    "the bridge could not be opened",
			arrange: func(d *surfaceDeps) { d.spawnErr = errors.New("no such binary") },
			run: func(ctx context.Context, roles *promptRoles, p *vibekit.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", p)
			},
			want:    "no such binary",
			kind:    vibekit.EventInterrupted,
			outcome: vibekit.TurnOutcomeInterrupted,
			frame:   true,
		},
		{
			name:    "the bridge slot was held despite the reservation",
			arrange: func(d *surfaceDeps) { d.slotHeld = true },
			run: func(ctx context.Context, roles *promptRoles, p *vibekit.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", p)
			},
			want:    "The prompt could not start",
			kind:    vibekit.EventInterrupted,
			outcome: vibekit.TurnOutcomeInterrupted,
			frame:   true,
		},
		{
			name:    "the turn was cancelled before an epoch was minted",
			arrange: func(d *surfaceDeps) { d.deadEpoch = true },
			run: func(ctx context.Context, roles *promptRoles, p *vibekit.PromptCommand) {
				runPromptTurn(ctx, func() {}, roles, "c1", p)
			},
			want:    "cancelled before the agent answered",
			kind:    vibekit.EventInterrupted,
			outcome: vibekit.TurnOutcomeInterrupted,
			frame:   true,
		},
		{
			// The one exit with no frame at all: it logged a Warn and returned, so the
			// row is the whole of what a reader ever learns about it.
			name:    "the empty-turn retry's own epoch never opened",
			arrange: func(d *surfaceDeps) { d.deadEpoch = true },
			run: func(ctx context.Context, roles *promptRoles, p *vibekit.PromptCommand) {
				retryEmptyTurnPrompt(ctx, roles.bridges, roles.chats, roles.bus,
					roles.turnOutcome, "c1", p, map[string]any{})
			},
			want:    "cancelled before the agent answered",
			kind:    vibekit.EventInterrupted,
			outcome: vibekit.TurnOutcomeInterrupted,
		},
		{
			// The reader pressed Stop and KAS never acked it, so the grace budget killed
			// the prompt context during the spawn/prime/MCP window. Nothing is broken, so
			// the row is a SKIP marker with no prose and no toast: an EventInterrupted
			// here would outrank the cancel in deriveTurnOutcome and paint the turn red.
			name:    "an unacked cancel killed the context before an epoch was minted",
			arrange: func(d *surfaceDeps) { d.deadEpoch = true },
			run: func(ctx context.Context, roles *promptRoles, p *vibekit.PromptCommand) {
				ctx, cancel := context.WithCancelCause(ctx)
				defer cancel(nil)
				cancel(ErrCancelGraceExpired)
				runPromptTurn(ctx, func() {}, roles, "c1", p)
			},
			kind:    vibekit.EventCancelled,
			outcome: vibekit.TurnOutcomeCancelled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newSurfaceDeps()
			tc.arrange(deps)
			tc.run(t.Context(), promptRolesOf(deps), &vibekit.PromptCommand{Text: "hi", MessageID: "m1"})

			deps.mu.Lock()
			appended := deps.appended
			frames := len(deps.errors)
			deps.mu.Unlock()

			if len(appended) != 1 {
				t.Fatalf("appended %d messages, want exactly 1: %+v", len(appended), appended)
			}
			got := appended[0]
			if got.Role != vibekit.RoleEvent {
				t.Errorf("role = %q, want %q: a divider, not a bubble", got.Role, vibekit.RoleEvent)
			}
			if got.EventKind != tc.kind {
				t.Errorf("event_kind = %q, want %q: the kind is what grades the turn and "+
					"what turnFailureText reads its prose from", got.EventKind, tc.kind)
			}
			if got.TurnOutcome != tc.outcome {
				t.Errorf("turn_outcome = %q, want %q: an unstamped row leaves both "+
					"projections inferring one, and closesTurn then reads the segment as open",
					got.TurnOutcome, tc.outcome)
			}
			if tc.want == "" {
				if got.Content != "" {
					t.Errorf("content = %q, want empty: a cancel has no account to give", got.Content)
				}
				if got.TurnFailureReason != "" {
					t.Errorf("turn_failure_reason = %q, want empty", got.TurnFailureReason)
				}
			} else if !strings.Contains(got.Content, tc.want) {
				t.Errorf("content = %q, want it to contain %q", got.Content, tc.want)
			}
			if got.ID == "" {
				t.Error("id is empty: the store dedupes and orders by id")
			}
			if tc.frame {
				if frame := deps.onlyError(t); frame.Message != got.Content {
					t.Errorf("frame message %q != row content %q: one failure, one rendering",
						frame.Message, got.Content)
				}
			} else if frames != 0 {
				t.Errorf("broadcast %d error frames, want none: this exit's surface is the "+
					"transcript row alone", frames)
			}
		})
	}
}
