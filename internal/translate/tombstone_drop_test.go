package translate

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// lateWrites are the handlers in this package that persist something AFTER the
// frame that caused it, which is every write it makes. Each entry drives one
// site of the tombstone contract on ChatRecords.
//
// Keyed by the log message the site emits on a real failure, so a case whose
// drop regresses names the exact slog line to look for.
func lateWrites() map[string]func(*Translator, context.Context, vibekit.ChatID) {
	permID := int64(1)
	return map[string]func(*Translator, context.Context, vibekit.ChatID){
		"persist plan": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			tr.HandlePlan(ctx, id, mustJSONCtx(map[string]any{
				"entries": []map[string]any{{"content": "step", "status": "pending"}},
			}))
		},
		"mode update persist": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			tr.HandleModeUpdate(ctx, id, mustJSONCtx(map[string]any{"currentModeId": "spec"}))
		},
		"compaction: append event / set watermark": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			summary := "rolled up"
			tr.handleCompactionCompleted(ctx, id, &summary)
		},
		"compaction: append failed event": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			tr.handleCompactionFailed(ctx, id, "out of context")
		},
		"safety: append block event": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			tr.HandleSafetyStatusChanged(ctx, id, &vibekit.RPCResponse{
				Params: mustJSONCtx(map[string]any{"status": "blocked", "detail": "refused"}),
			})
		},
		"focus title: persist": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			tr.handleFocusUpdate(ctx, id, &focusUpdate{Title: "Agent picked this"})
		},
		"agent_not_found: persist fallback": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			tr.HandleAgentNotFound(ctx, id, &vibekit.RPCResponse{
				ID:     &permID,
				Params: mustJSONCtx(map[string]any{"requestedAgent": "nope", "fallbackAgent": "vibe"}),
			})
		},
		"persist v3 turn summary": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			tr.persistTurnSummary(ctx, id, []promptTurnSummary{{Unit: meteringUnitCredit, Usage: 0.5}}, 1200, false)
		},
		"persist v3 usage": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			tr.HandleUsageUpdate(ctx, id, mustJSONCtx(map[string]any{"size": 100, "used": 50}))
		},
		"persist v3 config catalog": func(tr *Translator, ctx context.Context, id vibekit.ChatID) {
			tr.HandleConfigOptionUpdate(ctx, id, mustJSONCtx(map[string]any{
				"configOptions": []map[string]any{{
					"id":      "model",
					"type":    "select",
					"options": []map[string]any{{"name": "Opus", "value": "claude-opus-5"}},
				}},
			}))
		},
	}
}

// TestLateWrites_TombstonedRefusalIsNotAnError pins the drop the tombstone was
// designed for.
//
// chat.ErrTombstoned means the write was DECLINED because the chat id was
// deleted inside the tombstone window: the mutator never ran, nothing reached
// disk, nothing was broadcast. Every write in this package races a possible
// delete, so surfacing that as a logged error would put an ERROR line in the
// operator's log for the mechanism working as intended — on the most travelled
// paths in the app, several per turn.
func TestLateWrites_TombstonedRefusalIsNotAnError(t *testing.T) {
	for name, drive := range lateWrites() {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			defer captureSlog(&logs)()

			deps, _ := newEventCaptureDeps()
			deps.store = &recStore{appendErr: chat.ErrTombstoned, mutateErr: chat.ErrTombstoned, upsertErr: chat.ErrTombstoned}
			tr := New(rolesOf(deps))

			drive(tr, t.Context(), "c1")

			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("a tombstoned write logged an error:\n%s", logs.String())
			}
		})
	}
}

// TestLateWrites_OtherErrorsStillLog is the other half, and it is what keeps the
// drop narrow: matching the sentinel must not swallow a real persist failure —
// a full disk, a permission fault, a corrupt chat file.
func TestLateWrites_OtherErrorsStillLog(t *testing.T) {
	for name, drive := range lateWrites() {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			defer captureSlog(&logs)()

			deps, _ := newEventCaptureDeps()
			deps.store = &recStore{appendErr: errBoom, mutateErr: errBoom, upsertErr: errBoom}
			tr := New(rolesOf(deps))

			drive(tr, t.Context(), "c1")

			if !strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("a real persist failure logged no error:\n%s", logs.String())
			}
		})
	}
}

// TestHandleCompactionFailed_TombstonedChatGetsNoBanner is the one site whose
// drop is observable on the wire rather than only in the log: a refused persist
// used to fall through to the client error banner, so a chat the user had just
// deleted still raised one.
func TestHandleCompactionFailed_TombstonedChatGetsNoBanner(t *testing.T) {
	deps, events := newEventCaptureDeps()
	deps.store = &recStore{appendErr: chat.ErrTombstoned}
	tr := New(rolesOf(deps))

	tr.handleCompactionFailed(t.Context(), "c1", "out of context")

	for _, e := range *events {
		if e.Type == vibekit.EventError {
			t.Errorf("broadcast %s for a deleted chat", e.Type)
		}
	}
}

// TestHandleCompactionCompleted_TombstonedChatStopsAfterOneWrite pins the return
// rather than the log level: a refused append means the chat is gone, so the
// watermark Mutate that follows it can only be refused too.
func TestHandleCompactionCompleted_TombstonedChatStopsAfterOneWrite(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	store := &recStore{appendErr: chat.ErrTombstoned, mutateErr: chat.ErrTombstoned}
	deps.store = store
	tr := New(rolesOf(deps))

	summary := "rolled up"
	tr.handleCompactionCompleted(t.Context(), "c1", &summary)

	if store.appendCalls != 1 {
		t.Errorf("appendCalls = %d, want 1", store.appendCalls)
	}
	if store.mutateCalls != 0 {
		t.Errorf("mutateCalls = %d, want 0 — the chat is gone, so there is nothing to watermark", store.mutateCalls)
	}
}

// mustJSONCtx is mustJSON without a *testing.T, for the table above: a case's
// payload is built once when the table is composed, outside any subtest.
func mustJSONCtx(v any) []byte {
	return mustJSONRapid(v)
}
