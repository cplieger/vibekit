// actionStatus: a consolidated view over an action's recent lifecycle.
// Maintains a per-name snapshot of { pending, lastError, lastSuccess,
// lastDispatchedAt } updated by the registry's lifecycle subscriber.
//
// Use cases:
//   - UI surfaces that need richer state than just "is it pending":
//     show "Saved · 3s ago" with the last success, or "Last error:
//     server timeout" persistent affordance.
//   - Cross-cutting indicators that span multiple actions (e.g. a
//     "Last save failed" banner reading lastError off the last
//     settings-* action).
//
// Lifecycle:
//   - First call to `actionStatus(name)` registers a per-name listener
//     on the registry via subscribeByName. Subsequent calls return the
//     cached snapshot.
//   - The snapshot is mutated in-place so callers holding a reference
//     see updates without resubscribing. (DO NOT mutate the returned
//     object externally.)
// ---------------------------------------------------------------------------

import type { ActionErrorLike, ActionInstance } from "./types.js";
import { subscribeByName, pendingFor } from "./registry.js";

export interface ActionStatus {
  /** Number of currently-pending instances of this action name. */
  pending: number;
  /** Last error recorded, or undefined if none in this session. */
  lastError?: ActionErrorLike;
  /** Result of the last successful dispatch, or undefined if none. */
  lastSuccess?: unknown;
  /** Timestamp of the most recent dispatch (any status). 0 if never
   *  dispatched. */
  lastDispatchedAt: number;
  /** Timestamp of the most recent terminal transition (success / error /
   *  cancelled). 0 if no terminal yet. */
  lastSettledAt: number;
  /** Timestamp of the most recent cancellation. 0 if never cancelled.
   *  Distinguishes "last settled was a cancel" from success/error without
   *  requiring callers to compare timestamps across fields. */
  lastCancelledAt: number;
}

const snapshots = new Map<string, ActionStatus>();
const unsubs = new Map<string, () => void>();
const MAX_SNAPSHOTS = 200;

function handleEvent(name: string, inst: ActionInstance): void {
  const snap = snapshots.get(name);
  if (snap === undefined) return;
  if (inst.dispatchedAt > snap.lastDispatchedAt) {
    snap.lastDispatchedAt = inst.dispatchedAt;
  }
  // Always recompute pending from the registry's authoritative index.
  // Avoids double-count when actionStatus() is first called from within
  // a listener during the same record() notification that fires us.
  snap.pending = pendingFor(name).length;
  switch (inst.status) {
    case "success":
      snap.lastSuccess = inst.result;
      snap.lastSettledAt = inst.completedAt ?? Date.now();
      break;
    case "error":
      if (inst.error !== undefined) snap.lastError = inst.error;
      snap.lastSettledAt = inst.completedAt ?? Date.now();
      break;
    case "cancelled":
      snap.lastSettledAt = inst.completedAt ?? Date.now();
      snap.lastCancelledAt = inst.completedAt ?? Date.now();
      break;
  }
}

/**
 * Get a live, mutable snapshot of an action's status. The returned
 * object updates in-place as the action's lifecycle progresses; do
 * NOT mutate it externally.
 *
 * First call for a given name lazily installs a per-name registry
 * listener via subscribeByName (O(1) per record() call vs the previous
 * global subscribe which was O(n) across all watched names).
 * Subsequent calls return the cached (same-reference) snapshot.
 *
 * @param name - Action name to observe (e.g. "settings.patch").
 * @returns A live {@link ActionStatus} snapshot (pending count, last
 *   error/success, timestamps).
 */
export function actionStatus(name: string): ActionStatus {
  let snap = snapshots.get(name);
  if (snap !== undefined) return snap;

  // Seed pending count from the registry so callers that subscribe
  // AFTER an action is already in-flight see the correct count.
  const pending = pendingFor(name);
  let lastDispatchedAt = 0;
  for (let i = 0; i < pending.length; i++) {
    if (pending[i]!.dispatchedAt > lastDispatchedAt) lastDispatchedAt = pending[i]!.dispatchedAt;
  }
  snap = {
    pending: pending.length,
    lastDispatchedAt,
    lastSettledAt: 0,
    lastCancelledAt: 0,
  };
  snapshots.set(name, snap);

  // Per-name subscription: only fires for this action's events.
  const unsub = subscribeByName(name, (inst) => handleEvent(name, inst));
  unsubs.set(name, unsub);

  // Evict oldest idle entry when over cap to bound memory.
  if (snapshots.size > MAX_SNAPSHOTS) {
    for (const [k, v] of snapshots) {
      if (k !== name && v.pending === 0) {
        snapshots.delete(k);
        const u = unsubs.get(k);
        if (u !== undefined) { u(); unsubs.delete(k); }
        break;
      }
    }
  }
  return snap;
}

/** Test-only: clear snapshots so tests don't bleed state. Also
 *  unsubscribes all per-name listeners so a freshly-reset registry
 *  doesn't accumulate stale subscriptions. */
export function _resetForTest(): void {
  for (const unsub of unsubs.values()) unsub();
  unsubs.clear();
  snapshots.clear();
}
