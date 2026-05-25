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
//   - First call to `actionStatus(name)` registers a lazy listener on
//     the registry. Subsequent calls return the cached snapshot.
//   - The snapshot is mutated in-place so callers holding a reference
//     see updates without resubscribing. (DO NOT mutate the returned
//     object externally.)
// ---------------------------------------------------------------------------

import type { ActionErrorLike, ActionInstance, RegistryListener } from "./types.js";
import { subscribe } from "./registry.js";

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
}

const snapshots = new Map<string, ActionStatus>();
let listenerInstalled = false;
let unsubscribe: (() => void) | null = null;

const registryListener: RegistryListener = (inst: ActionInstance): void => {
  const snap = snapshots.get(inst.name);
  if (snap === undefined) return; // not watched
  // Update last-dispatched for any transition.
  if (inst.dispatchedAt > snap.lastDispatchedAt) {
    snap.lastDispatchedAt = inst.dispatchedAt;
  }
  switch (inst.status) {
    case "pending":
      snap.pending++;
      break;
    case "success":
      snap.pending = Math.max(0, snap.pending - 1);
      snap.lastSuccess = inst.result;
      snap.lastSettledAt = inst.completedAt ?? Date.now();
      break;
    case "error":
      snap.pending = Math.max(0, snap.pending - 1);
      if (inst.error !== undefined) snap.lastError = inst.error;
      snap.lastSettledAt = inst.completedAt ?? Date.now();
      break;
    case "cancelled":
      snap.pending = Math.max(0, snap.pending - 1);
      snap.lastSettledAt = inst.completedAt ?? Date.now();
      break;
  }
};

/**
 * Get a live, mutable snapshot of an action's status. The returned
 * object updates in-place as the action's lifecycle progresses; do
 * NOT mutate it externally.
 *
 * First call for a given name lazily installs a registry listener.
 * Subsequent calls return the cached (same-reference) snapshot.
 *
 * @param name - Action name to observe (e.g. "settings.patch").
 * @returns A live {@link ActionStatus} snapshot (pending count, last
 *   error/success, timestamps).
 */
export function actionStatus(name: string): ActionStatus {
  let snap = snapshots.get(name);
  if (snap === undefined) {
    snap = {
      pending: 0,
      lastDispatchedAt: 0,
      lastSettledAt: 0,
    };
    snapshots.set(name, snap);
  }
  // Lazy listener install: only when at least one consumer asks.
  if (!listenerInstalled) {
    unsubscribe = subscribe(registryListener);
    listenerInstalled = true;
  }
  return snap;
}

/** Test-only: clear snapshots so tests don't bleed state. Also resets
 *  the listener-installed flag so a freshly-reset registry will get a
 *  new subscription on the next actionStatus() call. */
export function _resetForTest(): void {
  unsubscribe?.();
  unsubscribe = null;
  snapshots.clear();
  listenerInstalled = false;
}
