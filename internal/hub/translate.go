// ACP → domain event translation (dispatcher + session-update sub-dispatcher).
//
// KIRO-CLI 2.0.1 tui.js:886710603bed3fb6 — payload shapes pinned.
//
// kiro-cli sends a mix of JSON-RPC notifications, requests (that need
// a response), and method-qualified envelopes. `translateACPEvent` is
// the single dispatch point — every per-method handler lives in a
// sibling `translate_*.go` file so this one stays short and readable.
//
// Design rules:
//   - Unhandled `_kiro.dev/*` extensions fall through to a debug log,
//     not a panic or a silent drop. kiro-cli's extension namespace is
//     explicitly unstable; we discover new surfaces without committing
//     to decode them.
//   - ACP-spec methods (no `_kiro.dev` prefix) are expected to be
//     stable; unknown ones log at the same debug level but that's a
//     stronger signal something needs wiring.
//   - Requests (msg.ID != nil && msg.Method != "") route through the
//     fs handlers first; the rest of the dispatcher only cares about
//     notifications.

package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/translate"
)

// chatHandler is the unified notification handler type. All handlers
// receive ctx, chatID, and msg. Global handlers (MCP notifications)
// receive an empty chatID.
type chatHandler = func(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse)

// ignoreSubSession adapts a 3-arg handler (ctx, chatID, raw) to the
// 4-arg sessionUpdateHandler signature by discarding the subSessionID.
// Eliminates repeated anonymous closure boilerplate in the dispatch table.
func ignoreSubSession(fn func(context.Context, api.ChatID, json.RawMessage)) sessionUpdateHandler {
	return func(ctx context.Context, chatID api.ChatID, raw json.RawMessage, _ string) {
		fn(ctx, chatID, raw)
	}
}

// initDispatch builds the method → handler maps. Called once from
// translateACPEvent on first use (lazy init avoids a constructor).
func (h *Hub) initDispatch() {
	h.chatHandlers = map[string]chatHandler{
		api.MethodSessionUpdate:     h.handleSessionUpdate,
		api.MethodRequestPermission: h.translator.HandlePermissionRequest,
		api.MethodElicitationCreate:   h.translator.HandleElicitationCreate,
		api.MethodElicitationComplete: h.translator.HandleElicitationComplete,
		methodMetadataLegacy:        h.translator.HandleMetadata,
		methodMetadata:              h.translator.HandleMetadata,
		methodCommandsAvailable:     h.translator.HandleCommandsAvailable,
		methodCompactionStatus:      h.translator.HandleCompactionStatus,
		methodSubagentListUpdate:    h.translator.HandleCrewUpdate,
		methodAgentNotFound:         h.translator.HandleAgentNotFound,
		methodAgentConfigError:      h.translator.HandleAgentConfigError,
		methodModelNotFound:         h.translator.HandleModelNotFound,
		methodErrorRateLimit:        h.translator.HandleRateLimit,
		methodAgentSwitched:         h.translator.HandleAgentSwitched,
		methodSessionActivity:       h.translator.HandleSessionActivity,
		methodSessionListUpdate:     h.translator.HandleSessionListUpdate,
		methodInboxNotification:     h.translator.HandleInboxNotification,
		methodExtSessionUpdate:      h.translator.HandleExtSessionUpdate,
		methodSessionRetry:          h.translator.HandleSessionRetry,
		// Global handlers (chatID is always "" for these).
		methodMCPServerInitialized: h.translator.HandleMCPInitialized,
		methodMCPServerInitFailure: h.translator.HandleMCPInitFailure,
		methodMCPOAuthRequest:      h.translator.HandleMCPOAuth,
	}
	// Explicit noops: methods we recognise but intentionally ignore.
	h.noopMethods = map[string]struct{}{
		methodClearStatus: {},
	}
	// Session-update sub-dispatcher: built eagerly to avoid a data race
	// when multiple bridge goroutines call sessionUpdateHandlers() concurrently.
	h.sessUpdateHandlers = map[api.ACPUpdateKind]sessionUpdateHandler{
		api.ACPUpdateAgentChunk: ignoreSubSession(func(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
			h.translator.HandleAssistantChunk(ctx, chatID, raw, false)
		}),
		api.ACPUpdateThoughtChunk: ignoreSubSession(func(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
			h.translator.HandleAssistantChunk(ctx, chatID, raw, true)
		}),
		api.ACPUpdateToolCall:   h.translator.HandleToolCall,
		api.ACPUpdateToolUpdate: h.translator.HandleToolCallUpdate,
		api.ACPUpdatePlan:       ignoreSubSession(h.translator.HandlePlan),
		api.ACPUpdateModeChange: ignoreSubSession(h.translator.HandleModeUpdate),
		api.ACPUpdateSteering:   ignoreSubSession(h.translator.HandleSteeringInclusion),
	}
}

