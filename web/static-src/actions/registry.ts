// Action registry: in-memory log of all dispatched actions with a
// subscribe API. Fires per state transition. Bounded to a recent
// window so memory usage stays flat over a long session.
//
// Surface for:
//   - Devtools / debug overlay (show recent actions)
//   - Loading-state queries (registry.pendingFor("chat.delete"))
//   - Future: action telemetry, replay, time-travel debugging
// ---------------------------------------------------------------------------

import type { ActionInstance, RegistryListener } from "./types.js";

const MAX_LOG_SIZE = 200;

// Module-level state. The registry is intentionally a singleton — at
// most one log per page; subscribers are tab-scoped.
//
// log:    ordered ring of recent instances (newest at end).
// idMap:  id -> log index for O(1) state-transition updates.
const log: ActionInstance[] = [];
const idMap = new Map<string, number>();
const listeners = new Set<RegistryListener>();

/** Record a state transition. Called by define.ts at every status
 *  change. The instance is push-replaced (newest at end). */
export function record(instance: ActionInstance): void {
  // De-duplicate by id: replace any existing entry with the same id
  // (state transitions on the same instance overwrite, not append).
  const existing = idMap.get(instance.id);
  if (existing !== undefined) {
    log[existing] = instance;
  } else {
    log.push(instance);
    idMap.set(instance.id, log.length - 1);
    if (log.length > MAX_LOG_SIZE) {
      log.shift();
      // Indices shifted by one — reindex.
      // O(n) cost is acceptable at MAX_LOG_SIZE=200; ring buffer is the upgrade path.
      idMap.clear();
      for (let i = 0; i < log.length; i++) {
        const entry = log[i];
        if (entry !== undefined) idMap.set(entry.id, i);
      }
    }
  }
  // Snapshot listeners so a subscriber added during dispatch (or
  // removed) doesn't see this event mid-iteration. Principle of
  // least surprise: a listener added in response to event N first
  // sees event N+1.
  const snapshot = [...listeners];
  for (const fn of snapshot) {
    try {
      fn(instance);
    } catch (e) {
      // Don't let a buggy subscriber bring down the registry.
      console.error("[actions] registry listener threw", e);
    }
  }
}

/** Subscribe to all action lifecycle events. Returns an unsubscribe
 *  function. */
export function subscribe(fn: RegistryListener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Snapshot of recent action instances (newest at end). */
export function recentLog(): readonly ActionInstance[] {
  return log.slice();
}

/** Currently-pending instances of a named action. Useful for deriving
 *  loading state without an explicit observer. */
export function pendingFor(name: string): readonly ActionInstance[] {
  return log.filter((i) => i.name === name && i.status === "pending");
}

/** Test-only: clear log + listeners. */
export function _resetForTest(): void {
  log.length = 0;
  idMap.clear();
  listeners.clear();
}
