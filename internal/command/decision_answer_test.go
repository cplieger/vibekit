package command

// The three decision handlers share one rule: claim the request, and answer
// kiro-cli only if the claim succeeded. What is pinned here is the losing side,
// because it is the side that used to be silent — the answer went out, kiro-cli
// dropped it for a request id it had already resolved, and the client was told
// its choice had landed.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// countingBridge counts the answers that reach the wire. Respond is the only
// method these handlers use; the rest satisfies the interface.
type countingBridge struct {
	recordingBridge
	responds []int64
}

func (b *countingBridge) Respond(_ context.Context, id int64, _ any, _ error) error {
	b.responds = append(b.responds, id)
	return nil
}

// takeDeps is a host double whose claim outcome the test scripts.
type takeDeps struct {
	*benchDeps
	bridge Bridge
	takes  []int64
	takeOK bool
}

func (d *takeDeps) GetBridge(vibekit.ChatID) Bridge { return d.bridge }

func (d *takeDeps) TakePendingPerm(requestID int64, _ vibekit.SettledBy) bool {
	d.takes = append(d.takes, requestID)
	return d.takeOK
}

func decisionCommand(t *testing.T, typ vibekit.CommandType, payload any) *vibekit.ClientCommand {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &vibekit.ClientCommand{Type: typ, ChatID: "c1", Payload: raw}
}

const decisionRequestID = int64(7)

// decisionCases is the three handlers, each with a payload that answers
// request 7 in the least eventful way its wire allows.
func decisionCases(t *testing.T) []struct {
	name string
	run  func(*Dispatcher, hostDouble, http.ResponseWriter)
} {
	t.Helper()
	perm := decisionCommand(t, vibekit.CmdPermissionResponse,
		vibekit.PermissionResponseCommand{RequestID: decisionRequestID, OptionID: "allow_once"})
	elicit := decisionCommand(t, vibekit.CmdElicitationResponse,
		vibekit.ElicitationResponseCommand{RequestID: decisionRequestID, Action: vibekit.ElicitationActionDecline})
	input := decisionCommand(t, vibekit.CmdUserInputResponse,
		vibekit.UserInputResponseCommand{RequestID: decisionRequestID, Action: vibekit.UserInputActionDismissed})
	return []struct {
		name string
		run  func(*Dispatcher, hostDouble, http.ResponseWriter)
	}{
		{name: "permission", run: func(d *Dispatcher, host hostDouble, w http.ResponseWriter) {
			CmdPermission(d, host, host, t.Context(), w, perm)
		}},
		{name: "elicitation", run: func(d *Dispatcher, host hostDouble, w http.ResponseWriter) {
			CmdElicitationResponse(d, host, host, t.Context(), w, elicit)
		}},
		{name: "user_input", run: func(d *Dispatcher, host hostDouble, w http.ResponseWriter) {
			CmdUserInputResponse(d, host, host, t.Context(), w, input)
		}},
	}
}

// TestDecisionHandlers_LostClaimIsRefusedAndNotAnswered: another surface
// already answered, so the handler must send nothing to kiro-cli and say so
// with a 409 the client can act on.
func TestDecisionHandlers_LostClaimIsRefusedAndNotAnswered(t *testing.T) {
	for _, tc := range decisionCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &countingBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: false}
			w := httptest.NewRecorder()

			tc.run(New(), deps, w)

			if w.Code != http.StatusConflict {
				t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
			}
			if !strings.Contains(w.Body.String(), "already_answered") {
				t.Errorf("body = %q, want the already_answered code", w.Body.String())
			}
			if len(bridge.responds) != 0 {
				t.Errorf("answered kiro-cli %d times after losing the claim, want 0", len(bridge.responds))
			}
			if len(deps.takes) != 1 || deps.takes[0] != decisionRequestID {
				t.Errorf("claims = %v, want exactly [%d]", deps.takes, decisionRequestID)
			}
		})
	}
}

// TestDecisionHandlers_WonClaimAnswersOnce is the other half: the winner does
// reach the wire, on the request id it claimed.
func TestDecisionHandlers_WonClaimAnswersOnce(t *testing.T) {
	for _, tc := range decisionCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &countingBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: true}
			w := httptest.NewRecorder()

			tc.run(New(), deps, w)

			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want %d (body %q)", w.Code, http.StatusOK, w.Body.String())
			}
			if len(bridge.responds) != 1 || bridge.responds[0] != decisionRequestID {
				t.Errorf("answers = %v, want exactly [%d]", bridge.responds, decisionRequestID)
			}
		})
	}
}
