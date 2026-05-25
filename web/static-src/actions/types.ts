// Action framework types: defines the contract between callers, the
// dispatcher, and observers. Pure types — no imports, no runtime
// behavior — so any module in the codebase can depend on this without
// pulling in transport / toast / store.
// ---------------------------------------------------------------------------

/** Lifecycle status of a single dispatched action instance. */
export type ActionStatus =
  | "pending"     // optimistic ran (if any), run() in flight
  | "success"     // run() resolved
  | "error"       // run() threw; rollback ran
  | "cancelled";  // dispatch().cancel() called or AbortController fired

/** Errors thrown by an action's run() function. ActionError subclass
 *  in error.ts attaches HTTP status + server error code metadata. */
export interface ActionErrorLike {
  readonly message: string;
  readonly status?: number;   // HTTP status if applicable
  readonly code?: string;     // server-side error code
  readonly cause?: unknown;
}

/** Snapshot of a single in-flight or historical action invocation.
 *  Stored in the registry log for observability. */
export interface ActionInstance<TArgs = unknown, TResult = unknown> {
  readonly id: string;            // ULID-like; unique per dispatch
  readonly name: string;          // matches ActionDefinition.name
  readonly status: ActionStatus;
  readonly args: TArgs;
  readonly startedAt: number;     // Date.now() at dispatch
  readonly completedAt?: number;  // Date.now() at terminal state
  readonly result?: TResult;      // present iff status === "success"
  readonly error?: ActionErrorLike; // present iff status === "error"
}

/** Opaque value the optimistic() function returns and rollback()
 *  receives. Use it to thread "what to undo" across the lifecycle. */
export type OptimisticOp = unknown;

/** Toast wiring: either a literal string (used as-is), or a function
 *  computed from action args + result/error at call time. Pass false
 *  to opt out of the default toast for that branch. */
export type ToastSpec<TArgs, TPayload> =
  | string
  | ((args: TArgs, payload: TPayload) => string)
  | false;

/** The declarative shape passed to defineAction(). All optional except
 *  name + run. */
export interface ActionDefinition<TArgs, TResult> {
  /** Stable identifier, e.g. "chat.delete", "files.create".
   *  Used in the registry log + as a default toast prefix. */
  readonly name: string;

  /** The work the action performs. Must throw ActionError on failure
   *  (or any Error — wrappers will normalise). The signal aborts when
   *  dispatch().cancel() is called or the optimistic AbortController
   *  is torn down. */
  run: (args: TArgs, signal: AbortSignal) => Promise<TResult>;

  /** Optional optimistic mutation. Runs synchronously before run().
   *  Return any state you need to undo the mutation in rollback().
   *  If run() succeeds, the optimistic mutation stays. If it fails,
   *  rollback() is called with the OptimisticOp. */
  optimistic?: (args: TArgs) => OptimisticOp | undefined;

  /** Undo the optimistic mutation. Called only if run() throws. */
  rollback?: (args: TArgs, op: OptimisticOp | undefined, err: ActionErrorLike) => void;

  /** Toast on success. Default: no toast (success is usually obvious
   *  from UI updates). Pass a string or function to enable. */
  success?: ToastSpec<TArgs, TResult>;

  /** Toast on error. Default: the action name humanised + the server
   *  error message. Pass a string prefix to customise, a function
   *  for full control, or false to suppress (rare; only for actions
   *  that surface errors via banner or inline). */
  error?: ToastSpec<TArgs, ActionErrorLike>;

  /** When set, the error toast renders a "Retry" button that re-dispatches
   *  the action with the same args.
   *
   *    "network": only show retry for network/timeout failures
   *               (status === 0 or code === "timeout") — safest default
   *               since 4xx/5xx may indicate a permanent rejection
   *               that re-dispatching won't fix.
   *    "always":  always show retry on error (use only for fully
   *               idempotent actions).
   *    false / undefined (default): no retry button.
   *
   *  Suppressed when `error: false` (no toast renders at all). */
  retryable?: "network" | "always" | false;

