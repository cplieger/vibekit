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
  CrewStatus,
  ErrorCode,
  EventKind,
  ForgeKind,
  PendingAction,
  PendingChangeKind,
  PlanStatus,
  Role,
  StopReason,
  ToolKind,
  ToolStatus,
  Transport,
  // Domain shapes
  ChatHeader,
  Crew,
  CrewPendingStage,
  CrewSubagent,
  FileChange,
  Message,
  MeteringItem,
  PendingChange,
  PermissionOption,
  PlanEntry,
  SessionMode,
  SessionModel,
  ToolCall,
  ToolDiff,
  ToolLocation,
  Usage,
  // SSE payloads
  ChatDeletedPayload,
  CommandsUpdatedPayload,
  ConnectedPayload,
  ErrorPayload,
  MCPConnectedPayload,
  MCPDisconnectedPayload,
  MCPFailedPayload,
  MCPOAuthPayload,
  MessageChunkPayload,
  PendingChangeAddedPayload,
  PendingChangeResolvedPayload,
  PendingChangesClearedPayload,
  PermissionNeededPayload,
  ToolCallPayload,
  ToolCallUpdatePayload,
  TurnEndedPayload,
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

/** Catalogue entry returned by GET /api/models. Distinct from the
 *  per-session SessionModel that arrives over the bridge — the REST
 *  endpoint exists for the prelaunch model picker. */
export interface ModelInfo {
  model_name: string;
  model_id: string;
  rate_multiplier: number;
  description?: string;
}

/** Slash-command catalogue entry. The wire shape is opaque (kiro-cli
 *  forwards arbitrary metadata); the client only consumes name + description. */
export interface AvailableCommand {
  name: string;
  description?: string;
  meta?: Record<string, unknown>;
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

// --- Local session state (client-only projection of server chat) ---

export interface Session {
  id: string;
  name: string;
  agent: string;
  model: string;
  acp_session_id: string;
  current_mode_id: string;
  auto_approve_crew: boolean;
  available_modes: SessionMode[];
  available_models: SessionModel[];
  available_commands: AvailableCommand[];
  available_prompts: AvailableCommand[];
  usage: Usage;
  messages: Message[];
  message_count: number;
  has_more: boolean;
  thinking: boolean;
  working_label: string;
  prompt_queue?: string[];
  supervised_mode?: boolean;
  trusted_this_turn?: boolean;
  pending_changes: PendingChange[];
  frozen?: boolean;
  is_tangent?: boolean;
  parent_chat_id?: string;
  compaction_watermark?: string;
  oldest_checkpoint_tag?: string;
}
