package command

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

func TestValidatePromptPayload(t *testing.T) {
	valid := func(text, msgID, model string) []byte {
		b, _ := json.Marshal(api.PromptCommand{Text: text, MessageID: msgID, Model: model})
		return b
	}

	tests := []struct {
		name       string
		payload    []byte
		wantStatus int
		wantErr    bool
	}{
		{"valid minimal", valid("hello", "msg-1", ""), 0, false},
		{"valid with model", valid("hi", "msg-2", "claude"), 0, false},
		{"empty text", valid("", "msg-1", ""), http.StatusBadRequest, true},
		{"text at exact cap", valid(strings.Repeat("a", maxPromptBytes), "msg-1", ""), 0, false},
		{"oversized text", valid(strings.Repeat("x", maxPromptBytes+1), "msg-1", ""), http.StatusRequestEntityTooLarge, true},
		{"missing message_id", valid("hi", "", ""), http.StatusBadRequest, true},
		{"invalid message_id", valid("hi", "msg id/bad", ""), http.StatusBadRequest, true},
		{"invalid model", valid("hi", "msg-1", "bad model!"), http.StatusBadRequest, true},
		{"malformed json", []byte(`{not json`), http.StatusBadRequest, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &api.ClientCommand{Payload: tc.payload}
			_, status, err := validatePromptPayload(cmd)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}

// TestTurnContext_SurvivesRequestCancel is the regression test for the
// mid-turn-disconnect bug: a client drop cancels the prompt POST's
// request context, but the turn's context (which the bridge Call runs
// under) must NOT be cancelled — otherwise the Call aborts, prompt_failed
// fires before EmitTurnEndedWithStats, and the assistant buffer is lost
// while kiro-cli keeps running the turn.
func TestTurnContext_SurvivesRequestCancel(t *testing.T) {
	reqCtx, reqCancel := context.WithCancel(t.Context())
	shutdownCtx, shutdownCancel := context.WithCancel(t.Context())
	defer shutdownCancel()

	turnCtx, cleanup := turnContext(reqCtx, shutdownCtx)
	defer cleanup()

	// Simulate a mid-turn client disconnect.
	reqCancel()

	select {
	case <-turnCtx.Done():
		t.Fatal("turn context cancelled by request disconnect: the bridge Call would abort before turn_ended")
	case <-time.After(50 * time.Millisecond):
	}
	if err := turnCtx.Err(); err != nil {
		t.Fatalf("turn context Err = %v after request cancel, want nil", err)
	}
}

// TestTurnContext_CancelsOnShutdown verifies hub shutdown still tears the
// turn down — cancellation must move from the request context to the
// shutdown context, not disappear entirely.
func TestTurnContext_CancelsOnShutdown(t *testing.T) {
	shutdownCtx, shutdownCancel := context.WithCancel(t.Context())

	turnCtx, cleanup := turnContext(t.Context(), shutdownCtx)
	defer cleanup()

	shutdownCancel()

	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("turn context not cancelled on hub shutdown")
	}
}

// TestTurnContext_CleanupCancels verifies the returned cleanup cancels the
// turn context (normal handler-return teardown, and unregisters the
// shutdown AfterFunc so it can't leak).
func TestTurnContext_CleanupCancels(t *testing.T) {
	turnCtx, cleanup := turnContext(t.Context(), t.Context())
	cleanup()
	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("cleanup did not cancel the turn context")
	}
}

// TestTurnContext_PreservesValues verifies request-scoped values survive
// the WithoutCancel detachment (only cancellation is severed, not values).
func TestTurnContext_PreservesValues(t *testing.T) {
	type ctxKey string
	const k ctxKey = "trace-id"
	reqCtx := context.WithValue(t.Context(), k, "abc123")

	turnCtx, cleanup := turnContext(reqCtx, t.Context())
	defer cleanup()

	if got := turnCtx.Value(k); got != "abc123" {
		t.Fatalf("turn context lost request-scoped value: got %v, want abc123", got)
	}
}
