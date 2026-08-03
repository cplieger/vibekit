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
  ClearReason,
  ErrorCode,
  EventKind,
  ForgeKind,
  PendingAction,
  PendingChangeKind,
  PlanStatus,
  Role,
  SafetyStatus,
  StopReason,
  ToolKind,
  ToolStatus,
  Transport,
  // Domain shapes
  AccountUsage,
  AccountUsageBreakdown,
  ChatHeader,
  FileChange,
  Message,
  Block,
  CodeReference,
  RefusalInfo,
  MeteringItem,
  PendingChange,
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
  PendingChangeAddedPayload,
  PendingChangeResolvedPayload,
  PendingChangesClearedPayload,
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
  SystemTool,
  TurnEndedPayload,
  TurnStatePayload,
} from "./wire/types.gen.js";

// PermissionNeeded is the legacy alias used at call sites that predate
// the generated naming. The generated type is PermissionNeededPayload.
export type { PermissionNeededPayload as PermissionNeeded } from "./wire/types.gen.js";

import type { Message, PendingChange, SessionMode, SessionModel, Usage } from "./wire/types.gen.js";

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

/** Empty payload for `pending_trust_enabled`. The chat_id on the
 *  envelope is the only meaningful data; receipt means "flip this
 *  chat's Supervised pill to Trusted". Client-only because this
 *  event has no Go-side payload struct. */
export interface PendingTrustEnabledPayload {
  readonly _empty?: never;
}

/** Payload for `pending_trust_cleared`. Reason lets the UI
 *  differentiate user-initiated clear (mode toggle) from turn boundary.
 *  Client-only because this event has no Go-side payload struct. */
export interface PendingTrustClearedPayload {
  reason?: "turn_ended" | "cancelled" | "mode_disabled" | "chat_deleted";
}

/** One prompt buffered while a turn is in flight, waiting to drain on the
 *  next `turn_ended`. Text and its attachments travel together in one entry
 *  so they can never desync (the earlier design kept attachments in a
 *  parallel map keyed positionally, which was one refactor away from
 *  leaking onto the wrong prompt). Attachments are `unknown[]` — the same
 *  loose shape the attachment row uses — carrying `AttachedFile` objects. */
export interface QueuedPrompt {
  text: string;
  attachments: unknown[];
  /** The client-generated user-message id this prompt was FIRST sent
   *  under. The drain re-sends with the SAME id so the server's
   *  append-by-id idempotency absorbs the retry — a 409'd first attempt
   *  already persisted (and rendered) the user bubble, and a fresh id on
   *  the drain would append it a second time. */
  messageId: string;
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
  prompt_queue?: QueuedPrompt[];
  supervised_mode?: boolean;
  trusted_this_turn?: boolean;
  pending_changes: PendingChange[];
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
