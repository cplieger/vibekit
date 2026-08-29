// ---------------------------------------------------------------------------
// Client-side type definitions.
//
// Wire-format interfaces (Message, ChatHeader, ToolCall, etc.) and their
// enums are generated from the Go server's api/* structs at build time.
// They live in ./wire/types.gen.ts and are re-exported below as the
// canonical types for the rest of the app. Edit the Go side and re-run
// `go run ./cmd/wire-codegen` to update them.
//
// Client-only types (Session projection, ConnectionStatus, ModelInfo,
// BannerLevel, PendingTrust*) are declared here.
// ---------------------------------------------------------------------------

// --- Wire types (re-exported from generated) ---

export type {
  // Enums
  AlwaysAllowBlock,
  DecisionKind,
  ErrorCode,
  EventKind,
  ForgeKind,
  PlanStatus,
  Role,
  SafetyStatus,
  SettledBy,
  StopReason,
  TabKind,
  ToolKind,
  ToolStatus,
  Transport,
  // Domain shapes
  AccountUsage,
  AccountUsageBreakdown,
  ApprovalFile,
  Attachment,
  ChatHeader,
  FileChange,
  Message,
  Block,
  CodeReference,
  RefusalInfo,
  ToolDisclosed,
  ToolDenial,
  ToolDenialRule,
  MeteringItem,
  PermissionOption,
  PlanEntry,
  PolicyRule,
  PolicyView,
  PolicyExplainResult,
  SecurityProfile,
  SessionEffortLevel,
  SessionMode,
  SessionModel,
  TabList,
  TabSubject,
  ToolCall,
  ToolDiff,
  ToolLocation,
  Usage,
  // SSE payloads
  ChatDeletedPayload,
  CodeReferencesPayload,
  ConnectedPayload,
  DecisionSettledPayload,
  DraftChangedPayload,
  ElicitationNeededPayload,
  UserInputNeededPayload,
  UserInputOption,
  UserInputSubOption,
  ElicitationPropertySchema,
  ElicitationRequestSchema,
  ErrorPayload,
  GovernanceFeatures,
  GovernanceStatePayload,
  MCPConnectedPayload,
  MCPDisconnectedPayload,
  MCPFailedPayload,
  MCPOAuthPayload,
  MessageChunkPayload,
  OpenExternalURLPayload,
  PermissionNeededPayload,
  PermissionsChangedPayload,
  PolicyErrorPayload,
  SafetyProperty,
  SafetyStatusPayload,
  SafetyPropertiesPayload,
  ToolCallPayload,
  ToolCallUpdatePayload,
  CatalogInfo,
  AptPackage,
  Inventory,
  Job,
  JobResponse,
  JobsResponse,
  RemoveResponse,
  SearchHit,
  SearchResponse,
  ToolInfo,
  ToolJobChangedPayload,
  ToolJobOutputPayload,
  RunStartedPayload,
  RunProgressPayload,
  RunFinishedPayload,
  RunStepPayload,
  Recipe,
  RecipesResponse,
  RunLaunchRequest,
  RunLaunchedResponse,
  SystemTool,
  SteerQueuedPayload,
  SteerInjectedPayload,
  SteerClearedPayload,
  AgentNoticePayload,
  TabsChangedPayload,
  TextSpan,
  TerminalCreatedPayload,
  TerminalOutputPayload,
  TerminalExitedPayload,
  TurnEndedPayload,
  TurnStatePayload,
} from "./wire/types.gen.js";

// PermissionNeeded is the legacy alias used at call sites that predate
// the generated naming. The generated type is PermissionNeededPayload.
export type { PermissionNeededPayload as PermissionNeeded } from "./wire/types.gen.js";

import type {
  Message,
  SessionEffortLevel,
  SessionMode,
  SessionModel,
  Usage,
} from "./wire/types.gen.js";

// --- Client-only types ---

/** Severity level for banner notifications. Shared by handlers/turn.ts
 *  and banner-stack.ts to avoid duplicate declarations. */
export type BannerLevel = "error" | "warning" | "info";

