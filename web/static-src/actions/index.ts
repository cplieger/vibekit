// Public surface of the actions framework. Callers import only from
// here. Internal modules (registry, define) re-export through this
// to keep the public/private boundary clear.
// ---------------------------------------------------------------------------

export { defineAction } from "./define.js";
export { apiAction } from "./api.js";
export { transportAction } from "./transport.js";
export { ActionError, toActionError } from "./error.js";

// Registry surface — for devtools, loading-state queries, telemetry.
export { recentLog, subscribe as subscribeToActions, pendingFor } from "./registry.js";

// Loading-state helper: bind a button/input's disabled + aria-busy
// state to a named action's pending count. Returns an unsubscribe.
export { bindLoadingState } from "./loading.js";
export type { BindLoadingOptions } from "./loading.js";

// Cleanup hooks: cancel all in-flight actions on page unload, and
// register raw (non-action) cleanup for fetch controllers / timers.
export { registerCleanup, cancelAllPending } from "./cleanup.js";

// Persisted error tail — last N action errors saved to localStorage
// for inclusion in a future bug-report flow.
export {
  initErrorTail,
  getRecentErrors,
  clearRecentErrors,
} from "./error-tail.js";
export type { PersistedError } from "./error-tail.js";

// Types — re-exported for external typing of action definitions.
export type {
  Action,
  ActionDefinition,
  ActionInstance,
  ActionStatus,
  ActionErrorLike,
  DispatchOptions,
  OptimisticOp,
  RequestSpec,
  ToastSpec,
  RegistryListener,
} from "./types.js";

export type { ApiActionDefinition } from "./api.js";
export type { TransportActionDefinition } from "./transport.js";
