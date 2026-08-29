package command

// The empty-turn recovery gate: three clauses, each of which has to hold before a
// prompt is re-executed and paid for twice.

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// recoveryOutcome is a TurnOutcomeAccess whose awaited result and later-turn
// answer the test dictates, so each clause of the gate can be moved on its own.
type recoveryOutcome struct {
	result      vibekit.TurnResult
	laterTurn   bool
	awaitErr    error
	openedTurns []vibekit.TurnOpenSource
}

func (o *recoveryOutcome) OpenTurn(_ context.Context, _ vibekit.ChatID, source vibekit.TurnOpenSource) vibekit.TurnEpoch {
	o.openedTurns = append(o.openedTurns, source)
	return vibekit.TurnEpoch(len(o.openedTurns))
}

func (o *recoveryOutcome) AwaitTurn(context.Context, vibekit.ChatID, vibekit.TurnEpoch) (vibekit.TurnResult, error) {
	return o.result, o.awaitErr
}

func (o *recoveryOutcome) ReleaseTurn(vibekit.ChatID, vibekit.TurnEpoch) {}

func (o *recoveryOutcome) SettleTurnOnResponse(context.Context, vibekit.ChatID, vibekit.TurnEpoch, uint64, *vibekit.RPCResponse) {
}

func (o *recoveryOutcome) TurnOpenedAfter(vibekit.ChatID, vibekit.TurnEpoch) bool { return o.laterTurn }

func (o *recoveryOutcome) PrimeTurnOpen(vibekit.ChatID) bool { return false }

func (o *recoveryOutcome) FinalizeLocalShellTurn(context.Context, vibekit.ChatID, vibekit.TurnEpoch) {
}

func (o *recoveryOutcome) AbandonInFlightTurn(context.Context, vibekit.ChatID, vibekit.TurnEpoch, string) {
}

// recoveryBridges records whether the recovery tore the session down, which is the
// first irreversible thing it does and therefore the cleanest observable for
// "did the gate fire".
type recoveryBridges struct {
	*benchDeps
	closed int
}

func (b *recoveryBridges) CloseBridge(vibekit.ChatID) { b.closed++ }

func (b *recoveryBridges) OpenBridge(context.Context, vibekit.ChatID, string) (Bridge, error) {
	return &recoveryBridge{}, nil
}

// recoveryBridge is a Bridge that grants the prompt slot and answers every call,
// so the firing row runs the retry to the end rather than abandoning it at the
// slot.
type recoveryBridge struct{}

func (*recoveryBridge) Call(context.Context, string, any) (*vibekit.RPCResponse, error) {
	return &vibekit.RPCResponse{}, nil
}

func (*recoveryBridge) CallAt(context.Context, string, any) (*vibekit.RPCResponse, uint64, error) {
	return &vibekit.RPCResponse{}, 0, nil
}

func (*recoveryBridge) Notify(context.Context, string, any) error        { return nil }
func (*recoveryBridge) Respond(context.Context, int64, any, error) error { return nil }
func (*recoveryBridge) SessionID() vibekit.SessionID                     { return "s1" }
func (*recoveryBridge) TryAcquireForPrompt() bool                        { return true }
func (*recoveryBridge) ReleaseAfterPrompt()                              {}
func (*recoveryBridge) BeginPromptCall(context.CancelFunc) uint64        { return 1 }
func (*recoveryBridge) EndPromptCall()                                   {}
func (*recoveryBridge) ArmCancelGrace(uint64, time.Duration) bool        { return true }
func (*recoveryBridge) PromptGeneration() uint64                         { return 1 }

// The three clauses, each one moved on its own from a firing baseline. Every row
// but the first must NOT re-prompt.
func TestRecoverEmptyTurn_GateRequiresAllThreeClauses(t *testing.T) {
	firing := vibekit.TurnResult{
		Stop:           vibekit.StopReasonEndTurn,
		EmittedNothing: true,
		WireEnded:      true,
	}
	cases := []struct {
		name      string
		result    vibekit.TurnResult
		laterTurn bool
		wantFire  bool
	}{
		{
			name:     "all three hold, so the turn really did end empty on the wire",
			result:   firing,
			wantFire: true,
		},
		{
			// A locally-closed turn's outcome is the prompt response's, which can be
			// nothing richer than end_turn or cancelled and carries nothing on a fault.
			// `end_turn` there says only that vibekit had nothing better to call it.
			name:     "the turn was closed LOCALLY, so its end_turn is an inference",
			result:   vibekit.TurnResult{Stop: vibekit.StopReasonEndTurn, EmittedNothing: true},
			wantFire: false,
		},
		{
			name:     "the turn emitted content, so there is nothing to recover",
			result:   vibekit.TurnResult{Stop: vibekit.StopReasonEndTurn, WireEnded: true},
			wantFire: false,
		},
		{
			name:     "the wire named a different outcome",
			result:   vibekit.TurnResult{Stop: vibekit.StopReasonRefusal, EmittedNothing: true, WireEnded: true},
			wantFire: false,
		},
		{
			// The STRUCTURAL clause, and the only one that depends on no frame arriving.
			// A zero-content or tool-only auto-wake never sends agentInitiated, so a
			// mis-binding is never revised and the mis-bound pre-open closes with the
			// first three clauses satisfied. What it necessarily violates is this one:
			// the real agent-initiated turn is a later epoch on the same chat.
			name:      "a later turn opened, so the bracket this turn closed on was not ours",
			result:    firing,
			laterTurn: true,
			wantFire:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := &recoveryOutcome{result: tc.result, laterTurn: tc.laterTurn}
			bridges := &recoveryBridges{benchDeps: newBenchDeps()}
			p := &vibekit.PromptCommand{Text: "an ordinary question", MessageID: "m1"}

			recoverEmptyTurn(t.Context(), bridges, bridges, bridges, outcome, "c1", 1, p, map[string]any{})

			fired := bridges.closed > 0
			if fired != tc.wantFire {
				t.Errorf("session torn down = %v, want %v: the recovery re-executes the prompt and pays for it twice",
					fired, tc.wantFire)
			}
			retried := len(outcome.openedTurns) > 0
			if retried != tc.wantFire {
				t.Errorf("a retry turn was opened = %v, want %v", retried, tc.wantFire)
			}
			if tc.wantFire && !hasSource(outcome.openedTurns, vibekit.TurnSourceEmptyRetry) {
				t.Errorf("the retry opened %v, want an emptyRetry turn: its reply must not extend "+
					"the message of the turn it replaced", outcome.openedTurns)
			}
		})
	}
}

func hasSource(got []vibekit.TurnOpenSource, want vibekit.TurnOpenSource) bool {
	return slices.Contains(got, want)
}