/** SSE event envelope sent by the server. The payload is decoded by
 *  registered SSE decoders before reaching handlers; see ./bus.ts. */
import type { SSEPayloads } from "./bus.js";
export interface ServerEvent {
  type: keyof SSEPayloads;
  chat_id?: string;
  payload?: unknown;
}

/** Prelaunch model-picker entry, mapped from GET /api/config-template's
 *  SessionModel list (kiro-cli 2.14 _kiro/config/template). Distinct from
 *  the per-session SessionModel that arrives over the bridge — this shape
 *  exists for the picker cache before any session spawns. */
export interface ModelInfo {
  model_name: string;
  model_id: string;
  rate_multiplier: number;
  description?: string;
  /** Whether this model has reasoning-effort levels at all (KAS
   *  `_meta.kiro.hasEffort`). Absent on every model = the catalog does not carry
   *  the capability, which the effort control reads as "show it anyway". */
  has_effort?: boolean;
  /** This model's own default tier (`_meta.kiro.defaultEffortLevel`), which
   *  kiro-cli's own picker labels `[default]`. The pre-session fallback for the
   *  live-level highlight: before a session exists there is no currentValue to
   *  read, and a chat with no pick of its own would otherwise render with nothing
   *  selected. The tier LIST is not a per-model field — see Session.effort_levels. */
  default_effort_level?: string;
}

/** Connection status flag, surfaced through the status bar. */
export type ConnectionStatus = "connecting" | "connected" | "disconnected";

/** One mid-turn steer the agent has NOT read yet: a row in the bottom dock.
 *
 *  THE CLIENT WRITES INTENT, THE SERVER WRITES FACT. That is the whole rule,
 *  and it replaced an earlier one ("every field here is written by an SSE
 *  event ... rather than by the code that sent it") that this shape no longer
 *  obeys. Sending a steer writes a `pending` entry immediately — the user
 *  pressed Send and is owed a row for it — and `chat.steer`'s `rollback`
 *  un-draws that entry if the POST fails, in the same gesture that puts the
 *  text back in the composer. Everything else is still the server's: the
 *  confirmed id (`steer_queued`), the promotion into the transcript
 *  (`steer_injected`) and the drop at a turn boundary (`steer_cleared`).
 *
 *  A `pending` entry renders as "Sending" and NOTHING else — no Edit, no
 *  Discard, because there is no server-side id to clear yet. Reconciliation is
 *  by id (the client derives `steer-<messageID>`, which is what KAS returns),
 *  and if that convention ever drifts, by exact text against the OLDEST
 *  pending entry. Both branches adopt onto the existing entry rather than
 *  appending, so the optimistic entry and the confirmed one can never both
 *  render as two rows.
 *
 *  Always the USER's own message. An agent's progress notice travels the same
 *  KAS buffer but arrives as `agent_notice`, so nothing here needs a field for
 *  whose words it holds.
 *
 *  A steer LEAVES this array when the agent reads it or a boundary drops it,
 *  and becomes a `SteerMark` — see below. So the array is strictly "waiting",
 *  which is why there is no `injected` flag any more: the dock's lifetime is
 *  the turn, and a mark's lifetime is the loaded transcript.
 *
 *  It replaced a `QueuedPrompt` FIFO that held text until `turn_ended` and then
 *  sent it as a NEW turn — so a correction always arrived after the work it was
 *  correcting had finished. A steer lands in the turn already running. */
export interface PendingSteer {
  /** KAS's own steer id (`steer-<uuid>`), and the key for every lifecycle
   *  event. Client-minted and echoed back, so the row that appears is the
   *  one this device sent. */
  id: string;
  text: string;
  /** Written on SUBMIT and cleared by `steer_queued`: this row is the local
   *  claim that a POST is in flight, and its id is derived rather than
   *  confirmed. Absent on every entry the server has acknowledged. */
  pending?: boolean;
}

/** Where in a running turn a steer was read.
 *
 *  BLOCK granularity, not message granularity, and that is forced: KAS calls
 *  `StartTurn` once per turn, so a whole turn accumulates into ONE assistant
 *  message and "chronologically at the injection point" can only mean between
 *  two of that message's blocks. */
