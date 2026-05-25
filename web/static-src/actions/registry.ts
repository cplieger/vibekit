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
const MAX_LOG_HARD = 1000;

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
      // Evict the first NON-pending entry so pendingFor() never loses
      // track of long-running actions. If all entries are pending
      // (extreme case), skip eviction this round — hard cap below
      // bounds worst case.
      let evictIdx = -1;
      for (let i = 0; i < log.length; i++) {
        if (log[i]!.status !== "pending") { evictIdx = i; break; }
      }
      if (evictIdx !== -1) {
        const evictedId = log[evictIdx]!.id;
        log.splice(evictIdx, 1);
        idMap.delete(evictedId);
        // Decrement indices above the eviction point.
        for (const [id, idx] of idMap) {
          if (idx > evictIdx) idMap.set(id, idx - 1);
        }
      }
    }
    // Hard cap: force-evict oldest entry regardless of pending status
    // to bound memory in extreme runaway scenarios (B5/B6).
    if (log.length > MAX_LOG_HARD) {
      const evictedId = log[0]!.id;
      log.splice(0, 1);
      idMap.delete(evictedId);
      for (const [id, idx] of idMap) {
        idMap.set(id, idx - 1);
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

/** @internal Test-only public surface. */
export function recentLog(): readonly ActionInstance[] {
  return log.slice();
}

/** Currently-pending instances of a named action. Useful for deriving
 *  loading state without an explicit observer. */
export function pendingFor(name: string): readonly ActionInstance[] {
  return log.filter((i) => i.name === name && i.status === "pending");
}

/** Total count of pending action instances across all action names.
 *  Useful for an app-bar global progress indicator: when > 0, show
 *  some "doing things" affordance. */
export function pendingCount(): number {
  let n = 0;
  for (const i of log) {
    if (i.status === "pending") n++;
  }
  return n;
}

/** True if any of the named actions has at least one pending instance.
 *  Useful for binding a single UI element's loading state to multiple
 *  action names (e.g. a Save button bound to ["settings.patch",
 *  "settings.save_steering"]). */
export function pendingForAny(names: readonly string[]): boolean {
  if (names.length === 0) return false;
  // Set lookup avoids quadratic scan when names is large.
  const set = new Set(names);
  for (const i of log) {
    if (i.status === "pending" && set.has(i.name)) return true;
  }
  return false;
}

/** Test-only: clear log + listeners. */
export function _resetForTest(): void {
  log.length = 0;
  idMap.clear();
  listeners.clear();
}
