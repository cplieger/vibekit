package command

// The three decision handlers share one rule: claim the request, and answer
// kiro-cli only if the claim succeeded. What is pinned here is the losing side,
// because it is the side that used to be silent — the answer went out, kiro-cli
// dropped it for a request id it had already resolved, and the client was told
// its choice had landed.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
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
	// takeChats is the chat each claim named, so a handler that drops it is
	// visible: an id-only claim can retire another chat's card.
	takeChats []vibekit.ChatID
	takeOK    bool
}

func (d *takeDeps) Bridge(vibekit.ChatID) Bridge { return d.bridge }

func (d *takeDeps) TakePendingPerm(chatID vibekit.ChatID, requestID int64, _ vibekit.SettledBy) bool {
	d.takes = append(d.takes, requestID)
	d.takeChats = append(d.takeChats, chatID)
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
	run  func(hostDouble) (any, error)
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
		run  func(hostDouble) (any, error)
	}{
		{name: "permission", run: func(host hostDouble) (any, error) {
			return CmdPermission(t.Context(), host, host, perm)
		}},
		{name: "elicitation", run: func(host hostDouble) (any, error) {
			return CmdElicitationResponse(t.Context(), host, host, elicit)
		}},
		{name: "user_input", run: func(host hostDouble) (any, error) {
			return CmdUserInputResponse(t.Context(), host, host, input)
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

			_, err := tc.run(deps)

			if got := statusOf(err); got != http.StatusConflict {
				t.Errorf("status = %d, want %d", got, http.StatusConflict)
			}
			if err == nil || !strings.Contains(err.Error(), "already_answered") {
				t.Errorf("error = %v, want the already_answered code", err)
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

			_, err := tc.run(deps)

			if got := statusOf(err); got != http.StatusOK {
				t.Errorf("status = %d, want %d (err %v)", got, http.StatusOK, err)
			}
			if len(bridge.responds) != 1 || bridge.responds[0] != decisionRequestID {
				t.Errorf("answers = %v, want exactly [%d]", bridge.responds, decisionRequestID)
			}
			// The claim names the command's OWN chat. A request id is unique only
			// within one bridge, and every bridge mints from zero, so a claim that
			// dropped the chat would retire whichever chat's card happened to be
			// stored under that id — resolving one chat's dialog from another's
			// answer and leaving the real request with no answer path at all.
			if !slices.Equal(deps.takeChats, []vibekit.ChatID{"c1"}) {
				t.Errorf("claimed chats = %v, want [c1]", deps.takeChats)
			}
		})
	}
}

// resultBridge captures the answer VALUE, so a test can assert what a choice
// carried rather than only that an answer went out.
type resultBridge struct {
	recordingBridge
	results []any
}

func (b *resultBridge) Respond(_ context.Context, _ int64, result any, _ error) error {
	b.results = append(b.results, result)
	return nil
}

// Values travel only with the action that supplied them. A declined or
// cancelled form has no filled fields, so forwarding the ones the client sent
// anyway would hand kiro-cli values the user withdrew.
func TestCmdElicitationResponse_ContentTravelsOnlyOnAccept(t *testing.T) {
	const filled = `{"branch":"main"}`
	cases := []struct {
		name        string
		action      string
		wantContent string
	}{
		{name: "accept forwards the filled form", action: vibekit.ElicitationActionAccept, wantContent: filled},
		{name: "decline forwards no content", action: vibekit.ElicitationActionDecline, wantContent: ""},
		{name: "cancel forwards no content", action: vibekit.ElicitationActionCancel, wantContent: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &resultBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: true}
			cmd := decisionCommand(t, vibekit.CmdElicitationResponse, vibekit.ElicitationResponseCommand{
				RequestID: decisionRequestID,
				Action:    tc.action,
				Content:   json.RawMessage(filled),
			})

			if _, err := CmdElicitationResponse(t.Context(), deps, deps, cmd); err != nil {
				t.Fatalf("CmdElicitationResponse(%s) = %v", tc.action, err)
			}

			if len(bridge.results) != 1 {
				t.Fatalf("got %d answers, want 1", len(bridge.results))
			}
			result, ok := bridge.results[0].(vibekit.ElicitationResult)
			if !ok {
				t.Fatalf("answer = %T, want vibekit.ElicitationResult", bridge.results[0])
			}
			if result.Action != tc.action {
				t.Errorf("action = %q, want %q", result.Action, tc.action)
			}
			if string(result.Content) != tc.wantContent {
				t.Errorf("content for %q = %q, want %q", tc.action, result.Content, tc.wantContent)
			}
		})
	}
}

// The same rule on the user-input wire: a dismissed question carries no answer,
// because a dismissal is the user declining to give one.
func TestCmdUserInputResponse_AnswerTravelsOnlyWhenAnswered(t *testing.T) {
	const typed = "use the second option"
	cases := []struct {
		name       string
		action     string
		wantAnswer string
	}{
		{name: "answered forwards the text", action: vibekit.UserInputActionAnswered, wantAnswer: typed},
		{name: "dismissed forwards no text", action: vibekit.UserInputActionDismissed, wantAnswer: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &resultBridge{}
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: bridge, takeOK: true}
			cmd := decisionCommand(t, vibekit.CmdUserInputResponse, vibekit.UserInputResponseCommand{
				RequestID: decisionRequestID,
				Action:    tc.action,
				Answer:    typed,
			})

			if _, err := CmdUserInputResponse(t.Context(), deps, deps, cmd); err != nil {
				t.Fatalf("CmdUserInputResponse(%s) = %v", tc.action, err)
			}

			if len(bridge.results) != 1 {
				t.Fatalf("got %d answers, want 1", len(bridge.results))
			}
			result, ok := bridge.results[0].(vibekit.UserInputResult)
			if !ok {
				t.Fatalf("answer = %T, want vibekit.UserInputResult", bridge.results[0])
			}
			if result.Action != tc.action {
				t.Errorf("action = %q, want %q", result.Action, tc.action)
			}
			if result.Answer != tc.wantAnswer {
				t.Errorf("answer for %q = %q, want %q", tc.action, result.Answer, tc.wantAnswer)
			}
		})
	}
}

// captureLogs swaps the slog default to a buffer-backed debug handler for the
// duration of the test and restores it on cleanup. The default is
// process-global, so a test using it must not run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// An answer kiro-cli accepted must leave no failure line behind. A log that
// reports a failure the code did not have is worse than no log at all: it sends
// whoever reads it after an incident looking at the wrong handler.
func TestDecisionHandlers_AnAcceptedAnswerLogsNoFailure(t *testing.T) {
	for _, tc := range decisionCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			deps := &takeDeps{benchDeps: newBenchDeps(), bridge: &countingBridge{}, takeOK: true}

			if _, err := tc.run(deps); err != nil {
				t.Fatalf("%s = %v, want it to succeed", tc.name, err)
			}

			for _, level := range []string{"level=ERROR", "level=WARN"} {
				if strings.Contains(logs.String(), level) {
					t.Errorf("a successful answer logged %s: %s", level, logs.String())
				}
			}
		})
	}
}
