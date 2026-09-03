// ACP → domain event translation (dispatcher + session-update sub-dispatcher).
//
// kiro-cli sends notifications, requests needing a response, and
// method-qualified envelopes. `translateACPEvent` is the single dispatch
// point; per-method handlers live in sibling `translate_*.go` files.
// Unhandled `_kiro/*` extensions log at Debug (that namespace is explicitly
// unstable); an unknown stable ACP-spec method logs the same but is a
// stronger signal something needs wiring.

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

// ignoreAttribution adapts a 3-arg handler (ctx, chatID, raw) to the
// 4-arg sessionUpdateHandler signature by discarding the attribution.
// Eliminates repeated anonymous closure boilerplate in the dispatch table.
func ignoreAttribution(fn func(context.Context, vibekit.ChatID, json.RawMessage)) sessionUpdateHandler {
	return func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, _ translate.FrameAttribution) {
		fn(ctx, chatID, raw)
	}
}

// initDispatch builds the method → handler maps, once, from the runtime's
// constructor. Eagerly rather than on first use: several bridge goroutines read
// these maps concurrently, and routeInboundRequest reaches chatHandlers on the
// request path.
func (rt *Runtime) initDispatch() {
	rt.chatHandlers = map[string]chatHandler{
		vibekit.MethodSessionUpdate: rt.handleSessionUpdate,
		// Refused on a short budget for a SCHEDULED run rather than parking it forever.
		vibekit.MethodRequestPermission: rt.runs.permissionWithUnattendedFloor(rt.translator.HandlePermissionRequest),
		vibekit.MethodElicitationCreate: rt.translator.HandleElicitationCreate,
		// Gated on the _meta.kiro.userInput initialize capability (bridge.go).
		vibekit.MethodKiroUserInput: rt.translator.HandleUserInput,
		// v3 (KAS) _kiro/* notifications.
		methodV3RateLimit:            rt.translator.HandleRateLimit,
		methodV3CustomAgentNotFound:  rt.translator.HandleAgentNotFound,
		methodV3CustomAgentConfigErr: rt.translator.HandleAgentConfigError,
		methodV3MCPStatus:            rt.translator.HandleMCPStatus,
		methodV3SystemNotify:         rt.translator.HandleSystemNotify,
		// Cedar policy hot-reload / parse-error → SSE refetch of GET /api/permissions.
		methodV3PolicyChanged:  rt.translator.HandlePolicyChanged,
		methodV3PolicyError:    rt.translator.HandlePolicyError,
		methodV3CodeReferences: rt.translator.HandleCodeReferences,
		// Dormant unless the enterprise safety gate is installed. See translate/safety.go.
		methodV3SafetyStatusChanged: rt.translator.HandleSafetyStatusChanged,
		methodV3SafetyPropertiesChg: rt.translator.HandleSafetyPropertiesChanged,
		methodV3Governance:          rt.translator.HandleGovernanceState,
		// Nine KAS notifications collapse to three SSE events; see translate/workflow.go.
		// Wrapped (not registered bare) so the run clock (run_bounds.go) can see
		// start/pause/finish — run_start is the only frame for an AGENT-launched run.
		methodWFRunStart:    rt.runs.observeStart,
		methodWFRunComplete: rt.runs.observeComplete,
		// A workflow step's question. The ONLY frame carrying it, and it used to
		// fall through to the Debug tail below, which lost the text, its run and
		// its step together. Registered on the run bridge's door too — see
		// (*Runtime).dispatch.
		methodKiroSessionNotify: rt.runs.handleSessionNotify,
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
		rt.chatHandlers[method] = rt.translator.RunProgressHandler(kind)
	}
	// A run-level pause stops the clock (each arm is a ceiling of EXECUTING
	// time; a run parked on purpose must not be cancelled for it). Node-level
	// pauses keep the clock running. TWO wrappers composed rather than
	// merged, since they read different collaborators: observePaused reaches
	// the bounds, healPaused reaches the bridges and chat store. Order is
	// load-bearing — the clock must be parked before anything can decide to
	// resume.
	rt.chatHandlers[methodWFPaused] = rt.runs.observePaused(
		rt.runs.healPaused(rt.chatHandlers[methodWFPaused]),
	)
	// A completed node is the only honest evidence a pause has cleared, so
	// it returns the run's heal budget.
	rt.chatHandlers[methodWFNodeComplete] = rt.runs.healProgress(rt.chatHandlers[methodWFNodeComplete])
	// v3 methods recognised but intentionally ignored (feature flags, catalogs
	// vibekit sources via REST). Listed to keep them out of the unhandled-debug log.
	rt.noopMethods = map[string]struct{}{
		methodV3SessionsChanged:    {},
		methodV3ToolsDidChange:     {},
		methodV3SteeringDocs:       {},
		methodV3ProgressiveContext: {},
		methodV3Powers:             {},
	}
	// Built eagerly to avoid a data race when multiple bridge goroutines call
	// sessionUpdateHandlers() concurrently.
	rt.sessUpdateHandlers = map[vibekit.ACPUpdateKind]sessionUpdateHandler{
		vibekit.ACPUpdateAgentChunk: ignoreAttribution(func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
			rt.translator.HandleAssistantChunk(ctx, chatID, raw, false)
		}),
		vibekit.ACPUpdateThoughtChunk: ignoreAttribution(func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
			rt.translator.HandleAssistantChunk(ctx, chatID, raw, true)
		}),
		vibekit.ACPUpdateToolCall:   rt.translator.HandleToolCall,
		vibekit.ACPUpdateToolUpdate: rt.translator.HandleToolCallUpdate,
		vibekit.ACPUpdatePlan:       ignoreAttribution(rt.translator.HandlePlan),
		vibekit.ACPUpdateModeChange: ignoreAttribution(rt.translator.HandleModeUpdate),
		// v3 sub-kinds moved here from v2's _kiro.dev/metadata + commands/available.
		vibekit.ACPUpdateSessionInfo: rt.translator.HandleSessionInfoUpdate,
		vibekit.ACPUpdateUsage:       ignoreAttribution(rt.translator.HandleUsageUpdate),
		// Wrapped: this frame is the only place vibekit learns that KAS moved the
		// session's reasoning effort on its own. See healEffort.
		vibekit.ACPUpdateConfigOption: rt.coord.healEffort(ignoreAttribution(rt.translator.HandleConfigOptionUpdate)),
	}
}

