package hub

// Tests for mcp_registry.go: the in-memory runtime view of MCP
// servers reported connected / needing OAuth / failed by kiro-cli.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/mcp"
)

// fakeMCPConfig implements api.MCPConfig for registry filter tests.
type fakeMCPConfig struct {
	enabled map[string]struct{}
	mu      sync.Mutex
}

func (f *fakeMCPConfig) ACPServers(_ context.Context) []map[string]any { return nil }
func (f *fakeMCPConfig) EnabledNames(_ context.Context) map[string]struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]struct{}, len(f.enabled))
	for k := range f.enabled {
		out[k] = struct{}{}
	}
	return out
}

func newHubWithMCPConfig(cfg api.MCPConfig) *Hub {
	cs := newFakeChatStore()
	factory := func() api.ACPBridge { return newFakeBridge() }
	var opts []Option
	if cfg != nil {
		opts = append(opts, WithMCPConfig(cfg))
	}
	h := New("/tmp/work", factory, cs, opts...)
	cs.SetBroadcaster(h)
	return h
}

func TestMCPRegistry_RecordConnectedPopulatesSnapshot(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.recordConnected(context.Background(), "github", nil, nil, nil)
	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v, want 1 server", snap)
	}
	if snap[0].Name != "github" || snap[0].State != mcpStateConnected {
		t.Errorf("snapshot[0] = %+v", snap[0])
	}
}

func TestMCPRegistry_RecordOAuthOverridesState(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.recordConnected(context.Background(), "linear", nil, nil, nil)
	h.mcpRegistry.recordOAuth(context.Background(), "linear", "https://oauth.example/auth")

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap[0].State != mcpStateOAuth {
		t.Errorf("state = %q, want %q", snap[0].State, mcpStateOAuth)
	}
	if snap[0].OAuthURL != "https://oauth.example/auth" {
		t.Errorf("oauth url = %q", snap[0].OAuthURL)
	}
}

func TestMCPRegistry_RecordInitFailureRecordsError(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	_, before := h.sse.hub.Bounds()
	h.mcpRegistry.recordInitFailure(context.Background(), "broken", "connection refused")

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 || snap[0].Name != "broken" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap[0].State != mcpStateFailed {
		t.Errorf("state = %q, want %q", snap[0].State, mcpStateFailed)
	}
	if snap[0].Error != "connection refused" {
		t.Errorf("error = %q", snap[0].Error)
	}
	// mcp_failed SSE must be emitted.
	types := extractTypes(t, bufferedSince(h, before))
	found := false
	for _, tp := range types {
		if tp == "mcp_failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("mcp_failed event not emitted, got %v", types)
	}
}

func TestMCPRegistry_ClearAllEmitsDisconnect(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.recordConnected(context.Background(), "a", nil, nil, nil)
	h.mcpRegistry.recordConnected(context.Background(), "b", nil, nil, nil)

	_, before := h.sse.hub.Bounds()
	h.mcpRegistry.clearAll(context.Background())

	if len(h.mcpRegistry.Snapshot()) != 0 {
		t.Error("clearAll left entries in registry")
	}
	types := extractTypes(t, bufferedSince(h, before))
	got := 0
	for _, tp := range types {
		if tp == "mcp_disconnected" {
			got++
		}
	}
	if got != 2 {
		t.Errorf("got %d mcp_disconnected events, want 2 (types=%v)", got, types)
	}
}

func TestMCPRegistry_ClearAllOnEmptyNoEvents(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	_, before := h.sse.hub.Bounds()
	h.mcpRegistry.clearAll(context.Background())
	if _, head := h.sse.hub.Bounds(); head != before {
		t.Error("clearAll on empty registry emitted events")
	}
}

func TestMCPRegistry_FiltersDisabledServerNotifications(t *testing.T) {
	cfg := &fakeMCPConfig{enabled: map[string]struct{}{"github": {}}}
	h := newHubWithMCPConfig(cfg)
	h.mcpRegistry.recordConnected(context.Background(), "github", nil, nil, nil)
	h.mcpRegistry.recordConnected(context.Background(), "disabled-server", nil, nil, nil)
	h.mcpRegistry.recordInitFailure(context.Background(), "another-disabled", "x")

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 || snap[0].Name != "github" {
		t.Errorf("snapshot = %+v, want only github", snap)
	}
}

