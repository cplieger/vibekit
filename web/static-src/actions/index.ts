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
export { transportAction } from "./transport.js";

// Error class + helper for callers throwing structured action errors
// from within run() (lets toast / retry classification see status + code).
export { ActionError, toActionError } from "./error.js";

// Registry surface for non-action consumers:
//   - subscribeToActions: mcp-panels uses it to capture per-dispatch
//     error metadata around the saveServer call.
//   - pendingFor: loading-state helpers + the upload guard query the
//     registry directly to gate their behavior.
export { subscribe as subscribeToActions, pendingFor } from "./registry.js";

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

// One type used by external callers (mcp-panels narrows the registry
// listener arg). The rest of the framework's types stay internal.
export type { ActionErrorLike } from "./types.js";
