// ---------------------------------------------------------------------------
// SSE handlers for Supervised mode: pending_change_added,
// pending_change_resolved, pending_changes_cleared.
//
// Forwards into the store. Per-card and per-pill rendering
// subscribes to store change events and reads s.pending_changes.
// Reconnect replay goes through the same events so there's no
// special-case path here.
// ---------------------------------------------------------------------------

import { onSSE, emitBus, BUS_PENDING_ADDED, BUS_PENDING_RESOLVED, BUS_PENDING_CLEARED, BUS_PENDING_TRUST_ENABLED, BUS_PENDING_TRUST_CLEARED } from "../bus.js";
import {
  addPendingChange, removePendingChange, clearPendingChanges,
  setTrustedThisTurn,
} from "../store.js";

onSSE("pending_change_added", (chatID, p) => {
  if (p.change === undefined) return;
  addPendingChange(chatID, p.change);
  // Notify tool cards so they can flip their pending status. The
  // tool-card UI listens for this rather than re-rendering the whole
  // transcript on every pending_change_added.
  emitBus(BUS_PENDING_ADDED, { chatID, change: p.change });
});

onSSE("pending_change_resolved", (chatID, p) => {
  if (p.tool_call_id === undefined) return;
  removePendingChange(chatID, p.tool_call_id);
  emitBus(BUS_PENDING_RESOLVED, { chatID, toolCallID: p.tool_call_id, action: p.action });
});

onSSE("pending_changes_cleared", (chatID, p) => {
  clearPendingChanges(chatID);
  emitBus(BUS_PENDING_CLEARED, { chatID, reason: p.reason ?? "" });
});

onSSE("pending_trust_enabled", (chatID) => {
  setTrustedThisTurn(chatID, true);
  emitBus(BUS_PENDING_TRUST_ENABLED, { chatID });
});

onSSE("pending_trust_cleared", (chatID, p) => {
  setTrustedThisTurn(chatID, false);
  emitBus(BUS_PENDING_TRUST_CLEARED, { chatID, reason: p?.reason ?? "turn_ended" });
});
