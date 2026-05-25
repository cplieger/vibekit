// Action registry: in-memory log of all dispatched actions with a
// subscribe API. Fires per state transition. Bounded to a recent
// window so memory usage stays flat over a long session.
//
// Surface for:
//   - Devtools / debug overlay (show recent actions)
//   - Loading-state queries (registry.pendingFor("chat.delete"))
//   - Future: action telemetry, replay, time-travel debugging
//
// Performance: eviction uses a head-pointer + tombstones instead of
// splice + O(n) index re-computation. State-transition updates use a
// direct id→instance Map (no index bookkeeping). Listener notification
// iterates the Set directly (no per-call array snapshot allocation).
// Net effect: record() is O(1) amortized in steady state.
// ---------------------------------------------------------------------------

import type { ActionInstance, RegistryListener } from "./types.js";

const MAX_LOG_SIZE = 200;
const MAX_LOG_HARD = 1000;

// Module-level state. The registry is intentionally a singleton — at
// most one log per page; subscribers are tab-scoped.
//
// log:       ordered ring of recent instances (newest at end). Evicted
//            slots are null (tombstones); head tracks the first live slot.
// idMap:     id -> ActionInstance for O(1) state-transition updates.
const log: (ActionInstance | null)[] = [];
const idMap = new Map<string, ActionInstance>();
const listeners = new Set<RegistryListener>();
// Incremental count of currently-pending instances. Maintained in
// record() at status transitions; pendingCount() returns this in O(1).
let _pendingN = 0;
// Number of live (non-null) entries in the log.
let _liveCount = 0;
// Head pointer: first potentially-live index. Entries before head are
// guaranteed null (compacted away).
let _head = 0;

/** Advance head past leading nulls and compact when prefix is large. */
function compact(): void {
  while (_head < log.length && log[_head] === null) _head++;
  if (_head > 256) {
    log.splice(0, _head);
    _head = 0;
  }
}

/** Record a state transition. Called by define.ts at every status
 *  change. The instance is push-replaced (newest at end). */
export function record(instance: ActionInstance): void {
  // De-duplicate by id: replace any existing entry with the same id
  // (state transitions on the same instance overwrite, not append).
  const existing = idMap.get(instance.id);
  if (existing !== undefined) {
    // Update incremental counter on transitions.
    if (existing.status === "pending" && instance.status !== "pending") _pendingN--;
    else if (existing.status !== "pending" && instance.status === "pending") _pendingN++;
    // Overwrite in the log array. Scan from the end (most recent
    // transitions are near the tail — typically last or second-to-last).
    for (let i = log.length - 1; i >= _head; i--) {
      if (log[i] !== null && log[i]!.id === instance.id) {
        log[i] = instance;
        break;
      }
    }
    idMap.set(instance.id, instance);
  } else {
    log.push(instance);
    idMap.set(instance.id, instance);
    _liveCount++;
    if (instance.status === "pending") _pendingN++;
    if (_liveCount > MAX_LOG_SIZE) {
      // Evict the first NON-pending live entry so pendingFor() never
      // loses track of long-running actions.
      for (let i = _head; i < log.length; i++) {
        const entry = log[i] ?? null;
        if (entry !== null && entry.status !== "pending") {
          idMap.delete(entry.id);
          log[i] = null;
          _liveCount--;
          break;
        }
      }
    }
    // Hard cap: force-evict oldest live entry regardless of pending
    // status to bound memory in extreme runaway scenarios (B5/B6).
    if (_liveCount > MAX_LOG_HARD) {
      for (let i = _head; i < log.length; i++) {
        const entry = log[i] ?? null;
        if (entry !== null) {
          if (entry.status === "pending") _pendingN--;
          idMap.delete(entry.id);
          log[i] = null;
          _liveCount--;
          break;
        }
      }
    }
    compact();
  }
  // Defensive: counter should never go negative; clamp to 0 if it
  // does (would indicate a record() invariant violation).
  if (_pendingN < 0) {
    console.warn("[actions] _pendingN went negative — invariant violation; clamping to 0");
    _pendingN = 0;
  }
  // Notify listeners. Iterate the Set directly — safe because we
  // catch per-listener errors. Set iteration semantics: entries
  // deleted during iteration are not re-visited; entries added during
  // iteration ARE visited. This matches the previous snapshot behavior
  // for the removal case (unsubscribe mid-iteration is safe) while
  // avoiding an array allocation on every record() call. The add-
  // during-iteration case (new subscriber sees current event) is an
  // acceptable semantic change — the previous code prevented it, but
  // no production code relies on that guarantee.
  for (const fn of listeners) {
    try {
      fn(instance);
    } catch (e) {
      // Don't let a buggy subscriber bring down the registry.
      console.error("[actions] registry listener threw", e);
    }
  }
}

/**
 * Subscribe to all action lifecycle events (pending/success/error/cancelled).
 *
 * @param fn - Listener invoked on each state transition. Errors thrown
 *   by the listener are caught and logged (never propagate to the dispatcher).
 * @returns An unsubscribe function.
 */
export function subscribe(fn: RegistryListener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** @internal Test-only public surface. */
export function recentLog(): readonly ActionInstance[] {
  const result: ActionInstance[] = [];
  for (let i = _head; i < log.length; i++) {
    const entry = log[i] ?? null;
    if (entry !== null) result.push(entry);
  }
  return result;
}

/** Currently-pending instances of a named action. Useful for deriving
 *  loading state without an explicit observer. */
export function pendingFor(name: string): readonly ActionInstance[] {
  const result: ActionInstance[] = [];
  for (let i = _head; i < log.length; i++) {
    const entry = log[i] ?? null;
    if (entry !== null && entry.name === name && entry.status === "pending") {
      result.push(entry);
    }
  }
  return result;
}

/** Total count of pending action instances across all action names.
 *  Useful for an app-bar global progress indicator: when > 0, show
 *  some "doing things" affordance. O(1). */
export function pendingCount(): number {
  return _pendingN;
}

/**
 * True if any of the named actions has at least one pending instance.
 * Uses a Set internally to avoid quadratic scan when `names` is large.
 *
 * @param names - Action names to check (e.g. ["settings.patch", "settings.save_steering"]).
 * @returns `true` if at least one instance with a matching name is pending.
 */
export function pendingForAny(names: readonly string[]): boolean {
  if (names.length === 0) return false;
  // Set lookup avoids quadratic scan when names is large.
  const set = new Set(names);
  for (let i = _head; i < log.length; i++) {
    const entry = log[i] ?? null;
    if (entry !== null && entry.status === "pending" && set.has(entry.name)) return true;
  }
  return false;
}

/** Test-only: clear log + listeners. */
export function _resetForTest(): void {
  log.length = 0;
  _head = 0;
  _liveCount = 0;
  idMap.clear();
  listeners.clear();
  _pendingN = 0;
}