export interface SteerAnchor {
  /** The assistant message the steer landed in. Empty when the steer was read
   *  before the turn had produced anything; the store rebinds it to the first
   *  assistant message that arrives. */
  msgID: string;
  /** How many blocks that message had at the moment of promotion. The mark
   *  renders immediately before the block at this index. */
  blockIndex: number;
}

/** A steer that has LEFT the dock and now renders inside the turn transcript.
 *
 *  Two ways in, and the row says which: the agent read it (`steer_injected`),
 *  or a turn boundary dropped it unread (`steer_cleared` → `dropped`). The
 *  second is worth a row precisely because it is the fact a reader would
 *  otherwise never learn — "I sent this and the agent never saw it".
 *
 *  Its lifetime is the LOADED TRANSCRIPT rather than the turn, which is why it
 *  is a separate field from `steers`: the dock empties at every boundary and
 *  this must survive one. */
export interface SteerMark {
  id: string;
  text: string;
  /** What the agent said it DID about the steer, from the acknowledgement
   *  marker KAS asks it to close its response with. Arrives later than the read
   *  frame and on a different channel (the text stream, not the steering one),
   *  because reading a steer and acting on it are separate moments. Absent
   *  until the marker closes, and absent for good if the agent never emits
   *  one. */
  ack?: string;
  /** The agent never read it: a turn boundary cleared the buffer first. */
  dropped?: boolean;
  anchor: SteerAnchor;
}

// --- Local session state (client-only projection of server chat) ---

/** How much of a chat's transcript is resident client-side.
 *
 *  - `loaded`: a successful newest-page fetch put the paginated window here —
 *    the ONLY writer of this value, so nothing weaker can claim it.
 *  - `evicted`: the idle sweep dropped the window; the session ROW survives
 *    with its header data, and activation must refetch.
 *  - `partial`: background ingest (SSE) landed on an evicted chat, so SOME
 *    messages are resident but the window around them is not.
 *
 *  Absent means the window was never loaded at all (a fresh or boot-listed
 *  chat), which every consumer treats exactly like not-`loaded`. */
export type MessagesResidency = "loaded" | "evicted" | "partial";

