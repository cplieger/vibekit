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

package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// chatHandler is the unified notification handler type. All handlers
// receive ctx, chatID, and msg. Global handlers (MCP notifications)
// receive an empty chatID.
type chatHandler = func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse)

// ignoreSubSession adapts a 3-arg handler (ctx, chatID, raw) to the
// 4-arg sessionUpdateHandler signature by discarding the subSessionID.
// Eliminates repeated anonymous closure boilerplate in the dispatch table.
func ignoreSubSession(fn func(context.Context, vibekit.ChatID, json.RawMessage)) sessionUpdateHandler {
	return func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, _ string) {
		fn(ctx, chatID, raw)
	}
}

// initDispatch builds the method → handler maps. Called once from
// translateACPEvent on first use (lazy init avoids a constructor).
func (h *Runtime) initDispatch() {
	h.chatHandlers = map[string]chatHandler{
		vibekit.MethodSessionUpdate: h.handleSessionUpdate,
		// Wrapped so a SCHEDULED run's request is refused on a short budget
		// rather than parking the run forever (run_unattended.go). The wrapper
		// is a no-op for every attended chat.
		vibekit.MethodRequestPermission: h.runs.permissionWithUnattendedFloor(h.translator.HandlePermissionRequest),
		// _kiro/mcp/elicitation (a request with an id). Routed here by method.
		vibekit.MethodElicitationCreate: h.translator.HandleElicitationCreate,
		// _kiro/userInput (a request with an id, 2.14+): the agent's
		// structured question — gated on the _meta.kiro.userInput
		// initialize capability declared in bridge.go.
		vibekit.MethodKiroUserInput: h.translator.HandleUserInput,
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
		// global) + runtime-side cache served at GET /api/governance. Gates
		// affordances the flags control (MCP availability, the org-policy
		// disclosure, the code-reference chip). See translate/governance.go.
		methodV3Governance: h.translator.HandleGovernanceState,
		// Workflow-run lifecycle. Nine KAS notifications → three SSE events; the
		// seven middle kinds share one handler because they mean one thing to a
		// client ("refetch"). See translate/workflow.go.
		// Wrapped rather than registered bare: the run clock (run_bounds.go) has
		// to see a run start, pause and finish, and `run_start` is the only frame
		// vibekit gets for an AGENT-launched run, whose launch path is KAS's.
		methodWFRunStart:    h.runs.observeStart,
		methodWFRunComplete: h.runs.observeComplete,
	}
	for method, kind := range map[string]vibekit.RunProgressKind{
		methodWFNodeStart:     vibekit.RunProgressNodeStart,
		methodWFNodeComplete:  vibekit.RunProgressNodeComplete,
		methodWFNodePaused:    vibekit.RunProgressNodePaused,
		methodWFPaused:        vibekit.RunProgressPaused,
		methodWFLoopIteration: vibekit.RunProgressLoopIteration,
		methodWFWatchPoll:     vibekit.RunProgressWatchPoll,
		methodWFStepsQueued:   vibekit.RunProgressStepsQueued,
	} {
		h.chatHandlers[method] = h.translator.RunProgressHandler(kind)
	}
	// A run-level pause stops the clock, because each arm is a ceiling of
	// EXECUTING time: a run parked on purpose must not be cancelled for having
	// been parked. Node-level pauses keep it — a step waiting inside a run that is
	// still going is exactly what the ceiling is for.
	h.chatHandlers[methodWFPaused] = h.runs.observePaused(h.chatHandlers[methodWFPaused])
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
	h.sessUpdateHandlers = map[vibekit.ACPUpdateKind]sessionUpdateHandler{
		vibekit.ACPUpdateAgentChunk: ignoreSubSession(func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
			h.translator.HandleAssistantChunk(ctx, chatID, raw, false)
		}),
		vibekit.ACPUpdateThoughtChunk: ignoreSubSession(func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
			h.translator.HandleAssistantChunk(ctx, chatID, raw, true)
		}),
		vibekit.ACPUpdateToolCall:   h.translator.HandleToolCall,
		vibekit.ACPUpdateToolUpdate: h.translator.HandleToolCallUpdate,
		vibekit.ACPUpdatePlan:       ignoreSubSession(h.translator.HandlePlan),
		vibekit.ACPUpdateModeChange: ignoreSubSession(h.translator.HandleModeUpdate),
		// v3 (KAS) sub-kinds: context-usage + slash-command catalog moved
		// here from the v2 _kiro.dev/metadata + commands/available notifs.
		// session_info_update also carries compaction (summarization) state;
		// usage_update is the primary v3 context-usage channel; and
		// config_option_update delivers the live model/mode/effort catalog.
		vibekit.ACPUpdateSessionInfo:  h.translator.HandleSessionInfoUpdate,
		vibekit.ACPUpdateUsage:        ignoreSubSession(h.translator.HandleUsageUpdate),
		vibekit.ACPUpdateConfigOption: ignoreSubSession(h.translator.HandleConfigOptionUpdate),
	}
}

