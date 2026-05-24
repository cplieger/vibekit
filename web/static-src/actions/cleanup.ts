// Global cleanup: cancel all in-flight actions + run registered
// cleanup hooks. Wired to window.beforeunload so navigation away
// from the page (or tab close) aborts everything cleanly.
//
// Cleanup hooks must be idempotent. cancelAllPending is allowed to fire
// multiple times (e.g., cancelled navigation followed by confirmed
// navigation). Aborting an already-aborted controller is a no-op by spec;
// ensure your hook has equivalent semantics. This is a known limitation of
// beforeunload-based cleanup. Cancelled navigation followed by confirmed
// navigation will fire cancelAllPending twice; ensure your hooks tolerate this.
//
// Two surfaces:
//
//   1. Actions registered via defineAction are auto-tracked. Their
//      .cancel() is called on global cleanup.
//
//   2. Modules with raw fetch controllers (transport.ts inflight,
//      msgControllers in store.ts, browserFetchHolder in files.ts,
//      pollGitHubDevice timer chain) call registerCleanup(fn) at
//      init time. The fn is invoked on global cleanup.
//
// Test-only: cancelAllPending() is exported so tests can invoke it
// directly without dispatching a beforeunload event. _resetForTest
// clears the registries.
// ---------------------------------------------------------------------------

import type { Action } from "./types.js";

const trackedActions = new Set<Action<unknown, unknown>>();
const cleanupHooks = new Set<() => void>();
let beforeunloadInstalled = false;

/** Internal: register an Action so cancelAllPending() can iterate it.
 *  Called from defineAction(); not part of the public API. */
export function _registerAction<TArgs, TResult>(action: Action<TArgs, TResult>): void {
  trackedActions.add(action as Action<unknown, unknown>);
  installBeforeunloadOnce();
}

/** Register a cleanup function to run on page unload (or test invoke).
 *  Use this for raw fetch controllers, timer chains, polling loops,
 *  or any in-flight work outside the action framework that should
 *  abort on navigation.
 *
 *  Returns an unregister function so a module that re-initializes can
 *  detach its old hook. */
export function registerCleanup(fn: () => void): () => void {
  cleanupHooks.add(fn);
  installBeforeunloadOnce();
  return () => cleanupHooks.delete(fn);
}

/** Cancel every in-flight action + run every cleanup hook. Errors from
 *  individual hooks are caught + logged; one bad hook does not stop
 *  the rest from running. */
export function cancelAllPending(): void {
  for (const action of [...trackedActions]) {
    try {
      action.cancel();
    } catch (e) {
      console.error(`[actions] cancel for ${action.name} threw`, e);
    }
  }
  // Snapshot before iterating: cleanup hooks may register/unregister
  // others as they run (e.g. during test resets).
  for (const fn of [...cleanupHooks]) {
    try {
      fn();
    } catch (e) {
      console.error("[actions] cleanup hook threw", e);
    }
  }
}

function installBeforeunloadOnce(): void {
  if (beforeunloadInstalled) return;
  beforeunloadInstalled = true;
  if (typeof window !== "undefined") {
    // Use beforeunload (vs pagehide) because we want to fire BEFORE
    // the navigation begins so in-flight requests can actually be
    // aborted client-side. pagehide is fired too late on most browsers.
    window.addEventListener("beforeunload", cancelAllPending);
  }
}

/** Test-only: clear both registries + uninstall the listener. */
export function _resetForTest(): void {
  trackedActions.clear();
  cleanupHooks.clear();
  if (beforeunloadInstalled && typeof window !== "undefined") {
    window.removeEventListener("beforeunload", cancelAllPending);
    beforeunloadInstalled = false;
  }
}
