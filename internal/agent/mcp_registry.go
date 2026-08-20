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

package agent

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp"
)

// mcpServerState is an alias for the vibekit-level MCPServerState enum.
type mcpServerState = vibekit.MCPServerState

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
	Origin    vibekit.Origin
	OAuthURL  string
	Error     string
	Tools     []string
	Prompts   []vibekit.MCPPromptInfo
	Resources []vibekit.MCPResourceInfo
	// Relayed is the relay's single-use latch for THIS authorization attempt
	// (mcp_oauth_relay.go). It is set the moment a relay RESERVES the attempt,
	// not after the callback lands, and it stays set once the loopback listener
	// accepts it — so a resubmitted address gets a legible "already relayed"
	// answer instead of a confusing gateway error from a code the provider has
	// since spent. A relay that did not deliver gives the reservation back, so a
	// corrected paste stays possible.
	//
	// Reserving IS the latch rather than a second in-flight flag: no consumer can
	// distinguish the two states, and a concurrent second paste has to be refused
	// in both, so one field carries both meanings. Only beginOAuthRelay and
	// releaseOAuthRelay write it, and it resets with the record on the next
	// recordOAuth.
	Relayed bool
}

// mcpRegistry is the in-memory view of connected MCP servers, and the whole MCP
// surface: the status record, the HTTP routes over it, and the reconnect/prompt/
// resource calls that go to a live CHAT bridge.
//
// It held a raw *Runtime, which was the strongest cycle in the package: the
// registry reached back for three HTTP handlers, four broadcasts, the runtime's
// own mcpConfig field and its done channel — while ten of those handlers' methods
// sat on the runtime reaching forward into the registry. Two halves of one
// concept, each holding the other. Merging them deletes the back-pointer; what is
// left is three named collaborators.
type mcpRegistry struct {
	// bridges looks up a chat's live bridge. Reconnect and prompt-fetch need a
	// CHAT bridge, not the utility one, which is why this surface is not part of
	// Settings even though it looks like configuration.
	bridges *bridgeManager
	// bus publishes the four MCP lifecycle events.
	bus *bus
	// lifetime supplies the done channel the debounce loop exits on.
	lifetime *lifetime
	// config is the enabled/known name sets vibekit itself configured. Optional:
	// WithMCPConfig is not passed in tests, and every reader nil-checks — a
	// registry with no config classifies every server as unconfigured, which is
	// the right answer when vibekit configured none.
	config  mcpNameSets `wiring:"optional"`
	servers map[string]*mcpServerRuntime
	// onChange is installed later by SetMCPOnChange from the composition root, so
	// it is optional at construction by design rather than by omission.
	onChange func() `wiring:"optional"`
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

func newMCPRegistry(bridges *bridgeManager, b *bus, lt *lifetime, cfg mcpNameSets) *mcpRegistry {
	r := &mcpRegistry{
		bridges:  bridges,
		bus:      b,
		lifetime: lt,
		config:   cfg,
		servers:  make(map[string]*mcpServerRuntime),
		notifyCh: make(chan struct{}, 1),
		readyCh:  make(chan struct{}),
	}
	return r
}

// SetOnChange wires an invalidation callback fired (outside the lock)
// whenever the registry mutates. The steering generator subscribes here.
// Starts the debounced notifier goroutine on first call.
func (reg *mcpRegistry) SetOnChange(fn func()) {
	reg.mu.Lock()
	first := reg.onChange == nil && fn != nil
	reg.onChange = fn
	reg.mu.Unlock()
	if first {
		reg.startNotifier()
	}
}

// WaitForReady blocks until MCP servers have reported their status
// (via commands/available) or the timeout expires. Returns true if
// ready, false on timeout or context cancellation.
func (reg *mcpRegistry) WaitForReady(ctx context.Context, timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-reg.readyCh:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Snapshot returns a deep copy of the current registry state, sorted
// by server name for stable output.
func (reg *mcpRegistry) Snapshot() []mcpServerRuntime {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]mcpServerRuntime, 0, len(reg.servers))
	for _, v := range reg.servers {
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
func (reg *mcpRegistry) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/mcp/status", reg.handleStatus)
	mux.HandleFunc("/api/mcp/reconnect", reg.handleReconnect)
	mux.HandleFunc("/api/mcp/prompt", reg.handlePrompt)
	mux.HandleFunc("/api/mcp/resource", reg.handleResource)
	// The OAuth loopback relay (mcp_oauth_relay.go). Registering it here is
	// mandatory, not optional: internal/mcp's "/api/mcp/" SUBTREE handler would
	// otherwise swallow this path and read "oauth-relay" as a server id, so an
	// unregistered relay 404s as an unknown server rather than failing visibly.
	mux.HandleFunc("/api/mcp/oauth-relay", reg.handleOAuthRelay)
}

// signalReady closes the readyCh so any goroutine waiting in
// WaitForReady unblocks. Called when the first commands/available
// notification arrives. Safe to call multiple times.
func (reg *mcpRegistry) SignalReady() {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	select {
	case <-reg.readyCh:
		// already closed
	default:
		close(reg.readyCh)
	}
}

// recordConnected marks a server as connected (recording the prompts and
// resources it advertises), broadcasts mcp_connected, and fires onChange.
// Called from the _kiro/mcp/status handler when a server reports the
// "connected" state. prompts/resources may be nil (server exposes none).
func (reg *mcpRegistry) RecordConnected(ctx context.Context, name string, tools []string, prompts []vibekit.MCPPromptInfo, resources []vibekit.MCPResourceInfo) {
	origin, ok := reg.originFor(ctx, name)
	if !ok {
		return
	}
	reg.mu.Lock()
	reg.servers[name] = &mcpServerRuntime{
		Name:      name,
		State:     mcpStateConnected,
		Origin:    origin,
		Tools:     tools,
		Prompts:   prompts,
		Resources: resources,
	}
	reg.mu.Unlock()

	reg.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMCPConnected, "", vibekit.MCPConnectedPayload{Server: name}))
	reg.signalChange()
}

