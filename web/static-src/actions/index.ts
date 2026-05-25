// Public surface of the actions framework. Callers import only from
// here. Internal modules (registry, define) re-export through this
// to keep the public/private boundary clear.
//
// Surface is curated: every re-export below has at least one consumer
// outside the framework. Items consumed only inside actions/ are
// imported directly from their defining module by those internal
// callers and not re-exported here, so the public API stays small.
// ---------------------------------------------------------------------------

// Action factories.
export { defineAction } from "./define.js";
export { apiAction } from "./api.js";
// transportAction is internal-only: consumed exclusively by action
// modules inside actions/ (chat.ts, editor.ts, messages.ts, crew.ts).
// External callers use defineAction or apiAction instead.

// Error class for callers throwing structured action errors from within
// run() (lets toast / retry classification see status + code).
export { ActionError } from "./error.js";

// Registry surface for non-action consumers:
//   - subscribeToActions: mcp-panels uses it to capture per-dispatch
//     error metadata around the saveServer call.
//   - pendingCount: total in-flight count — drives global progress
//     indicators (app-bar loading bar).
//   - pendingForAny: OR-query for binding one element to multiple
//     action names (e.g. a Save button covering several settings
//     actions).
export { subscribe as subscribeToActions, pendingCount, pendingForAny } from "./registry.js";

// Loading-state helper: bind a button's disabled + aria-busy state
// to a named action's pending count. Returns an unsubscribe.
export { bindLoadingState } from "./loading.js";

// Cleanup hooks: register raw (non-action) cleanup for fetch
// controllers / timers; the framework auto-installs a beforeunload
// listener that drains everything.
export { registerCleanup } from "./cleanup.js";

// Live console logger: subscribes to the registry and emits
// console.error for every action that fails. Wired once at app init.
export { initActionConsoleLog } from "./console-log.js";

// Debounce helper: wrap an action so rapid calls coalesce into a
// single dispatch after a quiet window. Useful for typeahead search,
// auto-save, slash-command option fetches.
export { debouncedDispatch } from "./debounce.js";
export type { DebouncedDispatch } from "./debounce.js";

// One type used by external callers (mcp-panels narrows the registry
// listener arg). The rest of the framework's types stay internal.
export type { ActionErrorLike } from "./types.js";

// DispatchOptions: per-dispatch overrides (silent, onSuccess, onError,
// onSettled). External callers (settings.ts, notify.ts) pass these
// inline; exporting the type lets helpers type-annotate the opts arg.
export type { DispatchOptions } from "./types.js";
