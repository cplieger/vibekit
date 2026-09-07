package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// tombstonedChats is a chat store that refuses every write the way the real one
// refuses a write to an id deleted inside the tombstone window.
type tombstonedChats struct{ ChatStore }

func (tombstonedChats) Mutate(context.Context, vibekit.ChatID, func(*vibekit.Chat, bool) bool) error {
	return chat.ErrTombstoned
}

// promptSpy answers every role and records the two things a refused prompt must
// not do: open a bridge, and broadcast anything other than the refusal.
type promptSpy struct {
	hostDouble
	opened int
	events []vibekit.ServerEvent
}

func (s *promptSpy) OpenBridge(context.Context, vibekit.ChatID, string) (Bridge, error) {
	s.opened++
	return nil, errors.New("the bridge must not be opened")
}

func (s *promptSpy) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	s.events = append(s.events, evt)
}

func promptReq(t *testing.T, chatID vibekit.ChatID, text string) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(vibekit.PromptCommand{Text: text, MessageID: "m-1"})
	if err != nil {
		t.Fatalf("marshal prompt payload: %v", err)
	}
	return &vibekit.ClientCommand{Type: vibekit.CmdPrompt, ChatID: chatID, Payload: payload}
}

// A prompt on a tombstoned chat is refused with 409, BEFORE the bridge is
// spawned.
//
// Every later step depends on the record the store just declined to create, and
// each of them failed silently: the bridge came up for a chat with no file, its
// own metadata persist was refused by the same tombstone, the prompt was SENT,
// the credits were spent, the agent wrote to the workspace for real, and the
// finished turn was discarded by a third refused write. Nothing reported any of
// it, because a refusal answered nil.
//
// The bridge counter is the load-bearing assertion. A test that only checked the
// status would pass for an implementation that refuses after the spawn, which is
// most of the cost.
func TestCmdPrompt_RefusesATombstonedChatBeforeSpawningABridge(t *testing.T) {
	spy := &promptSpy{hostDouble: newTestHost(t, tombstonedChats{testsupport.NewInMemoryChatStore()})}
	roles := promptRolesOf(spy)
	roles.bridges = spy
	roles.bus = spy

	_, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing"))

	if err == nil {
		t.Fatal("CmdPrompt on a tombstoned chat returned no error; the turn ran against a chat with no record")
	}
	if got := statusOf(err); got != http.StatusConflict {
		t.Errorf("CmdPrompt on a tombstoned chat = %d, want %d", got, http.StatusConflict)
	}
	if spy.opened != 0 {
		t.Errorf("OpenBridge called %d times on a refused prompt, want 0: a bridge without a chat record breaks the live-bridge invariant", spy.opened)
	}
}

// promptBridgeSpy hands the prompt path a live bridge and records the events the
// turn broadcasts.
type promptBridgeSpy struct {
	hostDouble
	bridge Bridge
	events []vibekit.ServerEvent
}

func (s *promptBridgeSpy) OpenBridge(context.Context, vibekit.ChatID, string) (Bridge, error) {
	return s.bridge, nil
}

func (s *promptBridgeSpy) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	s.events = append(s.events, evt)
}

// A prompt that failed because the backend rejected the TOKEN travels as
// auth_token_unavailable, not as the generic prompt_failed.
//
// That code is the only one in the client's routing table carrying a Sign in CTA,
// so it is what turns a dismissible toast with nothing to click into a
// non-dismissible banner with the one action that works. Sending the existing
// code is deliberately the whole client-side change: no new wire enum, no
// decoder regeneration.
func TestCmdPrompt_AnAuthFailureTravelsAsTheSignInCode(t *testing.T) {
	cases := map[string]struct {
		callErr  error
		wantCode vibekit.ErrorCode
	}{
		"the token was rejected": {
			callErr:  rpcErr(t, vibekit.RPCCodeInternal, "Authentication failed. Please sign in again.", nil),
			wantCode: vibekit.ErrCodeAuthTokenUnavailable,
		},
		// The control. Every other failure keeps the generic code, or the banner
		// stops meaning "sign in" and starts meaning "something went wrong". A
		// terminal class on purpose: a retried one would hold this case for the
		// retry loop's two 2s waits to assert a code the first attempt already
		// decided.
		"a refused payload": {
			callErr: rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{
				"details": "PromptTooLong",
			}),
			wantCode: vibekit.ErrCodePromptFailed,
		},
		"an entitlement refusal is not a sign-in problem": {
			callErr: rpcErr(t, vibekit.RPCCodeBridgeExited, "this account does not have access to them.", mappedErrorData{
				ErrorType:      "ModelRegistryAccessDeniedError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			wantCode: vibekit.ErrCodePromptFailed,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			spy := &promptBridgeSpy{
				hostDouble: newTestHost(t, store),
				bridge:     &recordingBridge{callErr: tc.callErr},
			}
			roles := promptRolesOf(spy)
			roles.bridges = spy
			roles.bus = spy
			join := &promptJoin{}
			roles.lifecycle = join

			// The POST answers at the ack; the failure happens after it and is
			// SSE-only, so the handler reports no error and the frame is joined on.
			if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
				t.Fatalf("CmdPrompt = %v, want the early ack", err)
			}
			join.join()

			var codes []vibekit.ErrorCode
			for _, evt := range spy.events {
				if evt.Type != vibekit.EventError {
					continue
				}
				p, ok := evt.Payload.(vibekit.ErrorPayload)
				if !ok {
					t.Fatalf("error event payload is %T, want vibekit.ErrorPayload", evt.Payload)
				}
				codes = append(codes, p.Code)
			}
			if len(codes) != 1 {
				t.Fatalf("error events = %v, want exactly one", codes)
			}
			if codes[0] != tc.wantCode {
				t.Errorf("error code = %q, want %q", codes[0], tc.wantCode)
			}
		})
	}
}

func TestReportPromptFailure_AuthClassLatchesForReadiness(t *testing.T) {
	cases := map[string]struct {
		callErr error
		want    bool
	}{
		"backend_rejects_credential": {
			callErr: rpcErr(t, vibekit.RPCCodeInternal, "Authentication failed. Please sign in again.", nil),
			want:    true,
		},
		"non_auth_failure": {
			callErr: rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{
				"details": "PromptTooLong",
			}),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spy := &promptBridgeSpy{
				hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore()),
				bridge:     &recordingBridge{callErr: tc.callErr},
			}
			readiness := new(AuthReadiness)
			roles := promptRolesOf(spy)
			roles.bridges = spy
			roles.bus = spy
			roles.auth = readiness
			join := &promptJoin{}
			roles.lifecycle = join

			if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
				t.Fatalf("CmdPrompt = %v, want the early ack", err)
			}
			join.join()

			if got := readiness.Unavailable(); got != tc.want {
				t.Errorf("AuthReadiness.Unavailable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReportPromptSuccess_ClearsAuthLatch(t *testing.T) {
	spy := &promptBridgeSpy{
		hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore()),
		bridge:     &recordingBridge{},
	}
	readiness := new(AuthReadiness)
	readiness.Record(errors.New("backend rejected the credential"))
	roles := promptRolesOf(spy)
	roles.bridges = spy
	roles.bus = spy
	roles.auth = readiness
	join := &promptJoin{}
	roles.lifecycle = join

	if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
		t.Fatalf("CmdPrompt = %v, want the early ack", err)
	}
	join.join()

	if readiness.Unavailable() {
		t.Error("AuthReadiness stayed unavailable after a completed prompt")
	}
}
