// MCP runtime registry.
//
// KIRO-CLI 2.0.1 tui.js:886710603bed3fb6 — payload shapes pinned.
//
// Tracks which configured MCP servers kiro-cli has reported as
// initialised or failed. Populated by translateACPEvent when it sees
// _kiro.dev/mcp/server_initialized, _kiro.dev/mcp/oauth_request, or
// _kiro.dev/mcp/server_init_failure notifications; cleared on bridge
// exit.
//
// Per-server tool lists are NOT stored here — kiro-cli doesn't include
// them in server_initialized. Tools are advertised via
// _kiro.dev/commands/available and consumed by translate_commands.go.
//
// The registry is:
//
//   - A single source of truth for the MCP page's status column
//     (connected / oauth / failed / idle).
//   - The source of the steering doc's "Connected integrations"
//     section (regenerated whenever the registry changes).
//
// Not persisted: each bridge re-announces its MCP servers on start, so
// the registry rebuilds itself on every container restart.

package hub

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// mcpServerState is an alias for the api-level MCPServerState enum.
type mcpServerState = api.MCPServerState

const (
	mcpStateIdle      mcpServerState = "idle"
	mcpStateConnected mcpServerState = "connected"
	mcpStateOAuth     mcpServerState = "needs_auth"
	mcpStateFailed    mcpServerState = "failed"
)

// mcpServerRuntime is the registry's per-server record. Prompts and
// Resources are the discovery lists a connected server advertises (from
// the _kiro/mcp/status notification); empty for non-connected servers.
type mcpServerRuntime struct {
	Name      string
	State     mcpServerState
	OAuthURL  string
	Error     string
	Prompts   []api.MCPPromptInfo
	Resources []api.MCPResourceInfo
}

// mcpRegistry is the hub's in-memory view of connected MCP servers.
type mcpRegistry struct {
	hub      *Hub
	servers  map[string]*mcpServerRuntime
	onChange func()
	// notifyCh coalesces rapid-fire onChange callbacks into a single
	// debounced invocation. Capacity 1 means multiple signals within
	// the debounce window collapse into one cb() call.
	notifyCh chan struct{}
	// readyCh is closed when the first _kiro.dev/commands/available
	// notification arrives (which lists expected MCP servers). Prompts
	// wait on this channel before forwarding to kiro-cli.
	readyCh chan struct{}
	mu      sync.RWMutex
}

func newMCPRegistry(h *Hub) *mcpRegistry {
	r := &mcpRegistry{
		hub:      h,
		servers:  make(map[string]*mcpServerRuntime),
		notifyCh: make(chan struct{}, 1),
		readyCh:  make(chan struct{}),
	}
	return r
}

// SetOnChange wires an invalidation callback fired (outside the lock)
// whenever the registry mutates. The steering generator subscribes here.
// Starts the debounced notifier goroutine on first call.
func (r *mcpRegistry) SetOnChange(fn func()) {
	r.mu.Lock()
	first := r.onChange == nil && fn != nil
	r.onChange = fn
	r.mu.Unlock()
	if first {
		r.startNotifier()
	}
}

// WaitForReady blocks until MCP servers have reported their status
// (via commands/available) or the timeout expires. Returns true if
// ready, false on timeout or context cancellation.
func (r *mcpRegistry) WaitForReady(ctx context.Context, timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-r.readyCh:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Snapshot returns a deep copy of the current registry state, sorted
// by server name for stable output.
func (r *mcpRegistry) Snapshot() []mcpServerRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]mcpServerRuntime, 0, len(r.servers))
	for _, v := range r.servers {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RegisterRoutes wires the runtime MCP endpoints: a read-only status view
// plus the live-control routes (reconnect / prompt / resource) that act on
// the running chat bridges. The exact paths are more specific than the
// mcp store's "/api/mcp/" subtree handler, so they take precedence on the
// shared mux (same as "/api/mcp/status" already does). See mcp_control.go
// for the handlers.
func (r *mcpRegistry) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/mcp/status", r.handleStatus)
	mux.HandleFunc("/api/mcp/reconnect", r.hub.handleMCPReconnect)
	mux.HandleFunc("/api/mcp/prompt", r.hub.handleMCPGetPrompt)
	mux.HandleFunc("/api/mcp/resource", r.hub.handleMCPGetResource)
}

// signalReady closes the readyCh so any goroutine waiting in
// WaitForReady unblocks. Called when the first commands/available
// notification arrives. Safe to call multiple times.
func (r *mcpRegistry) signalReady() {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.readyCh:
		// already closed
	default:
		close(r.readyCh)
	}
}