func TestMCPRegistry_SnapshotIsStableAlphabetically(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.recordConnected(context.Background(), "zulu", nil, nil, nil)
	h.mcpRegistry.recordConnected(context.Background(), "alpha", nil, nil, nil)
	h.mcpRegistry.recordConnected(context.Background(), "mike", nil, nil, nil)

	names := make([]string, 0)
	for _, s := range h.mcpRegistry.Snapshot() {
		names = append(names, s.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("snapshot not sorted: %v", names)
	}
}

func TestMCPRegistry_OnChangeFiresOutsideLock(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	var mu sync.Mutex
	count := 0
	done := make(chan struct{}, 10)
	h.mcpRegistry.SetOnChange(func() {
		mu.Lock()
		count++
		mu.Unlock()
		done <- struct{}{}
	})
	h.mcpRegistry.recordConnected(context.Background(), "a", nil, nil, nil)
	h.mcpRegistry.recordOAuth(context.Background(), "a", "url")
	h.mcpRegistry.recordInitFailure(context.Background(), "a", "err")
	h.mcpRegistry.clearAll(context.Background())

	// With debounced onChange, rapid-fire mutations coalesce into
	// fewer callbacks. Wait for at least one callback to confirm
	// the notifier fires outside the lock.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onChange never fired")
	}
	// Allow debounce to settle.
	time.Sleep(200 * time.Millisecond)
	// Drain any additional callbacks.
	for {
		select {
		case <-done:
		default:
			goto drained
		}
	}
drained:
	mu.Lock()
	defer mu.Unlock()
	if count < 1 {
		t.Errorf("onChange count = %d, want >= 1", count)
	}
	// Shutdown to clean up the notifier goroutine.
	close(h.lifecycle.done)
}

func TestMCPRegistry_HandleStatusReturnsJSON(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.recordConnected(context.Background(), "github", nil, nil, nil)
	h.mcpRegistry.recordInitFailure(context.Background(), "broken", "no auth")

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil)
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Servers []statusServer `json:"servers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Servers) != 2 {
		t.Fatalf("body = %+v", body)
	}
	// Alphabetical sort: broken then github.
	if body.Servers[0].Name != "broken" || body.Servers[0].State != "failed" {
		t.Errorf("body[0] = %+v", body.Servers[0])
	}
	if body.Servers[0].Error != "no auth" {
		t.Errorf("error = %q", body.Servers[0].Error)
	}
	if body.Servers[1].Name != "github" || body.Servers[1].State != "connected" {
		t.Errorf("body[1] = %+v", body.Servers[1])
	}
}

// BenchmarkMCPRegistrySnapshot measures the sorted-snapshot hot path
// with varying numbers of registered servers. Snapshot is called on
// every SSE reconnect and bridge spawn.
func BenchmarkMCPRegistrySnapshot(b *testing.B) {
	for _, n := range []int{1, 5, 20} {
		b.Run(fmt.Sprintf("servers=%d", n), func(b *testing.B) {
			h := newHubWithMCPConfig(nil)
			for i := range n {
				h.mcpRegistry.recordConnected(context.Background(), fmt.Sprintf("server-%02d", i), nil, nil, nil)
			}
			b.ResetTimer()
			for range b.N {
				_ = h.mcpRegistry.Snapshot()
			}
		})
	}
}

// TestRealMCPConfig_Contract runs the shared MCPConfigContractTest against
// the real mcp.Store implementation to catch drift between the fake and real.
func TestRealMCPConfig_Contract(t *testing.T) {
	MCPConfigContractTest(t, func(t *testing.T) api.MCPConfig {
		t.Helper()
		dir := t.TempDir()
		s, err := mcp.New(context.Background(), dir, nil, mcp.WithKASConfigPath(filepath.Join(dir, "kas-mcp.json")))
		if err != nil {
			t.Fatalf("mcp.New: %v", err)
		}
		return s
	})
}
