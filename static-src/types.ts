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
  DecisionKind,
  ErrorCode,
  EventKind,
  ForgeKind,
  PlanStatus,
  Role,
  SafetyStatus,
  SettledBy,
  StopReason,
  ToolKind,
  ToolStatus,
  Transport,
  // Domain shapes
  AccountUsage,
  AccountUsageBreakdown,
  ApprovalFile,
  ChatHeader,
  FileChange,
  Message,
  Block,
  CodeReference,
  RefusalInfo,
  MeteringItem,
  PermissionOption,
  PlanEntry,
  PolicyRule,
  PolicyView,
  PolicyExplainResult,
  SessionMode,
  SessionModel,
  ToolCall,
  ToolDiff,
  ToolLocation,
  Usage,
  // SSE payloads
  ChatDeletedPayload,
  CodeReferencesPayload,
  ConnectedPayload,
  DecisionSettledPayload,
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
  Recipe,
  RecipesResponse,
  RunLaunchRequest,
  RunLaunchedResponse,
  SystemTool,
  SteerQueuedPayload,
  SteerInjectedPayload,
  SteerClearedPayload,
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

import type { Message, SessionMode, SessionModel, Usage } from "./wire/types.gen.js";

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
}

/** Connection status flag, surfaced through the status bar. */
export type ConnectionStatus = "connecting" | "connected" | "disconnected";

/** One mid-turn steer, projected from the server.
 *
 *  This is NOT a client-side queue entry. It is vibekit's view of a message
 *  sitting in KAS's own per-session steering buffer, and every field here is
 *  written by an SSE event (`steer_queued` / `steer_injected` /
 *  `steer_cleared`) rather than by the code that sent it. The client keeps no
 *  independent copy: there is nothing to drain, nothing to retry and no order
 *  to preserve, because the buffer and its delivery are KAS's.
 *
 *  It replaced a `QueuedPrompt` FIFO that held text until `turn_ended` and then
 *  sent it as a NEW turn — so a correction always arrived after the work it was
 *  correcting had finished. A steer lands in the turn already running. */
export interface PendingSteer {
  /** KAS's own steer id (`steer-<uuid>`), and the key for every lifecycle
   *  event. Client-minted and echoed back, so the chip that appears is the
   *  one this device sent. */
  id: string;
  text: string;
  /** Whether the model has actually READ it. False means it is in the buffer
   *  waiting for the next node boundary. This is the distinction the chip row
   *  exists to show, and the reason `steer_injected` is its own event. */
  injected: boolean;
  /** Present only when KAS classified the message as a system notification
   *  instead of a user steer, which it decides by sniffing the text. vibekit
   *  refuses to SEND such a message, so a severity here means the notice came
   *  from a workflow step or a subagent rather than from the user. */
  severity?: string;
}

// --- Local session state (client-only projection of server chat) ---

export interface Session {
  id: string;
  name: string;
  model: string;
  acp_session_id: string;
  current_mode_id: string;
  available_modes: SessionMode[];
  available_models: SessionModel[];
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
  /** Mid-turn steers KAS is holding or has just delivered for this chat.
   *  A pure projection of the three steer SSE events; cleared at every turn
   *  boundary because that is when KAS clears its own buffer. */
  steers?: PendingSteer[];
  supervised_mode?: boolean;
  compaction_watermark?: string;
}

/** One resumable KAS session, from GET /api/sessions. Mirrors
 *  api.ResumableSession. */
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
 *  api.WorkflowRun. */
export interface WorkflowRunRow {
  workflow_id: string;
  name: string;
  status?: string;
  parent_chat_id?: string;
  updated_at: number;
  created_at?: number;
  started_at?: number;
}
