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
		// _kiro/userInput (a request with an id, 2.14+): the agent's
		// structured question — gated on the _meta.kiro.userInput
		// initialize capability declared in bridge.go.
		api.MethodKiroUserInput: h.translator.HandleUserInput,
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

	// A REPLAYED frame is stored history, not something happening now, and
	// must not reach the live handlers. KAS replays a session's whole
	// transcript on `session/load` — which vibekit calls on every
	// container-restart resume and every model-switch fallback — as ordinary
	// session/update notifications tagged `_meta.kiro.replay`.
	//
	// Measured against kiro-cli 2.16.0: a load of a one-turn session returns
	// 9 frames, 6 of them replay-tagged. Ungated, the replayed
	// agent_message_chunk runs ensureTurnStarted and opens a PHANTOM turn —
	// a fresh message id whose `message_created` + `message_chunk` events go
	// out to every connected client, re-streaming history as though the agent
	// were typing it. There is no `session/prompt` response to end that turn,
	// so it never flushes to the chat file — but every connected client has
	// already rendered the duplicate, and before the .partial sidecar was
	// deleted, boot recovery could promote it to a real message.
	//
	// The frames are no longer dropped: they BUILD the transcript, via a
	// per-chat translate.Projection opened for the load (load_projection.go).
	//
	// Three facts about that path are worth keeping here, at the seam:
	//
	//  1. Completion cannot be decided by the goroutine that issued the load.
	//     session/load runs inside bridge.Start, which blocks on the result,
	//     while these frames arrive on Forward. They precede the result on the
	//     wire, so when Start returns they are all PUSHED — but notifCh is
	//     buffered, so not necessarily DRAINED. Forward settles instead, on
	//     loadDone && len(NotifCh()) == 0.
	//  2. The projected transcript MERGES rather than replaces. Assistant ids
	//     differ between vibekit's record and the wire, event messages exist
	//     only in vibekit's, and a turn newer than the replay's window may be
	//     one KAS never flushed. See mergeProjection.
	//  3. The gate is PER-FRAME, not per-load. available_commands_update and
	//     config_option_update arrive untagged during a load because they carry
	//     current state, not history, and must keep reaching the live handlers.
	if base.Meta.Kiro.Replay {
		// A load in flight consumes the frame into its projection; anything
		// else is dropped, because a replay frame with no load to belong to has
		// no transcript to build.
		if !h.ingestReplayFrame(chatID, base.Kind, env.Params.Update) {
			slog.Debug("session/update: dropping replayed frame, no load in flight",
				"chat_id", chatID, "kind", base.Kind)
		}
		return
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
