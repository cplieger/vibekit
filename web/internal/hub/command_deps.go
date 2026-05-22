package hub

// command.Dependencies implementation methods for Hub.
// These expose Hub internals to the command package handlers.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/command"
	"vibekit/internal/pending"
)

// Compile-time assertion: Hub satisfies command.Dependencies.
var _ command.Dependencies = (*Hub)(nil)

// GetBridge returns the active bridge for a chat, or nil.
func (h *Hub) GetBridge(chatID api.ChatID) command.Bridge {
	sb := h.getBridge(chatID)
	if sb == nil {
		return nil
	}
	return &bridgeAdapter{sb: sb}
}

// GetOrCreateBridge ensures a bridge exists for the chat.
func (h *Hub) GetOrCreateBridge(ctx context.Context, chatID api.ChatID, agent, model string) (command.Bridge, error) {
	sb, err := h.getOrCreateBridge(ctx, chatID, agent, model)
	if err != nil {
		return nil, err
	}
	return &bridgeAdapter{sb: sb}, nil
}

// CloseBridge tears down the bridge for a chat.
func (h *Hub) CloseBridge(chatID api.ChatID) {
	h.closeBridge(chatID)
}

// PendingStore returns the pending-changes store.
func (h *Hub) PendingStore() *pending.Store {
	return h.perm.pending
}

// SupervisedSetTrust sets the per-turn trust flag for a chat.
func (h *Hub) SupervisedSetTrust(chatID api.ChatID) {
	h.perm.supervised.SetTrust(chatID)
}

// SupervisedClearTrust clears the per-turn trust flag.
func (h *Hub) SupervisedClearTrust(chatID api.ChatID, reason api.ClearReason) {
	h.perm.supervised.ClearTrust(chatID, reason)
}

// ChatInSupervisedMode reports whether the chat has supervised mode on.
func (h *Hub) ChatInSupervisedMode(ctx context.Context, chatID api.ChatID) bool {
	return h.chatInSupervisedMode(ctx, chatID)
}

// FlushPendingForChat rejects all outstanding pending ops for a chat.
func (h *Hub) FlushPendingForChat(ctx context.Context, chatID api.ChatID, reason api.ClearReason) {
	h.flushPendingForChat(ctx, chatID, reason)
}

// ClearPendingPermsForChat drops unresolved permission_needed entries.
func (h *Hub) ClearPendingPermsForChat(chatID api.ChatID) {
	h.clearPendingPermsForChat(chatID)
}

// RemovePendingPerm removes a single pending permission by request ID.
func (h *Hub) RemovePendingPerm(requestID int64) {
	h.sse.pendingPerms.Remove(requestID)
}

// Checkpoints returns the checkpoint service, or nil if unavailable.
func (h *Hub) Checkpoints() api.CheckpointService {
	return h.checkpoints
}

// AdvanceCheckpointTurn bumps the checkpoint turn counter.
func (h *Hub) AdvanceCheckpointTurn(ctx context.Context, chatID api.ChatID) {
	h.advanceCheckpointTurn(ctx, chatID)
}

// ConfigDir returns the configuration directory.
func (h *Hub) ConfigDir() string {
	return h.lifecycle.configDir
}

// ShutdownCtx returns the context cancelled on shutdown.
func (h *Hub) ShutdownCtx() context.Context {
	return h.lifecycle.shutdownCtx
}

// InflightAdd increments the inflight counter.
func (h *Hub) InflightAdd(delta int) {
	h.lifecycle.inflight.Add(delta)
}

// InflightDone decrements the inflight counter.
func (h *Hub) InflightDone() {
	h.lifecycle.inflight.Done()
}

// InflightGo runs fn under the inflight WaitGroup.
func (h *Hub) InflightGo(fn func()) {
	h.lifecycle.inflight.Go(fn)
}

// CleanupChatState tears down all in-memory state for a chat.
func (h *Hub) CleanupChatState(ctx context.Context, chatID api.ChatID) {
	h.cleanupChatState(ctx, chatID)
}

// MCPWaitForReady blocks until MCP servers are ready or timeout.
func (h *Hub) MCPWaitForReady(ctx context.Context, timeout time.Duration) bool {
	return h.mcpRegistry.WaitForReady(ctx, timeout)
}

// ResolveInsideWorkDir validates a path is inside the workspace.
func (h *Hub) ResolveInsideWorkDir(rel string) (string, error) {
	return h.resolveInsideWorkDir(rel)
}

// PrimeIfNeeded primes the bridge with history if needed.
func (h *Hub) PrimeIfNeeded(ctx context.Context, chatID api.ChatID, b command.Bridge) {
	ba, ok := b.(*bridgeAdapter)
	if !ok {
		slog.Error("hub: PrimeIfNeeded called with non-bridgeAdapter Bridge",
			"type", fmt.Sprintf("%T", b))
		return
	}
	h.primeIfNeeded(ctx, chatID, ba.sb)
}

// LinesClear clears line tracking for a chat.
func (h *Hub) LinesClear(chatID api.ChatID) {
	h.lines.Clear(chatID)
}

// IsEmptyTurn checks if a prompt response is an empty turn.
func (h *Hub) IsEmptyTurn(resp *api.RPCResponse, chatID api.ChatID) bool {
	return h.isEmptyTurn(resp, chatID)
}

// EmitTurnEndedWithStats broadcasts turn_ended with usage stats.
func (h *Hub) EmitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, creditsDelta, elapsedMs float64) {
	h.emitTurnEndedWithStats(ctx, chatID, resp, creditsDelta, elapsedMs)
}

// bridgeAdapter wraps *sharedBridge to satisfy command.Bridge.
type bridgeAdapter struct {
	sb *sharedBridge
}

func (a *bridgeAdapter) Call(ctx context.Context, method string, params any) (*api.RPCResponse, error) {
	return a.sb.bridge.Call(ctx, method, params)
}

func (a *bridgeAdapter) Notify(ctx context.Context, method string, params any) error {
	return a.sb.bridge.Notify(ctx, method, params)
}

func (a *bridgeAdapter) Respond(ctx context.Context, requestID int64, result any, err error) error {
	return a.sb.bridge.Respond(ctx, requestID, result, err)
}

func (a *bridgeAdapter) SessionID() string {
	return string(a.sb.bridge.SessionID())
}

func (a *bridgeAdapter) TryAcquireForPrompt() bool {
	return a.sb.tryAcquireForPrompt()
}

func (a *bridgeAdapter) ReleaseAfterPrompt() {
	a.sb.releaseAfterPrompt()
}

func (a *bridgeAdapter) SetLastActive() {
	a.sb.mu.Lock()
	a.sb.lastActiveAt = time.Now()
	a.sb.mu.Unlock()
}

func (a *bridgeAdapter) SetPrompting() {
	a.sb.mu.Lock()
	a.sb.lastActiveAt = time.Now()
	a.sb.state = bridgePrompting
	a.sb.mu.Unlock()
}

func (a *bridgeAdapter) IsPrimed() bool {
	a.sb.mu.Lock()
	defer a.sb.mu.Unlock()
	return a.sb.primed
}

func (a *bridgeAdapter) SetPrimed() {
	a.sb.mu.Lock()
	a.sb.primed = true
	a.sb.mu.Unlock()
}
