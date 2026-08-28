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

			if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err == nil {
				t.Fatal("CmdPrompt returned no error for a failed prompt Call")
			}

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

// tokenSpy counts the credential invalidations the prompt path asks for.
type tokenSpy struct{ calls int }

func (s *tokenSpy) Invalidate() { s.calls++ }

// A prompt that failed because the backend rejected the TOKEN also withdraws that
// token from the vend cache.
//
// The banner alone is not the remedy. The credential was accepted at the vend and
// rejected at the backend, which is what switching the active kiro-cli account
// looks like — invalidated without being expired — so the cache would keep serving
// it for up to (expiry - reuseLeeway) and the user's sign-in would change nothing
// until it aged out. Withdrawing it makes the next auth callback re-ask the CLI.
//
// The two controls are the point: the class this keys on is the same one that
// picks the sign-in code, so anything wider would spend a subprocess on every
// refused payload.
func TestCmdPrompt_AnAuthFailureInvalidatesTheCachedToken(t *testing.T) {
	cases := map[string]struct {
		callErr error
		want    int
	}{
		"the token was rejected": {
			callErr: rpcErr(t, vibekit.RPCCodeInternal, "Authentication failed. Please sign in again.", nil),
			want:    1,
		},
		"a refused payload": {
			callErr: rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{
				"details": "PromptTooLong",
			}),
			want: 0,
		},
		"an entitlement refusal is not a sign-in problem": {
			callErr: rpcErr(t, vibekit.RPCCodeBridgeExited, "this account does not have access to them.", mappedErrorData{
				ErrorType:      "ModelRegistryAccessDeniedError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			want: 0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spy := &promptBridgeSpy{
				hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore()),
				bridge:     &recordingBridge{callErr: tc.callErr},
			}
			tokens := &tokenSpy{}
			roles := promptRolesOf(spy)
			roles.bridges = spy
			roles.bus = spy
			roles.tokens = tokens

			if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err == nil {
				t.Fatal("CmdPrompt returned no error for a failed prompt Call")
			}

			if tokens.calls != tc.want {
				t.Errorf("Invalidate called %d times, want %d", tokens.calls, tc.want)
			}
		})
	}
}