// translateACPEvent is the sole entry point from bridge_lifecycle's
// forward goroutine. Every branch must return promptly; long-running
// work belongs in goroutines inside the handler (see bridge_fs.go).
func (h *Runtime) translateACPEvent(chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	// Derive a context from the runtime's shutdownCtx so handlers can
	// propagate shutdown cancellation to I/O calls.
	ctx, cancel := h.hubContext()
	defer cancel()

	// A RUN bridge's frames take their own door: step content must not flow
	// into any transcript, and this connection's workflow lifecycle frames are
	// workspace-global rather than a chat's. See run_host.go.
	if isRunChat(chatID) {
		h.dispatch(ctx, chatID, msg)
		return
	}

	if msg.ID != nil && h.routeInboundRequest(ctx, chatID, msg) {
		return
	}

	if fn, ok := h.chatHandlers[msg.Method]; ok {
		fn(ctx, chatID, msg)
		return
	}
	// The noop table is keyed by METHOD only, so the id test is what keeps it
	// from swallowing a REQUEST. Every member is a notification today, but a
	// future request-shaped method added to that map would otherwise return here
	// and never be answered, which is the exact wedge the refusal below exists to
	// prevent — and it would be invisible, because a noop logs nothing.
	if _, ok := h.noopMethods[msg.Method]; ok && msg.ID == nil {
		return
	}
	// An unrecognised REQUEST must be answered. KAS calls its ext-methods with
	// `await connection.extMethod(...)` and no timeout — the only rejection is
	// the connection closing — so an unanswered request does not degrade a
	// feature, it wedges the whole turn: the session/prompt Call never returns
	// (Bridge.Call has no client-side deadline by design), bridgePrompting is
	// never released, and every later prompt on that chat 409s into a client
	// queue whose only drain is a turn_ended that will never fire.
	//
	// This mirrors the fallback the utility bridge (utility_session.go) and the
	// run bridge (run_host.go) already have, with the same rationale written in
	// their comments. The chat dispatcher was the one of the three without it.
	//
	// The reachable case today is _kiro/workspace/{active_file,
	// currently_open_files}: KAS registers those resolvers with NO capability
	// gate, and reaches them from processPromptWithContext on any `#[[...]]`
	// reference in a workspace-authored agent prompt. vibekit deliberately does
	// not implement the pull direction (see vibekit-acp.md), so a prompt in this
	// very repo can raise a request nothing answers.
	//
	// The code is -32601, "Method not found", which is what JSON-RPC 2.0 assigns
	// to exactly this case. KAS answers its own unknown ext-methods with -32603
	// and switches on nothing, so either settles its promise — but -32603 means
	// "I broke", and labelling a deliberate refusal an internal fault would make
	// these logs lie about which side has the problem.
	if msg.ID != nil {
		slog.Warn("chat bridge: refusing an unexpected peer request",
			"method", msg.Method, "chat_id", chatID, "id", *msg.ID)
		if err := h.BridgeRespond(ctx, chatID, *msg.ID, nil,
			&vibekit.RPCError{
				Code:    vibekit.RPCCodeMethodNotFound,
				Message: "unsupported on the chat session: " + msg.Method,
			}); err != nil {
			slog.Error("chat bridge: refusal could not be delivered; the turn may be wedged",
				"method", msg.Method, "chat_id", chatID, "error", err)
		}
		return
	}
	// v3 (KAS) emits the _kiro/* extension namespace. Unhandled NOTIFICATIONS
	// (Cedar policy, spec/hooks/knowledge/safety/sandbox families, etc.) fall
	// through to a debug log rather than a silent drop. Nothing is owed on the
	// wire for these, unlike the request branch above.
	if strings.HasPrefix(msg.Method, "_kiro/") {
		slog.Debug("unhandled kiro extension",
			"method", msg.Method, "chat_id", chatID)
	}
}

