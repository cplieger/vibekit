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

// Devtools overlay — Ctrl+Shift+A toggles a floating panel showing
// recent action lifecycle events. Wire once at app startup.
export { initDevtoolsOverlay, toggle as toggleDevtools } from "./devtools.js";

// Telemetry adapter — opt-in subscriber that emits action lifecycle
// metadata (no args / no result) to a configurable sink.
export { initTelemetry } from "./telemetry.js";
export type { TelemetryEvent, TelemetryOptions } from "./telemetry.js";

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
