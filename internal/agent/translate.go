// ACP → domain event translation. `translateACPEvent` is the single dispatch point; the
// per-method handlers live in sibling `translate_*.go` files. An unhandled `_kiro/*`
// extension logs at Debug, because that namespace is explicitly unstable.

package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// chatHandler is the notification handler type; a global handler gets an empty chatID.
type chatHandler = func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse)

// ignoreAttribution adapts a handler that needs no attribution to sessionUpdateHandler.
func ignoreAttribution(fn func(context.Context, vibekit.ChatID, json.RawMessage)) sessionUpdateHandler {
	return func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, _ translate.FrameAttribution) {
		fn(ctx, chatID, raw)
	}
}

// initDispatch builds the method → handler maps once, from the runtime's constructor.
// Eagerly rather than on first use: several bridge goroutines read them concurrently.
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
		// Wrapped rather than registered bare so the run clock (run_bounds.go) can see
		// start/pause/finish — run_start is the only frame for an AGENT-launched run.
		methodWFRunStart:    rt.runs.observeStart,
		methodWFRunComplete: rt.runs.observeComplete,
		// A workflow step's question, and the only frame carrying it. Registered on the
		// run bridge's door too — see (*Runtime).dispatch.
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
	// A run-level pause stops the clock, so a run parked on purpose is not cancelled for
	// the wait; node-level pauses keep it running. TWO wrappers rather than one, because
	// they read different collaborators, and the order is load-bearing: the clock must be
	// parked before anything can decide to resume.
	rt.chatHandlers[methodWFPaused] = rt.runs.observePaused(
		rt.runs.healPaused(rt.chatHandlers[methodWFPaused]),
	)
	// A completed node is the only honest evidence a pause has cleared.
	rt.chatHandlers[methodWFNodeComplete] = rt.runs.healProgress(rt.chatHandlers[methodWFNodeComplete])
	// The tab offer's retry: an offer left unspent because the launching chat had no tab
	// yet, and one frame per step is enough to catch that chat opening later.
	rt.chatHandlers[methodWFNodeStart] = rt.runs.offerOnProgress(rt.chatHandlers[methodWFNodeStart])
	// Recognised but intentionally ignored; listed to keep them out of the Debug log.
	rt.noopMethods = map[string]struct{}{
		methodV3SessionsChanged:    {},
		methodV3ToolsDidChange:     {},
		methodV3SteeringDocs:       {},
		methodV3ProgressiveContext: {},
		methodV3Powers:             {},
	}
	// Eager: several bridge goroutines call sessionUpdateHandlers() concurrently.
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
		// The metadata channels.
		vibekit.ACPUpdateSessionInfo: rt.translator.HandleSessionInfoUpdate,
		vibekit.ACPUpdateUsage:       ignoreAttribution(rt.translator.HandleUsageUpdate),
		// The only frame telling vibekit that KAS moved the effort level itself.
		vibekit.ACPUpdateConfigOption: rt.coord.healEffort(ignoreAttribution(rt.translator.HandleConfigOptionUpdate)),
	}
}

// translateACPEvent is the sole entry point from bridge_lifecycle's forward goroutine.
// Every branch must return promptly; long-running work belongs in a goroutine inside the
// handler.
func (rt *Runtime) translateACPEvent(chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	ctx, cancel := rt.lifecycle.derivedContext()
	defer cancel()

	// A RUN bridge's frames take their own door: step content reaches no transcript.
	if isRunChat(chatID) {
		rt.dispatch(ctx, chatID, msg)
		return
	}

	if msg.ID != nil && rt.routeInboundRequest(ctx, chatID, msg) {
		return
	}

	// The id test keeps a future request-shaped method promoted into this map from being
	// swallowed silently by a notification handler.
	if fn, ok := rt.chatHandlers[msg.Method]; ok && msg.ID == nil {
		fn(ctx, chatID, msg)
		return
	}
	// Same reason: one landing in the noop table would return unanswered, and log nothing.
	if _, ok := rt.noopMethods[msg.Method]; ok && msg.ID == nil {
		return
	}
	// An unrecognised REQUEST must be answered. KAS's ext-method calls have no timeout, so
	// an unanswered one wedges the turn: session/prompt's Call never returns, its slot is
	// never released, and every later prompt on that chat 409s forever. -32601 rather than
	// -32603, or a deliberate refusal reads as our own fault in these logs.
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
	// Debug rather than a silent drop; nothing is owed on the wire for a notification.
	if strings.HasPrefix(msg.Method, "_kiro/") {
		slog.Debug("unhandled kiro extension",
			"method", msg.Method, "chat_id", chatID)
	}
}

// routeInboundRequest dispatches an A→C REQUEST (a frame carrying an id) to the handler
// family that owns it, reporting whether one claimed it. Every arm owes a wire response.
func (rt *Runtime) routeInboundRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) bool {
	if rt.inbound.handleFSRequest(ctx, chatID, msg) {
		return true
	}
	// A different rung of the adapter ladder: these execute rather than stage.
	if rt.inbound.handleKiroFSRequest(ctx, chatID, msg) {
		return true
	}
	if rt.inbound.handleKiroClientRequest(ctx, chatID, msg) {
		return true
	}
	// Must be answered: KAS rethrows a store/delete failure into the MCP connect path.
	if rt.inbound.handleKiroSecretRequest(ctx, chatID, msg) {
		return true
	}
	if strings.HasPrefix(msg.Method, methodTermPrefix) {
		rt.handleTerminalRequest(ctx, chatID, msg.Method, msg)
		return true
	}
	// Explicit whitelist for the three request-shaped chat-handler members: claiming the
	// frame here makes double dispatch unreachable, since the caller returns on true.
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

// handleSessionUpdate decodes the `update` envelope and fans out to the sub-handler for
// each sessionUpdate subtype, passing the frame's attribution through.
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

	// The ONE shared classifier, also used by deriveSubSession, so a step frame classifies
	// the same whichever door it came through. A STEP carries an empty SubSessionID and
	// Step true, because a non-empty id means "a subagent did this".
	attr := rt.translator.Attribute(chatID, env.Params.SessionID, base.Meta.Kiro.Workflow != nil)

	// A REPLAYED frame is stored history and must not reach the live handlers: ungated, it
	// opens a PHANTOM turn with no session/prompt response to end it. The frames build the
	// transcript instead, and the gate is PER-FRAME since current-state frames are untagged.
	if base.Meta.Kiro.Replay {
		// A load in flight consumes the frame; otherwise it is dropped.
		if !rt.replay.ingestReplayFrame(chatID, base.Kind, env.Params.Update) {
			slog.Debug("session/update: dropping replayed frame, no load in flight",
				"chat_id", chatID, "kind", base.Kind)
		}
		return
	}

	// Sub-kinds without a handler fall through silently, user_message_chunk deliberately
	// among them: vibekit persists user messages itself, so KAS's echo would double-render.
	fn, ok := rt.sessionUpdateHandlers()[base.Kind]
	if !ok {
		return
	}
	fn(ctx, chatID, env.Params.Update, attr)
}

// sessionUpdateHandler is the common signature for session-update sub-handlers.
type sessionUpdateHandler = func(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr translate.FrameAttribution)

// sessionUpdateHandlers returns the kind → handler map initDispatch built.
func (rt *Runtime) sessionUpdateHandlers() map[vibekit.ACPUpdateKind]sessionUpdateHandler {
	return rt.sessUpdateHandlers
}
