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
  withTimeout,
  getActionLog,
  API_TIMEOUT_MS,
  RETRY_STANDARD,
} from "@cplieger/actions";

export type {
  Action,
  ActionDefinition,
  ActionContext,
  ActionErrorLike,
  ActionInstance,
  ActionLifecycleStatus,
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
