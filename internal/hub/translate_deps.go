package hub

// Deps interface implementation for translate.Translator.
// Hub satisfies translate.Deps so the Translator can access
// Hub internals without importing the hub package.

import (
	"context"

	"github.com/cplieger/vibekit/internal/ansitext"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/translate"
)

var _ translate.Deps = (*Hub)(nil)

// ChatRecords returns the hub's chat store as translate reads it (3 of its 9
// methods). Separate from ChatStore() below it because internal/command needs 5,
// and one accessor cannot return two narrow types.
func (h *Hub) ChatRecords() translate.ChatRecords { return h.chatStore }

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
func (h *Hub) NotifyPush(ctx context.Context, body string, kind api.PushKind, chatID api.ChatID) {
	h.coord.NotifyPush(ctx, body, kind, chatID)
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
//
// ok reports whether the terminal is KNOWN, not whether it printed anything. A
// registered terminal with no output answers ("", nil, true), because a silent
// command is a different fact from a lost record and only the second one is
// worth warning about.
func (h *Hub) TerminalOutput(terminalID string) (string, []api.TextSpan, bool) {
	h.agentTerms.mu.Lock()
	term, live := h.agentTerms.terms[terminalID]
	h.agentTerms.mu.Unlock()

	var raw string
	if live {
		raw = term.rawOutput()
	} else {
		var known bool
		raw, known = h.agentTerms.peekRetired(terminalID)
		if !known {
			return "", nil, false
		}
	}
	if raw == "" {
		return "", nil, true
	}
	text, spans := ansitext.Parse(api.SanitizeUnicode(raw))
	return text, wireSpans(spans), true
}

// IsScheduledRun reports whether a run was launched by a schedule.
//
// The run's LEASE is already the record of that — it is what gates the deny-fast
// permission floor — so this exports the fact rather than tracking it twice.
// Granted between `new` and `invoke` in launchRun, which is before the first
// lifecycle frame can arrive, so a run_start reaching translate always sees the
// origin its launch recorded.
func (h *Hub) IsScheduledRun(workflowID string) bool {
	l, ok := h.lease(workflowID)
	return ok && l.Origin == runlease.OriginScheduled
}

// IsHookStatusEnabled returns whether hook status display is enabled.
func (h *Hub) IsHookStatusEnabled() bool {
	return h.isHookStatusEnabled()
}
