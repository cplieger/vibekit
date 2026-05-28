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
	"time"

	"vibekit/internal/api"
	"vibekit/internal/pending"
)

// maxCommandBody caps the whole POST /api/command envelope.
const maxCommandBody = 5 * 1024 * 1024

// Handler is the signature for a command handler function.
type Handler func(ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand)

// Dependencies defines what the Dispatcher needs from its host (Hub).
type Dependencies interface {
	// CheckDedup returns a cached response for the given request ID, if any.
	CheckDedup(reqID string) ([]byte, bool)
	// RecordDedup caches a response for idempotent replay.
	RecordDedup(reqID string, result []byte)
	// Draining reports whether the server is shutting down.
	Draining() bool

	// ChatStore returns the chat persistence layer.
	ChatStore() api.ChatStore
	// Broadcast sends an SSE event to all connected clients.
	Broadcast(ctx context.Context, evt api.ServerEvent)

	// GetBridge returns the active bridge for a chat, or nil.
	GetBridge(chatID api.ChatID) Bridge
	// GetOrCreateBridge ensures a bridge exists for the chat.
	GetOrCreateBridge(ctx context.Context, chatID api.ChatID, agent, model string) (Bridge, error)
	// CloseBridge tears down the bridge for a chat.
	CloseBridge(chatID api.ChatID)

	// PendingStore returns the pending-changes store.
	PendingStore() *pending.Store
	// SupervisedSetTrust sets the per-turn trust flag for a chat.
	SupervisedSetTrust(chatID api.ChatID)
	// SupervisedClearTrust clears the per-turn trust flag.
	SupervisedClearTrust(chatID api.ChatID, reason api.ClearReason)
	// ChatInSupervisedMode reports whether the chat has supervised mode on.
	ChatInSupervisedMode(ctx context.Context, chatID api.ChatID) bool
	// FlushPendingForChat rejects all outstanding pending ops for a chat.
	FlushPendingForChat(ctx context.Context, chatID api.ChatID, reason api.ClearReason)
	// ClearPendingPermsForChat drops unresolved permission_needed entries.
	ClearPendingPermsForChat(chatID api.ChatID)
	// RemovePendingPerm removes a single pending permission by request ID.
	RemovePendingPerm(requestID int64)

	// Checkpoints returns the checkpoint service, or nil if unavailable.
	Checkpoints() api.CheckpointService
	// AdvanceCheckpointTurn bumps the checkpoint turn counter.
	AdvanceCheckpointTurn(ctx context.Context, chatID api.ChatID)

	// WorkDir returns the workspace directory.
	WorkDir() string
	// ConfigDir returns the configuration directory.
	ConfigDir() string
	// ShutdownCtx returns the context cancelled on shutdown.
	ShutdownCtx() context.Context
	// InflightAdd increments the inflight counter.
	InflightAdd(delta int)
	// InflightDone decrements the inflight counter.
	InflightDone()
	// InflightGo runs fn under the inflight WaitGroup.
	InflightGo(fn func())

	// CleanupChatState tears down all in-memory state for a chat.
	CleanupChatState(ctx context.Context, chatID api.ChatID)

	// UtilityPrompt sends a prompt to the utility bridge.
	UtilityPrompt(ctx context.Context, prompt string) (string, error)

	// MCPWaitForReady blocks until MCP servers are ready or timeout.
	MCPWaitForReady(ctx context.Context, timeout time.Duration) bool

	// ResolveInsideWorkDir validates a path is inside the workspace.
	ResolveInsideWorkDir(rel string) (string, error)

	// PrimeIfNeeded primes the bridge with history if needed.
	PrimeIfNeeded(ctx context.Context, chatID api.ChatID, b Bridge)

	// IsEmptyTurn checks if a prompt response is an empty turn.
	IsEmptyTurn(resp *api.RPCResponse, chatID api.ChatID) bool

	// EmitTurnEndedWithStats broadcasts turn_ended with usage stats.
	EmitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, creditsDelta, elapsedMs float64)
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
		api.InternalError(w, err)
		return
	}
	d.deps.RecordDedup(reqID, data)
	api.WriteRawJSON(w, data)
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
func (d *Dispatcher) RespondErr(w http.ResponseWriter, code int, err error) {
	api.WriteJSONStatus(w, code, errorResponse{Error: err.Error()})
}

// RequireChatID validates that cmd.ChatID is non-empty and writes a
// 400 response if not. Returns true when the chat ID is present.
func (d *Dispatcher) RequireChatID(w http.ResponseWriter, cmd *api.ClientCommand) bool {
	if cmd.ChatID == "" {
		d.RespondErr(w, http.StatusBadRequest, errMissingChatID)
		return false
	}
	return true
}

// ServeHTTP is the POST /api/command HTTP handler.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
		return
	}
	if d.deps.Draining() {
		api.WriteJSONStatus(w, http.StatusServiceUnavailable, errorResponse{Error: "shutting down"})
		return
	}

	api.LimitBody(w, r, maxCommandBody)
	var cmd api.ClientCommand
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("command body too large",
				"limit", maxCommandBody, keyError, maxErr)
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				errorResponse{Error: "request body too large"})
			return
		}
		api.BadRequest(w, "invalid json")
		return
	}

	if !validRequestID(cmd.RequestID) {
		api.BadRequest(w, "invalid request_id")
		return
	}

	// Idempotent retries: same request_id → cached response.
	if cached, ok := d.deps.CheckDedup(cmd.RequestID); ok {
		slog.Debug("idempotent replay", "request_id", cmd.RequestID, keyType, cmd.Type)
		api.WriteRawJSON(w, cached)
		return
	}

	// Centralised chat_id validation.
	if cmd.ChatID != "" && !validChatID(cmd.ChatID) {
		api.BadRequest(w, api.ErrMsgInvalidChatID)
		return
	}

	d.mu.RLock()
	fn, ok := d.handlers[cmd.Type]
	d.mu.RUnlock()
	if ok {
		fn(r.Context(), w, &cmd)
	} else {
		api.BadRequest(w, "unknown command: "+string(cmd.Type))
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