// translateACPEvent is the sole entry point from bridge_lifecycle's
// forward goroutine. Every branch must return promptly; long-running
// work belongs in goroutines inside the handler (see bridge_fs.go).
func (rt *Runtime) translateACPEvent(chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	ctx, cancel := rt.lifecycle.derivedContext()
	defer cancel()

	// A RUN bridge's frames take their own door: step content must not
	// flow into any transcript. See run_host.go.
	if isRunChat(chatID) {
		rt.dispatch(ctx, chatID, msg)
		return
	}

	if msg.ID != nil && rt.routeInboundRequest(ctx, chatID, msg) {
		return
	}

	// The id test keeps a future request-shaped method promoted into this
	// map from being swallowed silently by a notification handler.
	if fn, ok := rt.chatHandlers[msg.Method]; ok && msg.ID == nil {
		fn(ctx, chatID, msg)
		return
	}
	// Same reason: a request-shaped method landing in the noop table would
	// otherwise return here unanswered, and a noop logs nothing.
	if _, ok := rt.noopMethods[msg.Method]; ok && msg.ID == nil {
		return
	}
	// An unrecognised REQUEST must be answered. KAS's ext-method calls have no
	// timeout, so an unanswered request wedges the whole turn: session/prompt's
	// Call never returns, the prompt slot is never released, and every later
	// prompt on that chat 409s forever. -32601 rather than -32603: -32601 is
	// "method not found", -32603 means "I broke" — a deliberate refusal
	// labelled internal fault would blame the wrong side in these logs.
	if msg.ID != nil {
		slog.Warn("chat bridge: refusing an unexpected peer request",
			"method", msg.Method, "chat_id", chatID, "id", *msg.ID)
		if err := rt.BridgeRespond(ctx, chatID, *msg.ID, nil,
			&vibekit.RPCError{
				Code:    vibekit.RPCCodeMethodNotFound,
				Message: "unsupported on the chat session: " + msg.Method,
			}); err != nil {
			slog.Error("chat bridge: refusal could not be delivered; the turn may be wedged",
				"method", msg.Method, "chat_id", chatID, "error", err)
		}
		return
	}
	// v3 (KAS) _kiro/* namespace: unhandled NOTIFICATIONS log at Debug rather
	// than dropping silently — nothing is owed on the wire for these.
	if strings.HasPrefix(msg.Method, "_kiro/") {
		slog.Debug("unhandled kiro extension",
			"method", msg.Method, "chat_id", chatID)
	}
}

