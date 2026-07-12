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
//   - Unhandled `_kiro/*` extensions fall through to a debug log,
//     not a panic or a silent drop. KAS's extension namespace is
//     explicitly unstable; we discover new surfaces without committing
//     to decode them.
//   - ACP-spec methods (no `_kiro/` prefix) are expected to be
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
		// _kiro/mcp/elicitation (a request with an id). Routed here by method.
		api.MethodElicitationCreate: h.translator.HandleElicitationCreate,
		// v3 (KAS) _kiro/* notifications.
		methodV3RateLimit:            h.translator.HandleRateLimit,
		methodV3CustomAgentNotFound:  h.translator.HandleAgentNotFound,
		methodV3CustomAgentConfigErr: h.translator.HandleAgentConfigError,
		methodV3MCPStatus:            h.translator.HandleMCPStatus,
		methodV3SystemNotify:         h.translator.HandleSystemNotify,
		// Native Cedar policy: hot-reload + parse-error notifications KAS
		// emits when a permissions.{yaml,json} file changes. Translated to
		// SSE so the client refetches GET /api/permissions.
		methodV3PolicyChanged: h.translator.HandlePolicyChanged,
		methodV3PolicyError:   h.translator.HandlePolicyError,
		// Licensed-code attribution: surfaced as a per-turn attribution chip.
		methodV3CodeReferences: h.translator.HandleCodeReferences,
		// Spec-workflow task-status deltas → the live Specs board SSE.
		methodV3SpecTaskStatusChanged: h.translator.HandleSpecTaskChanged,
		// Infrastructure-Safety gate state → safety_status / safety_properties
		// SSE. Defensive: KAS only emits these when the gate is installed (client
		// capability + an AWS governance flag that is off by default), so on a
		// normal account they never fire. See translate/safety.go.
		methodV3SafetyStatusChanged: h.translator.HandleSafetyStatusChanged,
		methodV3SafetyPropertiesChg: h.translator.HandleSafetyPropertiesChanged,
		// Org/account feature-flag policy → governance_state SSE (broadcast
		// global) + hub-side cache served at GET /api/governance. Gates
		// affordances the flags control (MCP availability, the org-policy
		// disclosure, the code-reference chip). See translate/governance.go.
		methodV3Governance: h.translator.HandleGovernanceState,
		// Knowledge-base indexing progress → knowledge_indexing SSE. Two
		// methods share one handler (a started/completed bool discriminates
		// the payload). Fire only for agent-declared knowledge_bases sync;
		// user-add progress is polled via GET /api/knowledge.
		methodKiroKnowledgeIndexingStarted: func(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
			h.translator.HandleKnowledgeIndexing(ctx, chatID, msg, true)
		},
		methodKiroKnowledgeIndexingCompleted: func(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
			h.translator.HandleKnowledgeIndexing(ctx, chatID, msg, false)
		},
	}
	// Explicit noops: v3 methods we recognise but intentionally ignore
	// (feature flags, tool/steering/skills catalogs vibekit sources via
	// REST, and the session inventory diff which has no client consumer now
	// that subagents are tool calls). Listing them keeps them out of the
	// "unhandled" debug log.
	h.noopMethods = map[string]struct{}{
		methodV3SessionsChanged:    {},
		methodV3ToolsDidChange:     {},
		methodV3SteeringDocs:       {},
		methodV3ProgressiveContext: {},
		methodV3Powers:             {},
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
		// v3 (KAS) sub-kinds: context-usage + slash-command catalog moved
		// here from the v2 _kiro.dev/metadata + commands/available notifs.
		// session_info_update also carries compaction (summarization) state;
		// usage_update is the primary v3 context-usage channel; and
		// config_option_update delivers the live model/mode/effort catalog.
		api.ACPUpdateSessionInfo:       h.translator.HandleSessionInfoUpdate,
		api.ACPUpdateCommandsAvailable: h.translator.HandleAvailableCommandsUpdate,
		api.ACPUpdateUsage:             ignoreSubSession(h.translator.HandleUsageUpdate),
		api.ACPUpdateConfigOption:      ignoreSubSession(h.translator.HandleConfigOptionUpdate),
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
	// v3 (KAS) host-mediated client requests (_kiro/auth/getAccessToken,
	// _kiro/terminal/shell_type). No-op under v1/v2.
	if msg.ID != nil && h.handleKiroClientRequest(ctx, chatID, msg) {
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
	// v3 (KAS) emits the _kiro/* extension namespace. Unhandled surfaces
	// (Cedar policy, spec/hooks/knowledge/safety/sandbox families, etc.)
	// fall through to a debug log rather than a silent drop.
	if strings.HasPrefix(msg.Method, "_kiro/") {
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

	// Sub-kinds without a handler fall through silently. user_message_chunk
	// is intentionally one of them: vibekit persists user messages itself
	// (cmdPrompt echoes the bubble via message_appended before the turn
	// starts), so consuming KAS's echo of the prompt would double-render it.
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