// recordOAuth marks a server as waiting for OAuth and broadcasts the URL.
func (reg *mcpRegistry) RecordOAuth(ctx context.Context, name, url string) {
	origin, ok := reg.originFor(ctx, name)
	if !ok {
		return
	}
	reg.mu.Lock()
	reg.servers[name] = &mcpServerRuntime{
		Name:     name,
		State:    mcpStateOAuth,
		Origin:   origin,
		OAuthURL: url,
	}
	reg.mu.Unlock()

	reg.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMCPOAuthNeeded, "", vibekit.MCPOAuthPayload{Server: name, URL: url}))
	reg.signalChange()
}

// recordInitFailure marks a server as having failed initialisation.
// Broadcast mcp_failed so the client can render a red status and
// surface the error message. A server the user disabled is silently dropped —
// they have already chosen not to run it.
func (reg *mcpRegistry) RecordInitFailure(ctx context.Context, name, errMsg string) {
	origin, ok := reg.originFor(ctx, name)
	if !ok {
		return
	}
	reg.mu.Lock()
	reg.servers[name] = &mcpServerRuntime{
		Name:   name,
		State:  mcpStateFailed,
		Origin: origin,
		Error:  errMsg,
	}
	reg.mu.Unlock()

	reg.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMCPFailed, "", vibekit.MCPFailedPayload{Server: name, Error: errMsg}))
	reg.signalChange()
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
func (reg *mcpRegistry) RecordDisabled(ctx context.Context, name string) {
	origin, ok := reg.originFor(ctx, name)
	if !ok || origin == vibekit.OriginUser {
		return
	}
	reg.mu.Lock()
	reg.servers[name] = &mcpServerRuntime{
		Name:   name,
		State:  mcpStateDisabled,
		Origin: origin,
	}
	reg.mu.Unlock()
	reg.signalChange()
}

// oauthAttempt is a granted reservation to relay ONE authorization attempt's
// callback. beginOAuthRelay issues it and it is the only handle that can give
// the reservation back, which is what makes the single-use guarantee an atomic
// reserve-then-complete instead of a check followed by a later, unattributed
// mark.
//
// The RECORD POINTER is the attempt's identity, and it needs no generation
// counter beside it because it cannot be confused with another attempt: every
// transition in this registry installs a FRESH mcpServerRuntime (see each
// record* method), and begin/release are the only writers that mutate a record in
// place rather than replacing it. So a token whose pointer is still the map's
// current value names the attempt it was issued for, and a token whose
// pointer was replaced names a dead one. That is what stops an old callback
// still in flight from latching the NEWER attempt recordOAuth installed while it
// was out: the pointers differ and release becomes a no-op, leaving the new
// attempt relayable rather than silently marked delivered.
//
// rec is never dereferenced outside the registry lock. authURL is copied in so
// the handler has no reason to reach through it: it is the relay's whole trust
// anchor (KAS put its own loopback `redirect_uri` and `state` in it), and
// reading it off the record later could pick up a different attempt's value.
type oauthAttempt struct {
	rec     *mcpServerRuntime
	server  string
	authURL string
}

// beginOAuthRelay reserves the relay for a server's pending authorization
// attempt, returning the attempt and the authorization URL to check the pasted
// address against.
//
// Everything the single-use rule depends on happens here under one lock: the
// server is waiting for authorization, no relay has delivered or is delivering
// its callback, and the reservation is taken. Two concurrent pastes therefore
// cannot both proceed — a double click, or a second device, gets
// errRelayAlreadyDone rather than spending the same authorization code twice.
// A server in any other state has no attempt at all and gets errRelayNoFlow,
// which is what keeps the route from being a standing lever with no flow behind
// it.
func (reg *mcpRegistry) beginOAuthRelay(name string) (oauthAttempt, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	s, exists := reg.servers[name]
	if !exists || s.State != mcpStateOAuth || s.OAuthURL == "" {
		return oauthAttempt{}, errRelayNoFlow
	}
	if s.Relayed {
		return oauthAttempt{}, errRelayAlreadyDone
	}
	s.Relayed = true
	return oauthAttempt{rec: s, server: name, authURL: s.OAuthURL}, nil
}

