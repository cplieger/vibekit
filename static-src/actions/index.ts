// Public surface of the actions framework. All primitives are now provided
// by @cplieger/actions; this module re-exports them so existing consumer
// imports (`from "./actions/index.js"`) continue to resolve unchanged.
// ---------------------------------------------------------------------------

export {
  defineAction,
  apiAction,
  transportAction,
  configure,
  configureApi,
  configureTransport,
  ActionError,
  hasErrorString,
  classifyFetchError,
  retryNetwork,
  subscribeToActions,
  subscribeByName,
  pendingCount,
  isPending,
  bindLoadingState,
  registerCleanup,
  debouncedDispatch,
  pollAction,
  pollUntil,
  getActionLog,
  RETRY_STANDARD,
  IDEMPOTENCY_HEADER,
  IDEMPOTENCY_COMMAND_FIELD,
} from "@cplieger/actions";

// Timeout composition lives in @cplieger/fetch (actions v3 stopped
// re-exporting its former duplicate copies); re-exported here so consumer
// imports stay unchanged.
export { withTimeout, API_TIMEOUT_MS } from "@cplieger/fetch";

export type {
  Action,
  ActionDefinition,
  ActionContext,
  ActionErrorLike,
  ActionInstance,
  ActionLifecycleStatus,
  ActionOutcome,
  DispatchOptions,
  DispatchHandle,
  DebouncedDispatch,
  PollOptions,
  RetryConfig,
  RequestSpec,
  Notifier,
  NotifierRetry,
  NotificationSpec,
  RegistryListener,
  TransportCommand,
  TransportSendFn,
  TransportSendResult,
  ApiConfig,
} from "@cplieger/actions";