// routeInboundRequest dispatches an A→C REQUEST (a frame carrying an id) to the
// handler family that owns it, returning whether one claimed it.
//
// Split out of translateACPEvent so the request chain and the notification chain
// are separately readable: every arm here owes a response on the wire, and
// nothing below in the caller does. That is also why the caller's fallthrough is
// a refusal rather than a log — see its comment.
func (h *Runtime) routeInboundRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) bool {
	if h.handleFSRequest(ctx, chatID, msg) {
		return true
	}
	// KAS's own fs verbs (_kiro/fs/{stat,read_directory,delete}). Separate from
	// handleFSRequest because they are a different rung of KAS's adapter ladder
	// with different shapes — and because these execute rather than stage.
	if h.handleKiroFSRequest(ctx, chatID, msg) {
		return true
	}
	// v3 (KAS) host-mediated client requests (_kiro/auth/getAccessToken,
	// _kiro/terminal/shell_type).
	if h.handleKiroClientRequest(ctx, chatID, msg) {
		return true
	}
	// v3 (KAS) credential storage (_kiro/secret/*). Must be answered: KAS
	// rethrows a store/delete failure into the MCP connect path, and an
	// UNANSWERED request wedges the turn.
	if h.handleKiroSecretRequest(ctx, chatID, msg) {
		return true
	}
	// Terminal requests from kiro-cli (terminal/create, terminal/output, etc.).
	if strings.HasPrefix(msg.Method, methodTermPrefix) {
		h.handleTerminalRequest(ctx, chatID, msg.Method, msg)
		return true
	}
	return false
}

// --- Session-update sub-dispatcher ---

// handleSessionUpdate decodes the `update` envelope and fans out to
// the sub-handler for each sessionUpdate subtype. The top-level
// params.sessionId identifies whether this notification belongs to the
// parent chat or a subagent; we pass it through so tool-call handlers
// can set SubSessionID on emitted events.
func (h *Runtime) handleSessionUpdate(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
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

	// Determine attribution through the ONE shared classifier. This site and
	// translate/deps.go's deriveSubSession are the protocol's two derivation
	// points and they shared no code, so a step frame classified differently
	// depending on which door it came through. ACP cannot supply the answer —
	// its session model is flat, one sessionId per method with no parent/child
	// concept — so it comes from KAS's own `_meta.kiro.workflow`, which the
	// frame carries, plus the step-session registry for frames that do not.
	//
	// A STEP resolves to OwnerStep and therefore to an empty subSessionID: the
	// per-kind handlers all read a non-empty value as "a subagent did this",
	// which is not true of a step. A step's own attribution rides its blocks
	// instead (ACPWorkflowMeta.SubtaskID).
	subSessionID := ""
	if h.translator.ClassifyFrame(chatID, env.Params.SessionID, base.Meta.Kiro.Workflow != nil) == translate.OwnerSubagent {
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
type sessionUpdateHandler = func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, subSessionID string)

// sessionUpdateHandlers returns the map of sessionUpdate kind → handler.
// The map is built eagerly in initDispatch and cached on the Runtime.
func (h *Runtime) sessionUpdateHandlers() map[vibekit.ACPUpdateKind]sessionUpdateHandler {
	return h.sessUpdateHandlers
}
