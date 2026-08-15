// MCP runtime registry.
//
// Tracks which configured MCP servers kiro-cli has reported as
// initialised or failed. Populated from the v3 `_kiro/mcp/status`
// notification (see internal/translate's MCP handling — the v2
// `_kiro.dev/mcp/*` notifications named here previously were removed
// with the v2 wire); cleared on bridge exit.
//
// Per-server TOOL lists live here too, alongside the prompts and resources they
// arrive with on that same notification. They used to be written into the MCP
// config file instead, which put agent-derived state in a user-intent file and
// did disk I/O on a notification path; once KAS's own config file became the
// source of truth that write also fed its watcher and bounced back as another
// status notification. (They were once attributed to the
// available_commands_update catalog, which was wrong and is now moot — that
// catalog is no longer decoded at all.)
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
	"cmp"
	"context"
	"net/http"
	"slices"
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
	mcpStateDisabled  mcpServerState = "disabled"
)

// mcpServerRuntime is the registry's per-server record. Prompts and
// Resources are the discovery lists a connected server advertises (from
// the _kiro/mcp/status notification); empty for non-connected servers.
//
// Origin says where the server came from, and it is the field that makes a row
// for a server vibekit never configured safe to show: the MCP page has no config
// entry to hang edit or delete on, so the row must declare itself read-only.
type mcpServerRuntime struct {
	Name      string
	State     mcpServerState
	Origin    api.Origin
	OAuthURL  string
	Error     string
	Tools     []string
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
	// readyCh is closed when the first `_kiro/mcp/status` notification arrives
	// (HandleMCPStatus → SignalReady), i.e. once KAS has reported a terminal
	// state for the session's MCP servers. Prompts wait on this channel before
	// forwarding to kiro-cli, so a tool call cannot race server init.
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
	slices.SortFunc(out, func(a, b mcpServerRuntime) int { return cmp.Compare(a.Name, b.Name) })
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
func (r *mcpRegistry) recordConnected(ctx context.Context, name string, tools []string, prompts []api.MCPPromptInfo, resources []api.MCPResourceInfo) {
	origin, ok := r.originFor(ctx, name)
	if !ok {
		return
	}
	r.mu.Lock()
	r.servers[name] = &mcpServerRuntime{
		Name:      name,
		State:     mcpStateConnected,
		Origin:    origin,
		Tools:     tools,
		Prompts:   prompts,
		Resources: resources,
	}
	r.mu.Unlock()

	r.hub.Broadcast(ctx, api.NewEvent(api.EventMCPConnected, "", api.MCPConnectedPayload{Server: name}))
	r.signalChange()
}

// recordOAuth marks a server as waiting for OAuth and broadcasts the URL.
func (r *mcpRegistry) recordOAuth(ctx context.Context, name, url string) {
	origin, ok := r.originFor(ctx, name)
	if !ok {
		return
	}
	r.mu.Lock()
	r.servers[name] = &mcpServerRuntime{
		Name:     name,
		State:    mcpStateOAuth,
		Origin:   origin,
		OAuthURL: url,
	}
	r.mu.Unlock()

	r.hub.Broadcast(ctx, api.NewEvent(api.EventMCPOAuthNeeded, "", api.MCPOAuthPayload{Server: name, URL: url}))
	r.signalChange()
}

// recordInitFailure marks a server as having failed initialisation.
// Broadcast mcp_failed so the client can render a red status and
// surface the error message. A server the user disabled is silently dropped —
// they have already chosen not to run it.
func (r *mcpRegistry) recordInitFailure(ctx context.Context, name, errMsg string) {
	origin, ok := r.originFor(ctx, name)
	if !ok {
		return
	}
	r.mu.Lock()
	r.servers[name] = &mcpServerRuntime{
		Name:   name,
		State:  mcpStateFailed,
		Origin: origin,
		Error:  errMsg,
	}
	r.mu.Unlock()

	r.hub.Broadcast(ctx, api.NewEvent(api.EventMCPFailed, "", api.MCPFailedPayload{Server: name, Error: errMsg}))
	r.signalChange()
}

// recordDisabled records a server KAS reports as "disabled". It is the one
// record path whose whole population is servers vibekit did NOT configure.
//
// A configured server is dropped here, whichever way its own flag points. The
// MCP page renders that server's off state from its config row's `enabled:
// false`, so a runtime row saying the same thing is a second copy of one fact;
// and for a server the user disabled mid-session, recording anything at all is
// what the narrowed guard exists to keep from happening. An UNCONFIGURED server
// has no config row, so this frame is the only evidence it exists — it becomes a
// read-only row rather than being discarded.
//
// No SSE event: there is no mcp_disabled type, and a disabled server never
// transitions on its own, so the row lands on the next /api/mcp/status read (the
// MCP page's own load, or any sibling server connecting in the same
// notification). signalChange still fires so the steering doc regenerates.
func (r *mcpRegistry) recordDisabled(ctx context.Context, name string) {
	origin, ok := r.originFor(ctx, name)
	if !ok || origin == api.OriginUser {
		return
	}
	r.mu.Lock()
	r.servers[name] = &mcpServerRuntime{
		Name:   name,
		State:  mcpStateDisabled,
		Origin: origin,
	}
	r.mu.Unlock()
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

// originFor answers both questions a record path has about a name at once: may
// this frame be recorded, and where did the server come from.
//
// It replaces a plain enabled check, and the narrowing is the whole point. That
// check dropped every name outside EnabledNames, which conflated two opposite
// situations: a server the user switched off (a stale frame, correctly dropped —
// recording one would render a server the user disabled mid-session as
// connected) and a server vibekit never configured at all. The second is not a
// disabled server; it is an integration reaching the agent through a Power or a
// config vibekit does not own, and dropping its frames left its tools in the
// agent's tool list while the MCP page said nothing about where they came from.
//
// So exactly one case still returns false: the name is in vibekit's config and
// not enabled. Everything else is recorded, tagged with the origin the caller
// stamps on the record.
//
// A nil mcpConfig (test hubs) reports OriginUser for every name, which keeps the
// pre-existing "no config means record it" behaviour and keeps recordDisabled's
// drop rule intact for those hubs.
func (r *mcpRegistry) originFor(ctx context.Context, name string) (api.Origin, bool) {
	cfg := r.hub.mcpConfig
	if cfg == nil {
		return api.OriginUser, true
	}
	if _, ok := cfg.EnabledNames(ctx)[name]; ok {
		return api.OriginUser, true
	}
	if _, ok := cfg.ConfiguredNames(ctx)[name]; ok {
		return "", false
	}
	// Past ConfiguredNames, membership in AllNames can only come from the
	// config file's powers block. This is the only branch that reads the file,
	// so a configured server's frame never touches the disk.
	if _, ok := cfg.AllNames(ctx)[name]; ok {
		return api.OriginPower, true
	}
	return api.OriginUnknown, true
}

// statusServer is the JSON shape for /api/mcp/status entries.
// mcpStatusResponse is the typed response for the MCP status endpoint.
type mcpStatusResponse struct {
	Servers []statusServer `json:"servers"`
}

// statusServer is the JSON projection of one mcpServerRuntime. The field ORDER
// must match that struct: handleStatus converts between them directly, so a
// reorder here is a compile error rather than a silent field swap.
type statusServer struct {
	Name  string             `json:"name"`
	State api.MCPServerState `json:"state"`
	// Origin is where the server came from. Always sent (not omitempty): the
	// client withholds edit affordances on anything but "user", and an absent
	// field would make it guess.
	Origin   api.Origin `json:"origin"`
	OAuthURL string     `json:"oauth_url,omitempty"`
	Error    string     `json:"error,omitempty"`
	// Tools is the connected server's tool names. The per-tool deny editor reads
	// them from here to offer suggestions; they were a persisted config field
	// (`known_tools`) until the config file became KAS's.
	Tools     []string              `json:"tools,omitempty"`
	Prompts   []api.MCPPromptInfo   `json:"prompts,omitempty"`
	Resources []api.MCPResourceInfo `json:"resources,omitempty"`
}

func (r *mcpRegistry) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := r.Snapshot()
	out := make([]statusServer, len(snap))
	for i := range snap {
		out[i] = statusServer(snap[i])
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
