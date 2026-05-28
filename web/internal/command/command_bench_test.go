package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/pending"
)

// benchDeps is a minimal Dependencies stub for benchmarking dispatch overhead.
type benchDeps struct {
	dedup map[string][]byte
}

func newBenchDeps() *benchDeps { return &benchDeps{dedup: make(map[string][]byte)} }

func (d *benchDeps) CheckDedup(reqID string) ([]byte, bool) { v, ok := d.dedup[reqID]; return v, ok }
func (d *benchDeps) RecordDedup(reqID string, data []byte)  { d.dedup[reqID] = data }
func (d *benchDeps) Draining() bool                         { return false }
func (d *benchDeps) ChatStore() api.ChatStore               { return nil }
func (d *benchDeps) Broadcast(context.Context, api.ServerEvent) {}
func (d *benchDeps) GetBridge(api.ChatID) Bridge            { return nil }
func (d *benchDeps) GetOrCreateBridge(context.Context, api.ChatID, string, string) (Bridge, error) {
	return nil, nil
}
func (d *benchDeps) CloseBridge(api.ChatID)                                                    {}
func (d *benchDeps) PendingStore() *pending.Store                                              { return nil }
func (d *benchDeps) SupervisedSetTrust(api.ChatID)                                             {}
func (d *benchDeps) SupervisedClearTrust(api.ChatID, api.ClearReason)                          {}
func (d *benchDeps) ChatInSupervisedMode(context.Context, api.ChatID) bool                     { return false }
func (d *benchDeps) FlushPendingForChat(context.Context, api.ChatID, api.ClearReason)          {}
func (d *benchDeps) ClearPendingPermsForChat(api.ChatID)                                       {}
func (d *benchDeps) RemovePendingPerm(int64)                                                   {}
func (d *benchDeps) Checkpoints() api.CheckpointService                                       { return nil }
func (d *benchDeps) AdvanceCheckpointTurn(context.Context, api.ChatID)                         {}
func (d *benchDeps) WorkDir() string                                                           { return "/tmp" }
func (d *benchDeps) ConfigDir() string                                                         { return "/tmp" }
func (d *benchDeps) ShutdownCtx() context.Context                                              { return context.Background() }
func (d *benchDeps) InflightAdd(int)                                                           {}
func (d *benchDeps) InflightDone()                                                             {}
func (d *benchDeps) InflightGo(func())                                                         {}
func (d *benchDeps) CleanupChatState(context.Context, api.ChatID)                              {}
func (d *benchDeps) UtilityPrompt(context.Context, string) (string, error)                     { return "", nil }
func (d *benchDeps) MCPWaitForReady(context.Context, time.Duration) bool                       { return true }
func (d *benchDeps) ResolveInsideWorkDir(string) (string, error)                               { return "", nil }
func (d *benchDeps) PrimeIfNeeded(context.Context, api.ChatID, Bridge)                         {}
func (d *benchDeps) IsEmptyTurn(*api.RPCResponse, api.ChatID) bool                             { return false }
func (d *benchDeps) EmitTurnEndedWithStats(context.Context, api.ChatID, *api.RPCResponse, float64, float64) {
}

func BenchmarkDispatcherServeHTTP(b *testing.B) {
	deps := newBenchDeps()
	d := New(deps)
	d.Register("create_chat", func(_ context.Context, w http.ResponseWriter, _ *api.ClientCommand) {
		api.WriteRawJSON(w, []byte(`{"ok":true}`))
	})

	body, _ := json.Marshal(api.ClientCommand{
		Type:      "create_chat",
		RequestID: "req-bench-1",
		ChatID:    "chat-bench-1",
	})

	b.Run("cache_miss", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			// Use unique request IDs to avoid dedup cache hits.
			reqBody, _ := json.Marshal(api.ClientCommand{
				Type:      "create_chat",
				RequestID: "req-" + string(rune('A'+i%26)) + string(rune('0'+i%10)),
				ChatID:    "chat-bench-1",
			})
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(reqBody))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})

	b.Run("cache_hit", func(b *testing.B) {
		// Prime the dedup cache.
		req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body))
		w := httptest.NewRecorder()
		d.ServeHTTP(w, req)

		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(body))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})

	b.Run("unknown_command", func(b *testing.B) {
		unknownBody, _ := json.Marshal(api.ClientCommand{
			Type:      "nonexistent_cmd",
			RequestID: "req-unknown",
			ChatID:    "chat-bench-1",
		})
		b.ReportAllocs()
		for range b.N {
			req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(unknownBody))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)
		}
	})
}
