package agent

// Live MCP control: reconnect a wedged server, and resolve MCP
// prompts/resources on demand, via the v3 (KAS) _kiro/mcp/* C→A requests.
//
// vibekit's MCP config is global, but each chat bridge is a separate
// kiro-cli acp process with its own MCP pool. reconnect fans out to ALL
// live bridges, since a wedged/expired-OAuth server is wedged per-pool.
// getPrompt/getResource are equivalent reads against any connected pool, so
// they target one live bridge and return errNoLiveBridge otherwise.
//
// _kiro/mcp/toggle is deliberately NOT wired: it is a GLOBAL notification
// with no serverName, so it cannot enable/disable a specific server and
// doesn't map onto vibekit's per-server enabled flag.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp/v2"
)

const (
	// mcpReconnectTimeout bounds a single bridge's resetServer round-trip
	// (may include an OAuth redirect). bridge.Call has no timeout of its own.
	mcpReconnectTimeout = 90 * time.Second
	// mcpFetchTimeout bounds a getPrompt / getResource round-trip.
	mcpFetchTimeout = 30 * time.Second
)

// errNoLiveBridge signals that no chat bridge is running, so a live MCP
// pool operation (getPrompt / getResource) can't be served.
var errNoLiveBridge = errors.New("no active chat session")

// keyServerName is the wire key naming the target MCP server in the
// _kiro/mcp/* request params.
const keyServerName = "serverName"

// keyExitCode is the shared wire key for a process exit code (agent
// terminals + hook command runs).
const keyExitCode = "exitCode"

// firstLiveBridge returns any live bridge, or nil when none are running.
// All live bridges load the same global MCP config, so for reads any one
// is equivalent.
func (reg *mcpRegistry) firstLiveBridge() *sharedBridge {
	for _, sb := range reg.bridges.all() {
		return sb
	}
	return nil
}

// serverEnabled reports whether name is a currently-enabled configured
// server. A nil mcpConfig (test hubs) treats every non-empty name as valid.
func (reg *mcpRegistry) serverEnabled(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}
	if reg.config == nil {
		return true
	}
	_, ok := reg.config.EnabledNames(ctx)[name]
	return ok
}

// reconnectServer sends _kiro/mcp/resetServer{serverName} to every live
// bridge concurrently and returns the number of bridges targeted. Per-bridge
// errors are logged, not fatal — a wedged bridge must not block the others.
func (reg *mcpRegistry) reconnectServer(ctx context.Context, name string) int {
	bridges := reg.bridges.all()
	if len(bridges) == 0 {
		return 0
	}
	cctx, cancel := context.WithTimeout(ctx, mcpReconnectTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for chatID, sb := range bridges {
		wg.Go(func() {
			if _, err := sb.bridge.Call(cctx, methodV3MCPResetServer, map[string]any{keyServerName: name}); err != nil {
				slog.Warn("mcp reconnect: bridge call failed",
					"chat_id", chatID, "server", name, "error", err)
			}
		})
	}
	wg.Wait()
	return len(bridges)
}

// promptFor resolves an MCP prompt via a live bridge's pool. args is always
// sent as an object (never null) so servers with no arguments still parse.
func (reg *mcpRegistry) promptFor(ctx context.Context, server, promptName string, args map[string]any) (json.RawMessage, error) {
	if args == nil {
		args = map[string]any{}
	}
	return reg.fetch(ctx, methodV3MCPGetPrompt, map[string]any{
		keyServerName: server,
		"promptName":  promptName,
		"arguments":   args,
	})
}

// resourceFor reads an MCP resource via a live bridge's pool.
func (reg *mcpRegistry) resourceFor(ctx context.Context, server, uri string) (json.RawMessage, error) {
	return reg.fetch(ctx, methodV3MCPGetResource, map[string]any{
		keyServerName: server,
		"uri":         uri,
	})
}

// fetch runs one C→A request against the first live bridge and returns
// its raw result. errNoLiveBridge when nothing is running.
func (reg *mcpRegistry) fetch(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	sb := reg.firstLiveBridge()
	if sb == nil {
		return nil, errNoLiveBridge
	}
	cctx, cancel := context.WithTimeout(ctx, mcpFetchTimeout)
	defer cancel()
	resp, err := sb.bridge.Call(cctx, method, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("empty MCP response")
	}
	return resp.Result, nil
}

// --- HTTP handlers (registered by mcpRegistry.RegisterRoutes) ---

type mcpReconnectReq struct {
	Server string `json:"server"`
}

// handleReconnect: POST /api/mcp/reconnect {server} → reconnect the named
// server on every live bridge. Returns {"reconnected": N} (N = bridges
// targeted; 0 when no chat is live).
func (reg *mcpRegistry) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpReconnectReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if !reg.serverEnabled(r.Context(), body.Server) {
		httpreply.NotFound(w, "unknown or disabled MCP server")
		return
	}
	n := reg.reconnectServer(r.Context(), body.Server)
	webhttp.WriteJSON(w, map[string]int{"reconnected": n})
}

type mcpGetPromptReq struct {
	Arguments map[string]any `json:"arguments"`
	Server    string         `json:"server"`
	Prompt    string         `json:"prompt"`
}

// handlePrompt: POST /api/mcp/prompt {server, prompt, arguments} →
// the raw MCP prompt result ({messages:[...]}).
func (reg *mcpRegistry) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpGetPromptReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if body.Prompt == "" {
		httpreply.BadRequest(w, "prompt required")
		return
	}
	if !reg.serverEnabled(r.Context(), body.Server) {
		httpreply.NotFound(w, "unknown or disabled MCP server")
		return
	}
	res, err := reg.promptFor(r.Context(), body.Server, body.Prompt, body.Arguments)
	if err != nil {
		writeFetchErr(w, err)
		return
	}
	writeMCPResult(w, res)
}

type mcpGetResourceReq struct {
	Server string `json:"server"`
	URI    string `json:"uri"`
}

// handleResource: POST /api/mcp/resource {server, uri} → the raw MCP
// resource result ({contents:[...]}).
func (reg *mcpRegistry) handleResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body mcpGetResourceReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	if body.URI == "" {
		httpreply.BadRequest(w, "uri required")
		return
	}
	if !reg.serverEnabled(r.Context(), body.Server) {
		httpreply.NotFound(w, "unknown or disabled MCP server")
		return
	}
	res, err := reg.resourceFor(r.Context(), body.Server, body.URI)
	if err != nil {
		writeFetchErr(w, err)
		return
	}
	writeMCPResult(w, res)
}

// writeMCPResult writes the raw MCP result verbatim, falling back to an
// empty object when the server returned nothing (so the client always
// decodes a valid object).
func writeMCPResult(w http.ResponseWriter, res json.RawMessage) {
	if len(res) == 0 {
		res = json.RawMessage("{}")
	}
	webhttp.WriteJSON(w, res)
}

// writeFetchErr maps a getPrompt/getResource failure to an HTTP status.
// errNoLiveBridge → 409 (open a chat first); any other error → 502 with a
// generic message (details logged, not leaked).
func writeFetchErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoLiveBridge) {
		httpreply.Conflict(w, "no active chat session — open a chat to use MCP prompts and resources")
		return
	}
	slog.Warn("mcp fetch failed", "error", err)
	webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("MCP server request failed"))
}
