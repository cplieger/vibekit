package translate

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestAskHandlers_AnswerAnUndecodableRequest is the red check for the wedge
// class on the three ask handlers.
//
// Each handler verifies the envelope id before it decodes, so a frame reaching
// the decode is provably a REQUEST, and KAS's sendRequest carries no timeout: a
// bare return strands the tool batch until process teardown, and the unattended
// floor cannot rescue it because the frame never reached a tracker. So the
// assertion is that an answer was written ON THAT ID at all.
//
// The payload half is the part that must not regress into something worse than
// the drop. KAS's turn-approval path fails OPEN — it answers approved when the
// requestPermission call throws — so a JSON-RPC error on a turn_approval frame
// would apply every unreviewed write in the turn. Hence rpcErr must be nil and
// the result must be the kind's own fail-closed value, and for a permission that
// value must name no option: fabricating one answers with a choice the request
// never offered.
func TestAskHandlers_AnswerAnUndecodableRequest(t *testing.T) {
	// Each case's params are well-formed JSON whose TYPES do not match the
	// handler's decode struct, which is the reachable trigger: the fields are
	// type-stable strings today, so what breaks a decode is an upstream shape
	// change to a field — including a decorative one.
	cases := map[string]struct {
		params map[string]any
		call   func(tr *Translator, chatID vibekit.ChatID, msg *vibekit.RPCResponse)
		want   any
	}{
		"permission: options is not an array": {
			params: map[string]any{"sessionId": "s", "options": 7},
			call: func(tr *Translator, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
				tr.HandlePermissionRequest(t.Context(), chatID, msg)
			},
			want: vibekit.PermissionOutcomeCancelled(),
		},
		"permission: a decorative meta field changed shape": {
			// _meta.kiro.consent is 2.19.1 decoration that shares the struct with
			// the routing fields, so a change to it takes the whole ask down.
			params: map[string]any{
				"sessionId": "s",
				"_meta":     map[string]any{"kiro": map[string]any{"consent": true}},
			},
			call: func(tr *Translator, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
				tr.HandlePermissionRequest(t.Context(), chatID, msg)
			},
			want: vibekit.PermissionOutcomeCancelled(),
		},
		"elicitation: the body is not an object": {
			params: map[string]any{"sessionId": "s", "elicitation": "not-an-object"},
			call: func(tr *Translator, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
				tr.HandleElicitationCreate(t.Context(), chatID, msg)
			},
			want: vibekit.ElicitationResult{Action: vibekit.ElicitationActionCancel},
		},
		"user input: options is not an array": {
			params: map[string]any{"sessionId": "s", "question": "Which?", "options": 7},
			call: func(tr *Translator, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
				tr.HandleUserInput(t.Context(), chatID, msg)
			},
			want: vibekit.UserInputResult{Action: vibekit.UserInputActionDismissed},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			deps := newBaseDeps()
			tr := New(rolesOf(deps))
			id := int64(4242)

			tc.call(tr, "c1", &vibekit.RPCResponse{ID: &id, Params: mustJSON(t, tc.params)})

			if len(deps.asked) != 1 {
				t.Fatalf("got %d answers, want 1 — an unanswered request wedges the tool batch", len(deps.asked))
			}
			got := deps.asked[0]
			if got.requestID != id {
				t.Errorf("answered request_id = %d, want %d", got.requestID, id)
			}
			if got.chatID != "c1" {
				t.Errorf("answered chat_id = %q, want c1 — an empty one misses the bridge lookup", got.chatID)
			}
			if got.rpcErr != nil {
				t.Errorf("answered with rpcErr = %v, want nil — an RPC error on a turn_approval frame auto-approves the turn", got.rpcErr)
			}
			gotJSON, wantJSON := string(mustJSON(t, got.result)), string(mustJSON(t, tc.want))
			if gotJSON != wantJSON {
				t.Errorf("answer = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// TestHandlePermissionRequest_RefusalNamesNoOption pins the one property that
// makes cancelled the only safe permission answer here: it selects nothing.
// Answering with a fabricated optionId would apply a choice the request never
// offered, which run_unattended.go's floor already refuses to do.
func TestHandlePermissionRequest_RefusalNamesNoOption(t *testing.T) {
	deps := newBaseDeps()
	tr := New(rolesOf(deps))
	id := int64(7)

	tr.HandlePermissionRequest(t.Context(), "c1", &vibekit.RPCResponse{
		ID:     &id,
		Params: mustJSON(t, map[string]any{"options": 7}),
	})

	if len(deps.asked) != 1 {
		t.Fatalf("got %d answers, want 1", len(deps.asked))
	}
	out, ok := deps.asked[0].result.(*vibekit.PermissionOutcome)
	if !ok {
		t.Fatalf("answer = %T, want *vibekit.PermissionOutcome", deps.asked[0].result)
	}
	if out.Outcome.OptionID != "" {
		t.Errorf("OptionID = %q, want empty — the refusal must name no option", out.Outcome.OptionID)
	}
	if out.Outcome.Outcome != string(vibekit.StopReasonCancelled) {
		t.Errorf("outcome = %q, want %q", out.Outcome.Outcome, vibekit.StopReasonCancelled)
	}
}

// TestAskHandlers_DecodedFrameStillReachesTheTracker is the other direction: the
// refusal must not have displaced the ordinary path, which registers the ask for
// reconnect replay and answers nothing itself.
func TestAskHandlers_DecodedFrameStillReachesTheTracker(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	id := int64(11)

	tr.HandlePermissionRequest(t.Context(), "c1", &vibekit.RPCResponse{
		ID: &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "s",
			"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "Write", "kind": "edit"},
			"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		}),
	})

	if _, ok := findPermissionNeeded(t, events); !ok {
		t.Error("no permission_needed event broadcast for a well-formed ask")
	}
	if len(deps.asked) != 0 {
		t.Errorf("answered a well-formed ask on the wire (%d times); the user's reply is the answer", len(deps.asked))
	}
}
