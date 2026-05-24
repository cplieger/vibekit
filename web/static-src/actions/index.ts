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

// Types — re-exported for external typing of action definitions.
export type {
  Action,
  ActionDefinition,
  ActionInstance,
  ActionStatus,
  ActionErrorLike,
  DispatchOptions,
  OptimisticOp,
  RegistryListener,
  RequestSpec,
  ToastSpec,
} from "./types.js";

export type { ApiActionDefinition } from "./api.js";
export type { TransportActionDefinition } from "./transport.js";
