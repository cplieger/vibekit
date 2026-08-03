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

// PendingPermAccess provides the pending-PERMISSION bookkeeping handlers need:
// an unanswered request is replayed to a reconnecting client, so answering or
// abandoning one has to retire the entry.
//
// It was SupervisedAccess, and the rename is the point. The pending-CHANGE half
// went with internal/pending — vibekit no longer holds writes back, so there is
// no staging queue to resolve, trust past or flush, and KAS's turn approval
// arrives as an ordinary permission request. Nothing supervised is left here,
// and a `ChatInSupervisedMode` reader with no consumer went with the gate.
type PendingPermAccess interface {
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

// PendingPerms returns the PendingPermAccess subset of dependencies.
func (d *Dispatcher) PendingPerms() PendingPermAccess { return d.deps }

// Infra returns the InfraDeps subset of dependencies.
func (d *Dispatcher) Infra() InfraDeps { return d.deps }
