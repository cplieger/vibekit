// Supervised-mode pending-change types split from events_payloads.go.
// These types define the wire shapes for pending-change SSE events and
// the PendingAction/PendingChangeKind domain types used across the
// pending, command, and hub packages.

package api

// PendingChangeKind discriminates between the three staged-file
// operations in Supervised mode. Using a typed string prevents typos
// at construction sites and makes the valid set discoverable.
type PendingChangeKind string

// PendingKindCreate and the following constants define the valid PendingChangeKind values for supervised-mode file operations.
const (
	PendingKindCreate PendingChangeKind = "create"
	PendingKindEdit   PendingChangeKind = "edit"
	PendingKindDelete PendingChangeKind = "delete"
)

// Valid reports whether k is one of the recognised pending change kinds.
func (k PendingChangeKind) Valid() bool {
	switch k {
	case PendingKindCreate, PendingKindEdit, PendingKindDelete:
		return true
	}
	return false
}

// PendingChange is one staged file operation awaiting user approval in
// Supervised mode. Key is ToolCallID (kiro-cli's id; unique per call);
// the Kind discriminates between writes, creates (no OldText), and
// deletes (no NewText).
//
// OldText and NewText are capped at pendingTextCap on the server side
// (4 MiB). When truncated, the client's "View diff" tab falls back to
// a file fetch for the full content; the staged payload only carries
// enough for the pill-popover summary and the inline diff preview.
type PendingChange struct {
	ToolCallID string            `json:"tool_call_id"`
	ChatID     ChatID            `json:"chat_id"`
	Path       string            `json:"path"`
	Kind       PendingChangeKind `json:"kind"` // "create" | "edit" | "delete"
	OldText    string            `json:"old_text,omitempty"`
	NewText    string            `json:"new_text,omitempty"`
	CreatedAt  int64             `json:"created_at"`
	Truncated  bool              `json:"truncated,omitempty"`
}

// PendingChangeAddedPayload is the payload for type="pending_change_added".
// Emitted when the Supervised-mode fs handler has received a write from
// kiro-cli and is blocking the agent until the user resolves it. Clients
// render a tool-card-level Accept/Reject pair and surface the op in the
// Supervised pill's popover.
type PendingChangeAddedPayload struct {
	Change PendingChange `json:"change"`
}

// PendingAction discriminates between the two resolution outcomes for
// a staged pending change. Using a typed string prevents typos at
// construction sites and makes the valid set discoverable.
type PendingAction string

// PendingActionAccept and the following constants define the valid PendingAction values for resolving staged changes.
const (
	PendingActionAccept PendingAction = "accept"
	PendingActionReject PendingAction = "reject"
)

// Valid reports whether a is one of the recognised pending actions.
func (a PendingAction) Valid() bool {
	switch a {
	case PendingActionAccept, PendingActionReject:
		return true
	}
	return false
}

// PendingChangeResolvedPayload is the payload for
// type="pending_change_resolved". Emitted after the user accepts or
// rejects a staged change, after the fs handler has unblocked and the
// disk state reflects the decision. Clients drop the op from the pending
// pill and update the source tool card's status.
type PendingChangeResolvedPayload struct {
	ToolCallID string        `json:"tool_call_id"`
	Action     PendingAction `json:"action"` // "accept" | "reject"
	Path       string        `json:"path,omitempty"`
}

// PendingChangesClearedPayload is the payload for
// type="pending_changes_cleared". Emitted when a turn is cancelled or
// the chat's Supervised mode is disabled while ops are outstanding;
// every pending op for the chat is rejected server-side and the client
// flushes its pending list.
type PendingChangesClearedPayload struct {
	Reason ClearReason `json:"reason,omitempty"` // "cancelled" | "mode_disabled" | "chat_deleted"
}

// ClearReason identifies why pending changes or trust were cleared.
// Using a typed string prevents typos at construction sites and makes
// the valid set discoverable via IDE completion.
type ClearReason string

// ClearReasonTurnEnded and the following constants define the valid ClearReason values for pending-change and trust clearance.
const (
	ClearReasonTurnEnded    ClearReason = "turn_ended"
	ClearReasonCancelled    ClearReason = "cancelled"
	ClearReasonModeDisabled ClearReason = "mode_disabled"
	ClearReasonChatDeleted  ClearReason = "chat_deleted"
	ClearReasonShutdown     ClearReason = "shutdown"
	ClearReasonUserCleared  ClearReason = "user_cleared"
	// ClearReasonBridgeExited flushes a chat's staged writes when its
	// kiro-cli bridge exits unexpectedly (crash, or a model-switch
	// CloseBridge). Cancel/delete/mode-disable already flush; this is the
	// bridge-exit sibling so a dead bridge can't leave a parked fs-handler
	// goroutine plus a phantom "awaiting approval" pending op.
	ClearReasonBridgeExited ClearReason = "bridge_exited"
)

// PendingTrustEnabledPayload is the payload for
// type="pending_trust_enabled". Emitted when the user clicks "Trust
// remaining" in the Supervised pill, setting the chat's perTurnTrust
// flag so subsequent agent writes in the same turn bypass staging.
// The pill flips to a visibly-distinct "Trusted · this turn" state;
// the flag clears on turn_ended via the paired
// pending_trust_cleared event. Replayed on SSE reconnect so the UI
// state survives disconnects mid-turn.
type PendingTrustEnabledPayload struct{}

// PendingTrustClearedPayload is the payload for
// type="pending_trust_cleared". Emitted when the perTurnTrust flag
// drops: end of turn, cancel, chat-delete, or supervised-mode
// toggle-off. The pill reverts to the standard Supervised state.
// Reason mirrors pending_changes_cleared's vocabulary so both events
// can share dispatch semantics on the client.
type PendingTrustClearedPayload struct {
	Reason ClearReason `json:"reason,omitempty"` // "turn_ended" | "cancelled" | "mode_disabled" | "chat_deleted"
}