// recordConnected marks a server as connected (recording the prompts and
// resources it advertises), broadcasts mcp_connected, and fires onChange.
// Called from the _kiro/mcp/status handler when a server reports the
// "connected" state. prompts/resources may be nil (server exposes none).
func (r *mcpRegistry) recordConnected(ctx context.Context, name string, prompts []api.MCPPromptInfo, resources []api.MCPResourceInfo) {
	if !r.nameIsEnabled(ctx, name) {
		return
	}
	r.mu.Lock()
	r.servers[name] = &mcpServerRuntime{
		Name:      name,
		State:     mcpStateConnected,
		Prompts:   prompts,
		Resources: resources,
	}
	r.mu.Unlock()

	r.hub.Broadcast(ctx, api.NewEvent(api.EventMCPConnected, "", api.MCPConnectedPayload{Server: name}))
	r.signalChange()
}

// recordOAuth marks a server as waiting for OAuth and broadcasts the URL.
func (r *mcpRegistry) recordOAuth(ctx context.Context, name, url string) {
	if !r.nameIsEnabled(ctx, name) {
		return
	}
	r.mu.Lock()
	r.servers[name] = &mcpServerRuntime{
		Name:     name,
		State:    mcpStateOAuth,
		OAuthURL: url,
	}
	r.mu.Unlock()

	r.hub.Broadcast(ctx, api.NewEvent(api.EventMCPOAuthNeeded, "", api.MCPOAuthPayload{Server: name, URL: url}))
	r.signalChange()
}

// recordInitFailure marks a server as having failed initialisation.
// Broadcast mcp_failed so the client can render a red status and
// surface the error message. Disabled servers are silently dropped —
// the user has already chosen not to run them.
func (r *mcpRegistry) recordInitFailure(ctx context.Context, name, errMsg string) {
	if !r.nameIsEnabled(ctx, name) {
		return
	}
	r.mu.Lock()
	r.servers[name] = &mcpServerRuntime{
		Name:  name,
		State: mcpStateFailed,
		Error: errMsg,
	}
	r.mu.Unlock()

	r.hub.Broadcast(ctx, api.NewEvent(api.EventMCPFailed, "", api.MCPFailedPayload{Server: name, Error: errMsg}))
	r.signalChange()
}

// clearAll wipes the runtime registry and broadcasts mcp_disconnected
// for each server that had state. Called when the last bridge exits;
// MCP subprocesses are scoped to kiro-cli, so nothing is live anymore.
func (r *mcpRegistry) clearAll(ctx context.Context) {
	r.mu.Lock()
	prev := r.servers
	r.servers = make(map[string]*mcpServerRuntime)
	r.mu.Unlock()

	if len(prev) == 0 {
		return
	}
	for name := range prev {
		if ctx.Err() != nil {
			break
		}
		r.hub.Broadcast(ctx, api.NewEvent(api.EventMCPDisconnected, "", api.MCPDisconnectedPayload{Server: name}))
	}
	r.signalChange()
}

// nameIsEnabled guards against spurious notifications for servers the
// user has since disabled.
func (r *mcpRegistry) nameIsEnabled(ctx context.Context, name string) bool {
	if r.hub.mcpConfig == nil {
		return true
	}
	_, ok := r.hub.mcpConfig.EnabledNames(ctx)[name]
	return ok
}

// statusServer is the JSON shape for /api/mcp/status entries.
// mcpStatusResponse is the typed response for the MCP status endpoint.
type mcpStatusResponse struct {
	Servers []statusServer `json:"servers"`
}

type statusServer struct {
	Name      string                `json:"name"`
	State     api.MCPServerState    `json:"state"`
	OAuthURL  string                `json:"oauth_url,omitempty"`
	Error     string                `json:"error,omitempty"`
	Prompts   []api.MCPPromptInfo   `json:"prompts,omitempty"`
	Resources []api.MCPResourceInfo `json:"resources,omitempty"`
}

func (r *mcpRegistry) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := r.Snapshot()
	out := make([]statusServer, len(snap))
	for i, s := range snap {
		out[i] = statusServer(s)
	}
	api.WriteJSON(w, mcpStatusResponse{Servers: out})
}

// startNotifier launches the single long-lived goroutine that drains
// notifyCh and calls onChange with a short debounce. Must be called
// after SetOnChange. Exits when h.lifecycle.done closes.
func (r *mcpRegistry) startNotifier() {
	go func() {
		const debounce = 100 * time.Millisecond
		for {
			select {
			case <-r.hub.lifecycle.done:
				return
			case <-r.notifyCh:
			}
			// Debounce: wait a short window to coalesce rapid signals.
			t := time.NewTimer(debounce)
			select {
			case <-r.hub.lifecycle.done:
				t.Stop()
				return
			case <-t.C:
			}
			// Drain any signals that arrived during the debounce window.
			select {
			case <-r.notifyCh:
			default:
			}
			r.mu.RLock()
			cb := r.onChange
			r.mu.RUnlock()
			if cb != nil {
				cb()
			}
		}
	}()
}

// signalChange sends a non-blocking signal to the notifier goroutine.
// Multiple calls within the debounce window collapse into one cb().
func (r *mcpRegistry) signalChange() {
	select {
	case r.notifyCh <- struct{}{}:
	default:
		// Already signalled; the notifier will pick it up.
	}
}
