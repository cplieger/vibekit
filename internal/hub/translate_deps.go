package hub

// Deps interface implementation for translate.Translator.
// Hub satisfies translate.Deps so the Translator can access
// Hub internals without importing the hub package.

import (
	"context"

	"github.com/cplieger/vibekit/internal/ansitext"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/redact"
	"github.com/cplieger/vibekit/internal/translate"
)

var _ translate.Deps = (*Hub)(nil)

// ChatStore returns the hub's chat store.
func (h *Hub) ChatStore() api.ChatStore { return h.chatStore }

// ParentACPSession returns the parent ACP session ID for a chat.
func (h *Hub) ParentACPSession(chatID api.ChatID) string {
	return h.parentACPSession(chatID)
}

// WorkDir returns the workspace root directory.
func (h *Hub) WorkDir() string { return h.lifecycle.workDir }

// BridgeRespond sends a response to the bridge for the given chat.
func (h *Hub) BridgeRespond(ctx context.Context, chatID api.ChatID, requestID int64, result any, err error) error {
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return nil
	}
	return sb.bridge.Respond(ctx, requestID, result, err)
}

// MCPRecorder returns the Hub's MCP state recorder.
func (h *Hub) MCPRecorder() translate.MCPRecorder {
	return &hubMCPRecorder{h: h}
}

// hubMCPRecorder adapts Hub's MCP internals to the MCPRecorder interface.
type hubMCPRecorder struct{ h *Hub }

func (r *hubMCPRecorder) RecordConnected(ctx context.Context, serverName string, tools []string, prompts []api.MCPPromptInfo, resources []api.MCPResourceInfo) {
	r.h.mcpRegistry.recordConnected(ctx, serverName, tools, prompts, resources)
}

func (r *hubMCPRecorder) RecordOAuth(ctx context.Context, serverName, oauthURL string) {
	r.h.mcpRegistry.recordOAuth(ctx, serverName, oauthURL)
}

func (r *hubMCPRecorder) RecordInitFailure(ctx context.Context, serverName, errMsg string) {
	r.h.mcpRegistry.recordInitFailure(ctx, serverName, errMsg)
}

func (r *hubMCPRecorder) RecordDisabled(ctx context.Context, serverName string) {
	r.h.mcpRegistry.recordDisabled(ctx, serverName)
}

func (r *hubMCPRecorder) SignalReady() {
	r.h.mcpRegistry.signalReady()
}

// PendingPermsAdd tracks a pending permission event for SSE replay.
func (h *Hub) PendingPermsAdd(requestID int64, evt api.ServerEvent) {
	h.sse.pendingPerms.Add(requestID, evt)
}

// NotifyPush sends a push notification.
func (h *Hub) NotifyPush(ctx context.Context, body string, kind api.PushKind) {
	h.coord.NotifyPush(ctx, body, kind)
}

// BufferStore returns the buffer store for streaming handlers.
func (h *Hub) BufferStore() translate.BufferAccess {
	return h.bridge.assistantBufs
}

// LineTracker returns the line tracker for file-change recording.
func (h *Hub) LineTracker() translate.LineRecorder {
	return h.lines
}

// TerminalOutput returns an agent terminal's output for the translate layer to
// persist onto the owning tool call. See translate.StreamingAccess for why the
// tool call needs it.
//
// It reads the RAW ring and renders on demand rather than returning a
// pre-accumulated copy. Three things fall out of that. The rendering is
// derivable, so there is no second buffer to keep in step and no second cap: the
// ring already bounds the bytes and keeps the tail. It works for a terminal that
// has already been released, because `retire` kept those bytes under the same id
// — which matters because KAS releases before it reports the result, so the
// live registry is empty by the time this is called. And the sanitize-then-parse
// order here is the same one the live pump uses, so the persisted text and the
// streamed text cannot disagree about what an escape meant.
func (h *Hub) TerminalOutput(terminalID string) (string, []api.TextSpan, bool) {
	h.agentTerms.mu.Lock()
	term, live := h.agentTerms.terms[terminalID]
	h.agentTerms.mu.Unlock()

	var raw string
	switch {
	case live:
		raw = term.output.String()
	default:
		var ok bool
		raw, ok = h.agentTerms.takeRetired(terminalID)
		if !ok {
			return "", nil, false
		}
	}
	if raw == "" {
		return "", nil, false
	}
	text, spans := ansitext.Parse(redact.Output(api.SanitizeUnicode(raw)))
	return text, wireSpans(spans), true
}

// IsHookStatusEnabled returns whether hook status display is enabled.
func (h *Hub) IsHookStatusEnabled() bool {
	return h.isHookStatusEnabled()
}