// translateACPEvent is the sole entry point from bridge_lifecycle's
// forward goroutine. Every branch must return promptly; long-running
// work belongs in goroutines inside the handler (see bridge_fs.go).
func (h *Hub) translateACPEvent(chatID api.ChatID, msg *api.RPCResponse) {
	// Derive a context from the hub's shutdownCtx so handlers can
	// propagate shutdown cancellation to I/O calls.
	ctx, cancel := h.hubContext()
	defer cancel()

	if msg.ID != nil && h.handleFSRequest(ctx, chatID, msg) {
		return
	}
	// Terminal requests from kiro-cli (terminal/create, terminal/output, etc.)
	if msg.ID != nil && strings.HasPrefix(msg.Method, methodTermPrefix) {
		h.handleTerminalRequest(ctx, chatID, msg.Method, msg)
		return
	}

	if fn, ok := h.chatHandlers[msg.Method]; ok {
		fn(ctx, chatID, msg)
		return
	}
	if _, ok := h.noopMethods[msg.Method]; ok {
		return
	}
	if strings.HasPrefix(msg.Method, "_kiro.dev/") {
		slog.Debug("unhandled kiro extension",
			"method", msg.Method, "chat_id", chatID)
	}
}

// --- Session-update sub-dispatcher ---

// handleSessionUpdate decodes the `update` envelope and fans out to
// the sub-handler for each sessionUpdate subtype. The top-level
// params.sessionId identifies whether this notification belongs to the
// parent chat or a subagent; we pass it through so tool-call handlers
// can set SubSessionID on emitted events.
func (h *Hub) handleSessionUpdate(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var env struct {
		Params translate.ACPSessionUpdateEnvelope `json:"params"`
	}
	if json.Unmarshal(msg.Params, &env.Params) != nil || env.Params.Update == nil {
		return
	}
	var base translate.ACPSessionUpdateBase
	if json.Unmarshal(env.Params.Update, &base) != nil {
		return
	}

	// Determine subagent attribution. Empty or matching parent = parent.
	subSessionID := ""
	parent := h.parentACPSession(chatID)
	if env.Params.SessionID != "" && parent != "" && env.Params.SessionID != parent {
		subSessionID = env.Params.SessionID
	}

	fn, ok := h.sessionUpdateHandlers()[base.Kind]
	if !ok {
		return
	}
	fn(ctx, chatID, env.Params.Update, subSessionID)
}

// sessionUpdateHandler is the common signature for session-update sub-handlers.
type sessionUpdateHandler = func(ctx context.Context, chatID api.ChatID, raw json.RawMessage, subSessionID string)

// sessionUpdateHandlers returns the map of sessionUpdate kind → handler.
// The map is built eagerly in initDispatch and cached on the Hub.
func (h *Hub) sessionUpdateHandlers() map[api.ACPUpdateKind]sessionUpdateHandler {
	return h.sessUpdateHandlers
}
