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
export { defineAction, dispatchWithResult } from "./define.js";
export { apiAction } from "./api.js";
// transportAction is internal-only: consumed exclusively by action
// modules inside actions/ (chat.ts, editor.ts, messages.ts, crew.ts).
// External callers use defineAction or apiAction instead.

// Error class for callers throwing structured action errors from within
// run() (lets toast / retry classification see status + code).
// classifyFetchError: normalise fetch catch-block errors into ActionError
// with canonical code (cancelled/timeout/network). Useful in custom
// defineAction run() implementations that call fetch directly.
export { ActionError, hasErrorString, classifyFetchError, isRetryableError, isTransientStatus, isPermanentCode, isActionError } from "./error.js";

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
export { bindLoadingState, bindLoadingStateMulti, bindLoadingCluster, bindDisabledPattern } from "./loading.js";
export type { ClusterState, DisabledPatternOptions, DisabledPatternHandle } from "./loading.js";

// Cleanup hooks: register raw (non-action) cleanup for fetch
// controllers / timers; the framework auto-installs a beforeunload
// listener that drains everything.
export { registerCleanup } from "./cleanup.js";

// Live console logger: subscribes to the registry and emits
// console.error for every action that fails. Wired once at app init.
export { initConsoleLog } from "./console-log.js";

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
export type { DispatchOptions, RetryAttemptInfo, DispatchResult } from "./types.js";

// Standard retry config constant: eliminates `retry: { count: 2, delay: 300 }`
// repetition across action definitions.
export { RETRY_STANDARD } from "./types.js";

// Utility extraction types: pull TArgs / TResult from an Action without
// manually re-declaring them. Useful in test helpers and callback typing.
export type { ArgsOf, ResultOf, ActionFromDef } from "./types.js";

// Action and ActionContext: needed by callers that type-annotate action
// variables or write custom run() implementations receiving the context.
export type { Action, ActionContext, ActionDefinition } from "./types.js";

// Instance snapshot: needed by subscribeToActions consumers who want to
// type-annotate callback parameters.
export type { ActionInstance } from "./types.js";

// Per-name status snapshot: consolidated view of pending count, last
// error/success, and timestamps for a named action.
export { actionStatus } from "./action-status.js";
export type { ActionStatus } from "./action-status.js";
