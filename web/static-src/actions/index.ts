// Public surface of the actions framework. Callers import only from
// here. Internal modules (registry, define, error) re-export through
// this to keep the public/private boundary clear.
//
// Surface is curated: every re-export below has at least one consumer
// outside the framework. Items consumed only inside actions/ are
// imported directly from their defining module by those internal
// callers and not re-exported here, so the public API stays small.
// ---------------------------------------------------------------------------

// Action factories. transportAction is internal-only — consumed
// exclusively by action modules inside actions/ (chat.ts, editor.ts,
// messages.ts, crew.ts). External callers use defineAction or apiAction.
export { defineAction } from "./define.js";
export { apiAction } from "./api.js";

// Error class for callers throwing structured action errors from within
// run() (toast / retry classification reads status + code from this).
// hasErrorString: predicate for narrowing parsed JSON bodies that may
// have an `{ error: "..." }` shape — used by api-client/transport/upload.
// classifyFetchError: normalise fetch catch-block errors into ActionError
// with canonical code (cancelled/timeout/network); used by action defs
// (conflicts.ts, files.ts) that call fetch directly outside apiAction.
export { ActionError, hasErrorString, classifyFetchError } from "./error.js";

// Registry surface for non-action consumers:
//   - subscribeToActions: 4 consumers — mcp-panels (capture per-dispatch
//     error metadata), save-indicator (settings batch detection),
//     app.ts boot wiring, commands-menu (post-fetch state).
//   - pendingCount: total in-flight count — drives the global progress
//     indicator in app.ts and supervised-pill.
//   - pendingForAny: OR-query for the settings batch-settled detection
//     pattern in save-indicator.
export { subscribe as subscribeToActions, pendingCount, pendingForAny } from "./registry.js";

// Loading-state helper: bind a button's disabled + aria-busy state to
// one or more named actions' pending count. Overloaded — pass a single
// name or an array.
export { bindLoadingState } from "./loading.js";

// Cleanup hooks: register raw (non-action) cleanup for fetch
// controllers / timers; the framework auto-installs a beforeunload
// listener that drains everything.
export { registerCleanup } from "./cleanup.js";

// Live console logger: subscribes to the registry and emits
// console.error for every action that fails. Wired once at app init.
export { initConsoleLog } from "./console-log.js";

// Debounce helper: wrap an action so rapid calls coalesce into a
// single dispatch after a quiet window. Used by typeahead search and
// slash-command option fetches.
export { debouncedDispatch } from "./debounce.js";
export type { DebouncedDispatch } from "./debounce.js";

// Standard retry config constant: eliminates `retry: { count: 2, delay: 300 }`
// repetition across action definitions.
export { RETRY_STANDARD } from "./types.js";

// Type exports — consumed by callers that type-annotate their action
// variables, options, or context parameters. Kept minimal: only types
// that callers reference explicitly.
export type {
  Action,             // type-annotate action variables
  ActionDefinition,   // type-annotate action factory args
  ActionContext,      // 3rd arg to run() in custom defineAction calls
  DispatchOptions,    // type-annotate dispatch opts
  ActionErrorLike,    // mcp-panels narrows the registry listener arg
  RetryConfig,        // custom retry configurations
} from "./types.js";