export interface Session {
  id: string;
  name: string;
  model: string;
  acp_session_id: string;
  current_mode_id: string;
  available_modes: SessionMode[];
  available_models: SessionModel[];
  /** The reasoning-effort tiers this session offers (the `effortLevel` config
   *  option's own choices). Empty means the current model has no tiers, which is
   *  what kiro-cli's TUI treats as "effort is not available on this model". */
  effort_levels?: SessionEffortLevel[];
  /** The tier the session is RUNNING at (that option's currentValue). Distinct
   *  from `effort`, which is what this chat CHOSE: a chat that never picked has
   *  an empty `effort` and still runs at a level. */
  effort_active?: string;
  usage: Usage;
  messages: Message[];
  message_count: number;
  has_more: boolean;
  thinking: boolean;
  working_label: string;
  /** Agent-declared activity status from the KAS focus_update channel
   *  (chat_status SSE): "in_progress" | "waiting_on_user" | "completed" |
   *  "idle". Client-only and ephemeral — cleared on the next prompt send
   *  and on a transport gap, never persisted. */
  agent_status?: string;
  /** Agent-declared one-line description of what it is working on
   *  (chat_status SSE). Shown as the chat tab's tooltip. */
  agent_status_text?: string;
  /** This chat's last turn or bridge operation failed (`error` SSE naming this
   *  chat). Client-only and latched, for the same reason `agent_status` is: the
   *  failure is a settled fact until the next turn, and a background chat's
   *  failure is otherwise invisible — `handlers/turn.ts` deliberately routes the
   *  error PROSE only for the active chat, to keep one chat's failure off
   *  another's send button. Cleared by the next `setThinking(id, true)` and by
   *  the transport-gap reconciler, exactly like `agent_status`. */
  turn_failed?: boolean;
  /** This chat's last turn finished. Client-only and latched, the mirror of
   *  `turn_failed`.
   *
   *  It exists because the agent-declared `completed` status is the higher-
   *  fidelity signal and NOT a guaranteed one: it only arrives when the model
   *  calls `update_session_information`, so a turn that ended without one fell
   *  to `idle` and "this chat finished" — the whole point of the tab dot — was
   *  true only sometimes. `turn_ended` always arrives, so the latch is what makes
   *  the promise hold; `completed` still wins where it lands, because it is the
   *  agent's own verdict rather than the transport's.
   *
   *  Set on EVERY finished turn, whoever is watching. It used to be set only for
   *  a chat the reader was NOT looking at, so it meant "finished while you were
   *  away" — and the cost of that was the dot falling back to hollow `idle` at
   *  the one moment the reader was watching a turn complete. Cleared by the next
   *  `setThinking(id, true)` and by the transport-gap reconciler, exactly like
   *  `turn_failed`; NOT by opening the chat, because seeing a finished turn does
   *  not un-finish it. Never set for a cancelled turn: nothing was finished. */
  turn_done?: boolean;
  /** Mid-turn steers the agent has NOT read yet: the bottom dock's rows.
   *  Written on submit (intent) and by the three steer SSE events (fact);
   *  emptied at every turn boundary because that is when KAS clears its own
   *  buffer, and each entry that leaves becomes a `steer_marks` entry. */
  steers?: PendingSteer[];
  /** Steers that have left the dock and now render INSIDE the turn transcript,
   *  each anchored at the block it was injected before. Survives the turn
   *  boundary that empties `steers`, so its lifetime is the loaded transcript;
   *  `store-load.ts` carries it across a header refetch for the same reason. */
  steer_marks?: SteerMark[];
  supervised_mode?: boolean;
  /** Reasoning-effort level ("low".."max", "" = the engine default). The
   *  fourth per-chat composer setting, beside model, mode and supervised; it
   *  used to be one global `model_effort` setting keyed by the last model, so
   *  two chats could not disagree. */
  effort?: string;
  /** The SERVER's copy of the composer draft, from the single-chat GET. A SEED
   *  only: composer-state.ts holds the live working copy, adopts this once per
   *  chat and thereafter ignores it, because a fetch can land after the user has
   *  started typing the next message. It rides that GET rather than the header so
   *  it stays off the list endpoint and off every chat_updated frame; a chat with
   *  a record but no messages therefore needs its own fetch to get one, which is
   *  what activateChatView's empty branch does (`set_mode` and `set_effort` both
   *  auto-create the record before the first prompt, so a persisted empty chat
   *  can genuinely hold a draft). */
  draft?: string;
  /** The SERVER's copy of the paths staged beside that draft, from the same GET
   *  and for the same reasons. A SEED only, and off the header for the reason the
   *  draft is: both save on one 600ms debounce, so a header field would put them
   *  in a chat_updated frame every keystroke's worth of typing. */
  attachments?: string[];
  compaction_watermark?: string;
  /** Client-only transcript residency; see MessagesResidency. Carried across
   *  the header-list rebuild like every other client-only projection. */
  residency?: MessagesResidency;
}

/** One resumable KAS session, from GET /api/sessions. Mirrors
 *  vibekit.ResumableSession. */
export interface ResumableSessionRow {
  session_id: string;
  title: string;
  agent_mode?: string;
  status?: string;
  description?: string;
  /** Set when a vibekit chat already owns this session, so opening it is just
   *  opening that chat rather than adopting it. */
  chat_id?: string;
  updated_at: number;
  created_at?: number;
}

/** One previous workflow run, from GET /api/sessions. Mirrors
 *  vibekit.WorkflowRun. */
export interface WorkflowRunRow {
  workflow_id: string;
  name: string;
  status?: string;
  parent_chat_id?: string;
  /** Why a run BOUND stopped this run: "overran" (a wall clock) or "step_cap" (a
   *  step's turn cap). Absent for every other ending, INCLUDING a user cancel —
   *  which is the point of the field, since both bounds stop a run through the
   *  same cancel a person uses and KAS reports `aborted` either way. */
  end_reason?: string;
  updated_at: number;
  created_at?: number;
  started_at?: number;
}
