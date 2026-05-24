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
const log: ActionInstance[] = [];
const listeners = new Set<RegistryListener>();

/** Record a state transition. Called by define.ts at every status
 *  change. The instance is push-replaced (newest at end). */
export function record(instance: ActionInstance): void {
  // De-duplicate by id: replace any existing entry with the same id
  // (state transitions on the same instance overwrite, not append).
  const existing = log.findIndex((i) => i.id === instance.id);
  if (existing !== -1) {
    log[existing] = instance;
  } else {
    log.push(instance);
    if (log.length > MAX_LOG_SIZE) log.shift();
  }
  for (const fn of listeners) {
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
  listeners.clear();
}