  /** Auto-retry transient failures BEFORE surfacing the error toast.
   *  Each attempt re-runs the action's `run()` (NOT optimistic — the
   *  optimistic mutation persists across retries). Only fires when
   *  the error matches `retryable`'s classifier ('network' or 'always').
   *
   *  Defaults to no auto-retry (count: 0). When set, errors that the
   *  retry would catch are silently re-tried up to `count` times with
   *  exponential backoff (delay × factor^n, capped at 5s) before the
   *  user sees a toast. The Retry button (if `retryable` is set) still
   *  appears once auto-retry is exhausted.
   *
   *  Example: `retry: { count: 2, delay: 300 }` — total ~900ms latency
   *  budget for transient blips, invisible to the user when successful. */
  retry?: {
    readonly count: number;        // additional attempts beyond the first
    readonly delay: number;        // ms before first retry
    readonly factor?: number;      // multiplier per retry (default 2)
  };

  /** When two dispatches share the same scope, the second waits for
   *  the first to finish (success / error / cancel) before its
   *  optimistic + run begin. Without scope, dispatches run in parallel.
   *
   *  Use cases: serialize git mutations on the same repo, settings
   *  patches that share state, batch-delete operations. Default: no
   *  scope (parallel dispatches allowed).
   *
   *  Scope can be a static string (one queue for the whole action) or
   *  a function of args (per-resource queue, e.g. per-repo, per-chatID). */
  scope?: string | ((args: TArgs) => string);
}

/** A registered action, returned by defineAction(). Can be dispatched
 *  many times; each dispatch is a separate instance with its own
 *  cancellation token. */
export interface Action<TArgs, TResult> {
  readonly name: string;

  /** Run the action. Resolves to the result on success or null on
   *  failure or cancellation. The toast (if configured) has already
   *  fired by the time dispatch resolves. */
  dispatch(args: TArgs, opts?: DispatchOptions<TArgs, TResult>): Promise<TResult | null>;

  /** Cancel all in-flight instances. Each instance moves to status
   *  "cancelled" and run()'s signal aborts. Rollback IS called on
   *  cancellation (the optimistic mutation should be undone). */
  cancel(): void;
}

/** Per-dispatch overrides. */
export interface DispatchOptions<TArgs = unknown, TResult = unknown> {
  /** Suppress the success toast for this call. Errors still toast. */
  readonly silent?: boolean;
  /** Override the success toast message for this call. */
  readonly successMessage?: string;
  /** Override the error toast prefix for this call. */
  readonly errorPrefix?: string;
  /** Per-call success callback. Fires after the action-level success
   *  toast (if any). Receives the resolved value. Useful when a
   *  specific callsite needs to react (focus an input, scroll, etc.)
   *  without changing the action definition. */
  readonly onSuccess?: (result: TResult, args: TArgs) => void;
  /** Per-call error callback. Fires after the action-level error
   *  toast (if any). Useful for callsite-specific recovery UI. */
  readonly onError?: (err: ActionErrorLike, args: TArgs) => void;
  /** Per-call settled callback. Fires for both success and error
   *  AND on cancellation. Useful for clearing per-call loading
   *  state that bindLoadingState doesn't cover. */
  readonly onSettled?: (args: TArgs) => void;
}

/** A request descriptor used by apiAction(). Mirrors the api-client
 *  shape so apiAction can pipe straight into apiPostOrError. GET is
 *  included for read actions that want toast/cancellation semantics
 *  without writing a custom defineAction. */
export type RequestSpec =
  | { readonly method: "GET"; readonly path: string }
  | { readonly method: "POST" | "PUT" | "PATCH" | "DELETE"; readonly path: string; readonly body?: unknown };

// (TransportSpec was an unused parallel descriptor — transportAction
// uses TypedCommand | Command from transport.ts directly. Removed.)

/** Subscriber callback for the registry. Fires once per state
 *  transition (pending -> success/error/cancelled). */
export type RegistryListener = (instance: ActionInstance) => void;
