package command

// Role-based consumer interfaces decomposed from the Dependencies
// god-interface. Each interface is shaped by what its consumers
// actually need, enabling minimal stub surfaces in tests and
// documenting each handler's actual dependency footprint.
//
// The Dependencies interface remains as the composite that Hub
// satisfies. These narrow interfaces are embedded into Dependencies
// for compile-time verification.

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/pending"
)

// BridgeAccess provides bridge lifecycle operations needed by prompt,
// cancel, subagent, slash, and permission handlers.
type BridgeAccess interface {
	GetBridge(chatID api.ChatID) Bridge
	GetOrCreateBridge(ctx context.Context, chatID api.ChatID, model string) (Bridge, error)
	CloseBridge(chatID api.ChatID)
	PrimeIfNeeded(ctx context.Context, chatID api.ChatID, b Bridge)
}

// ChatAccess provides chat store and broadcast operations needed by
// create, delete, rewind, and supervised-mode handlers.
type ChatAccess interface {
	ChatStore() api.ChatStore
	Broadcast(ctx context.Context, evt api.ServerEvent)
	CleanupChatState(ctx context.Context, chatID api.ChatID)
}

// SupervisedAccess provides supervised-mode and pending-permission
// operations needed by pending/trust handlers.
type SupervisedAccess interface {
	PendingStore() *pending.Store
	SupervisedSetTrust(chatID api.ChatID)
	SupervisedClearTrust(chatID api.ChatID, reason api.ClearReason)
	ChatInSupervisedMode(ctx context.Context, chatID api.ChatID) bool
	FlushPendingForChat(ctx context.Context, chatID api.ChatID, reason api.ClearReason)
	ClearPendingPermsForChat(chatID api.ChatID)
	RemovePendingPerm(requestID int64)
}

// InfraDeps provides shared infrastructure operations (workspace,
// lifecycle, dedup, MCP readiness) used across multiple handlers.
type InfraDeps interface {
	WorkDir() string
	ConfigDir() string
	ShutdownCtx() context.Context
	InflightAdd(delta int)
	InflightDone()
	MCPWaitForReady(ctx context.Context, timeout time.Duration) bool
	ResolveInsideWorkDir(rel string) (string, error)
	IsEmptyTurn(resp *api.RPCResponse, chatID api.ChatID) bool
	EmitTurnEndedWithStats(ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, creditsDelta, elapsedMs float64)
}

// Typed accessors on Dispatcher for narrow interface access.
// These allow handler functions to document and use only the
// subset of Dependencies they actually need.

// Bridge returns the BridgeAccess subset of dependencies.
func (d *Dispatcher) Bridge() BridgeAccess { return d.deps }

// Chat returns the ChatAccess subset of dependencies.
func (d *Dispatcher) Chat() ChatAccess { return d.deps }

// Supervised returns the SupervisedAccess subset of dependencies.
func (d *Dispatcher) Supervised() SupervisedAccess { return d.deps }

// Infra returns the InfraDeps subset of dependencies.
func (d *Dispatcher) Infra() InfraDeps { return d.deps }