// releaseOAuthRelay gives back a reservation whose relay did not deliver, so a
// corrected paste can still be tried. Called on a refused paste, a transport
// failure and a listener refusal; a delivered callback keeps the reservation,
// which is what makes it the latch.
//
// It mutates nothing unless the attempt is still the current one. A record
// replaced while this relay was out belongs to a different attempt, and clearing
// ITS latch would hand a fresh attempt's reservation to whoever holds a stale
// token.
func (reg *mcpRegistry) releaseOAuthRelay(a oauthAttempt) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if a.rec != nil && reg.servers[a.server] == a.rec {
		a.rec.Relayed = false
	}
}

// clearAll wipes the runtime registry and broadcasts mcp_disconnected
// for each server that had state. Called when the last bridge exits;
// MCP subprocesses are scoped to kiro-cli, so nothing is live anymore.
func (reg *mcpRegistry) clearAll(ctx context.Context) {
	reg.mu.Lock()
	prev := reg.servers
	reg.servers = make(map[string]*mcpServerRuntime)
	reg.mu.Unlock()

	if len(prev) == 0 {
		return
	}
	for name := range prev {
		if ctx.Err() != nil {
			break
		}
		reg.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMCPDisconnected, "", vibekit.MCPDisconnectedPayload{Server: name}))
	}
	reg.signalChange()
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
func (reg *mcpRegistry) originFor(ctx context.Context, name string) (vibekit.Origin, bool) {
	cfg := reg.config
	if cfg == nil {
		return vibekit.OriginUser, true
	}
	if _, ok := cfg.EnabledNames(ctx)[name]; ok {
		return vibekit.OriginUser, true
	}
	if _, ok := cfg.ConfiguredNames(ctx)[name]; ok {
		return "", false
	}
	// Past ConfiguredNames, membership in AllNames can only come from the
	// config file's powers block. This is the only branch that reads the file,
	// so a configured server's frame never touches the disk.
	if _, ok := cfg.AllNames(ctx)[name]; ok {
		return vibekit.OriginPower, true
	}
	return vibekit.OriginUnknown, true
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
	Name  string                 `json:"name"`
	State vibekit.MCPServerState `json:"state"`
	// Origin is where the server came from. Always sent (not omitempty): the
	// client withholds edit affordances on anything but "user", and an absent
	// field would make it guess.
	Origin   vibekit.Origin `json:"origin"`
	OAuthURL string         `json:"oauth_url,omitempty"`
	Error    string         `json:"error,omitempty"`
	// Tools is the connected server's tool names. The per-tool deny editor reads
	// them from here to offer suggestions; they were a persisted config field
	// (`known_tools`) until the config file became KAS's.
	Tools     []string                  `json:"tools,omitempty"`
	Prompts   []vibekit.MCPPromptInfo   `json:"prompts,omitempty"`
	Resources []vibekit.MCPResourceInfo `json:"resources,omitempty"`
	// Relayed says this attempt's callback is on its way to the loopback listener
	// or was already delivered to it. On the wire so a reload, or a second
	// device, does not offer the paste box again for a code that has been spent.
	Relayed bool `json:"relayed,omitempty"`
}

func (reg *mcpRegistry) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := reg.Snapshot()
	out := make([]statusServer, len(snap))
	for i := range snap {
		out[i] = statusServer(snap[i])
	}
	webhttp.WriteJSON(w, mcpStatusResponse{Servers: out})
}

// startNotifier launches the single long-lived goroutine that drains
// notifyCh and calls onChange with a short debounce. Must be called
// after SetOnChange. Exits when h.lifecycle.done closes.
func (reg *mcpRegistry) startNotifier() {
	go func() {
		const debounce = 100 * time.Millisecond
		for {
			select {
			case <-reg.lifetime.done:
				return
			case <-reg.notifyCh:
			}
			// Debounce: wait a short window to coalesce rapid signals.
			t := time.NewTimer(debounce)
			select {
			case <-reg.lifetime.done:
				t.Stop()
				return
			case <-t.C:
			}
			// Drain any signals that arrived during the debounce window.
			select {
			case <-reg.notifyCh:
			default:
			}
			reg.mu.RLock()
			cb := reg.onChange
			reg.mu.RUnlock()
			if cb != nil {
				cb()
			}
		}
	}()
}

// signalChange sends a non-blocking signal to the notifier goroutine.
// Multiple calls within the debounce window collapse into one cb().
func (reg *mcpRegistry) signalChange() {
	select {
	case reg.notifyCh <- struct{}{}:
	default:
		// Already signalled; the notifier will pick it up.
	}
}