// routeInboundRequest dispatches an A→C REQUEST (a frame carrying an id) to the
// handler family that owns it, returning whether one claimed it. Split out of
// translateACPEvent because every arm here owes a response on the wire.
func (rt *Runtime) routeInboundRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) bool {
	if rt.inbound.handleFSRequest(ctx, chatID, msg) {
		return true
	}
	// KAS's own fs verbs (_kiro/fs/{stat,read_directory,delete}): a different
	// rung of the adapter ladder, and these execute rather than stage.
	if rt.inbound.handleKiroFSRequest(ctx, chatID, msg) {
		return true
	}
	if rt.inbound.handleKiroClientRequest(ctx, chatID, msg) {
		return true
	}
	// Must be answered: KAS rethrows a store/delete failure into the MCP
	// connect path, and an UNANSWERED request wedges the turn.
	if rt.inbound.handleKiroSecretRequest(ctx, chatID, msg) {
		return true
	}
	if strings.HasPrefix(msg.Method, methodTermPrefix) {
		rt.handleTerminalRequest(ctx, chatID, msg.Method, msg)
		return true
	}
	// Explicit whitelist for the three request-shaped chat-handler members:
	// claiming the frame here makes double dispatch unreachable, since the
	// caller returns the moment this returns true.
	switch msg.Method {
	case vibekit.MethodRequestPermission,
		vibekit.MethodElicitationCreate,
		vibekit.MethodKiroUserInput:
		if fn, ok := rt.chatHandlers[msg.Method]; ok {
			fn(ctx, chatID, msg)
			return true
		}
	}
	return false
}

// --- Session-update sub-dispatcher ---

// handleSessionUpdate decodes the `update` envelope and fans out to
// the sub-handler for each sessionUpdate subtype. The top-level
// params.sessionId identifies whether this notification belongs to the
// parent chat or a subagent; we pass it through so tool-call handlers
// can set SubSessionID on emitted events.
func (rt *Runtime) handleSessionUpdate(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
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

	// Determine attribution through the ONE shared classifier (also used by
	// translate/deps.go's deriveSubSession, so a step frame classifies the
	// same regardless of which door it came through). ACP's session model
	// is flat, so this comes from KAS's own `_meta.kiro.workflow` plus the
	// step-session registry for frames lacking it. A STEP carries an empty
	// SubSessionID and Step true: per-kind handlers read a non-empty id as
	// "a subagent did this", false for a step, whose own display
	// attribution rides ACPWorkflowMeta.SubtaskID on the blocks instead.
	attr := rt.translator.Attribute(chatID, env.Params.SessionID, base.Meta.Kiro.Workflow != nil)

	// A REPLAYED frame is stored history and must not reach the live
	// handlers. KAS replays a session's transcript on `session/load` as
	// ordinary session/update notifications tagged `_meta.kiro.replay`;
	// ungated, a replay opens a PHANTOM turn with no session/prompt response
	// to end it. The frames BUILD the transcript instead via a per-chat
	// translate.Projection (see load_projection.go): the gate is PER-FRAME
	// since KAS leaves current-state frames untagged.
	if base.Meta.Kiro.Replay {
		// A load in flight consumes the frame; otherwise it is dropped.
		if !rt.replay.ingestReplayFrame(chatID, base.Kind, env.Params.Update) {
			slog.Debug("session/update: dropping replayed frame, no load in flight",
				"chat_id", chatID, "kind", base.Kind)
		}
		return
	}

	// Sub-kinds without a handler fall through silently. user_message_chunk
	// is intentionally one of them: vibekit persists user messages itself
	// (cmdPrompt echoes the bubble via message_appended before the turn
	// starts), so consuming KAS's echo of the prompt would double-render it.
	fn, ok := rt.sessionUpdateHandlers()[base.Kind]
	if !ok {
		return
	}
	fn(ctx, chatID, env.Params.Update, attr)
}

// sessionUpdateHandler is the common signature for session-update sub-handlers.
type sessionUpdateHandler = func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr translate.FrameAttribution)

// sessionUpdateHandlers returns the map of sessionUpdate kind → handler.
// The map is built eagerly in initDispatch and cached on the Runtime.
func (rt *Runtime) sessionUpdateHandlers() map[vibekit.ACPUpdateKind]sessionUpdateHandler {
	return rt.sessUpdateHandlers
}
