// Package command implements the POST /api/command dispatch table.
// Hub registers concrete handler functions; the Dispatcher routes
// incoming commands by type and handles envelope-level concerns
// (body parsing, idempotency, validation).
package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp"
)

// maxCommandBody caps the whole POST /api/command envelope. It is the generic
// webhttp.MaxJSONBody: the largest payload this endpoint carries is a prompt's text
// at maxPromptBytes (512 KiB) plus path-only attachment metadata, so 1 MiB is
// ~2x headroom. It was 5 MiB to fit a 4 MiB user-merged partial-write payload,
// and that command is gone — KAS decides per action, so there is no merged text
// to post.
const maxCommandBody = webhttp.MaxJSONBody

// Handler is the signature for a command handler function.
type Handler func(ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand)

// Dependencies defines what the Dispatcher needs from its host (Hub).
// Composed from the role-based sub-interfaces declared in interfaces.go.
type Dependencies interface {
	BridgeAccess
	ChatAccess
	PendingPermAccess
	TerminalAccess
	WorkspaceAccess
	LifecycleAccess
	MCPAccess
	TurnOutcomeAccess
	Draining() bool
	CheckDedup(reqID string) ([]byte, bool)
	RecordDedup(reqID string, result []byte)
}

// Bridge is the per-chat ACP bridge contract for command handlers.
// The canonical definition lives in api.CommandBridge; this alias
// preserves backward compatibility within the command package.
type Bridge = api.CommandBridge

// Dispatcher holds the command dispatch table and serves the
// POST /api/command HTTP endpoint.
type Dispatcher struct {
	deps     Dependencies
	handlers map[api.CommandType]Handler
	mu       sync.RWMutex
}

// New constructs a Dispatcher with the given dependencies.
func New(deps Dependencies) *Dispatcher {
	return &Dispatcher{
		deps:     deps,
		handlers: make(map[api.CommandType]Handler),
	}
}

// Deps returns the dependencies for use by handler functions.
func (d *Dispatcher) Deps() Dependencies { return d.deps }

// Register adds a handler for the given command type.
func (d *Dispatcher) Register(t api.CommandType, h Handler) {
	d.mu.Lock()
	d.handlers[t] = h
	d.mu.Unlock()
}

// Respond writes a JSON body and caches it for request_id idempotency.
func (d *Dispatcher) Respond(w http.ResponseWriter, reqID string, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		slog.Error("respond marshal", keyError, err)
		httpreply.InternalError(w, err)
		return
	}
	d.deps.RecordDedup(reqID, data)
	httpreply.WriteRawJSON(w, data)
}

// RespondOK writes the standard {"ok":true} success response and
// caches it for idempotent replay. Shorthand for the most common
// command success case.
func (d *Dispatcher) RespondOK(w http.ResponseWriter, reqID string) {
	d.Respond(w, reqID, responseOK)
}

// errorResponse is the typed wire shape for JSON error responses.
// Using a struct instead of map[string]string makes the shape explicit,
// enables json.Marshal's cached struct encoder, and provides a single
// place to add future fields (e.g. code, retryable).
type errorResponse struct {
	Error string `json:"error"`
}

// RespondErr writes a JSON error response with the given status code.
//
// The body goes through api.RPCErrorText rather than err.Error() because four of
// the handlers reaching here (compact, mode, rewind, steer) forward a bridge Call
// failure verbatim, and on a -32603 the error string is KAS's literal "Internal
// error" while the cause sits unread in `error.data`. RPCErrorText is a no-op for
// every ordinary Go error, so the one call covers both populations.
func (d *Dispatcher) RespondErr(w http.ResponseWriter, code int, err error) {
	webhttp.WriteJSONStatus(w, code, errorResponse{Error: api.RPCErrorText(err)})
}

// RequireChatID validates that cmd.ChatID is non-empty and writes a
// 400 response if not. Returns true when the chat ID is present.
func (d *Dispatcher) RequireChatID(w http.ResponseWriter, cmd *api.ClientCommand) bool {
	if cmd.ChatID == "" {
		d.RespondErr(w, http.StatusBadRequest, ErrMissingChatID)
		return false
	}
	return true
}

// ServeHTTP is the POST /api/command HTTP handler.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	if d.deps.Draining() {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, errorResponse{Error: "shutting down"})
		return
	}

	webhttp.LimitBody(w, r, maxCommandBody)
	var cmd api.ClientCommand
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("command body too large",
				"limit", maxCommandBody, keyError, maxErr)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				errorResponse{Error: "request body too large"})
			return
		}
		httpreply.BadRequest(w, "invalid json")
		return
	}

	if !validRequestID(cmd.RequestID) {
		httpreply.BadRequest(w, "invalid request_id")
		return
	}

	// Idempotent retries: same request_id → cached response.
	if cached, ok := d.deps.CheckDedup(cmd.RequestID); ok {
		slog.Debug("idempotent replay", "request_id", cmd.RequestID, keyType, cmd.Type)
		httpreply.WriteRawJSON(w, cached)
		return
	}

	// Centralised chat_id validation.
	if cmd.ChatID != "" && !validChatID(cmd.ChatID) {
		httpreply.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}

	d.mu.RLock()
	fn, ok := d.handlers[cmd.Type]
	d.mu.RUnlock()
	if ok {
		fn(r.Context(), w, &cmd)
	} else {
		httpreply.BadRequest(w, "unknown command: "+string(cmd.Type))
	}
}

// SessionParams builds the base ACP parameter map with the "sessionId"
// key set from the bridge. Extra key-value pairs from extra maps are
// merged in (last-wins).
func SessionParams(b Bridge, extra ...map[string]any) map[string]any {
	m := map[string]any{keySessionID: b.SessionID()}
	for _, e := range extra {
		maps.Copy(m, e)
	}
	return m
}
