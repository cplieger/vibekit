// ---------------------------------------------------------------------------
// SSE handlers for assistant messages + tool calls: appended, created,
// chunk, updated, tool_call, tool_call_update.
//
// Forwards into the store; the store emits change events that renderer.ts
// subscribes to. Typed through onSSE — no `unknown` unwrap boilerplate.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import {
  appendMessage,
  upsertMessage,
  appendChunk,
  upsertToolCall,
  setCodeReferences,
  setThinking,
  setAgentStatus,
  setSnapshotSeq,
} from "../store.js";
import { markGitDirty } from "../git.js";
import { isRepoMutatingKind } from "../tool-schema.js";

// Defensive `=== undefined` guards in this file look unnecessary to
// the type checker — the wire decoder marks payloads non-nullable —
// but the SSE bus can hand us a malformed frame at runtime, and the
// test suite exercises that path explicitly via `fireSSE(..., undefined)`.
// Suppressing no-unnecessary-condition file-wide keeps the guards
// intentional rather than slowly eroding under "fix" passes.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

onSSE("message_appended", (chatID, m) => {
  if (m === undefined) {
    return;
  }
  appendMessage(chatID, m);
});

onSSE("message_created", (chatID, m) => {
  if (m === undefined) {
    return;
  }
  // message_created starts a new assistant bubble; upsert so future chunks
  // target the right ID. Content is empty until chunks arrive.
  upsertMessage(chatID, m);
});

onSSE("message_chunk", (chatID, p) => {
  if (p === undefined) {
    return;
  }
  appendChunk(
    chatID,
    p.message_id,
    p.delta,
    p.is_reasoning ?? false,
    p.block_index,
    p.agent_subtask_id ?? "",
    p.seq ?? 0,
    p.refusal,
  );
});

// turn_state: connect-time synthesis of an in-flight turn (never
// broadcast live). The server emits one per BUSY chat in the SSE
// connect replay: an authoritative busy signal (replacing the gap
// handler's eager thinking-clear guess for chats that are genuinely
// mid-turn), the accumulated assistant message so the streaming
// transcript isn't blank until the next chunk, and the agent's last
// self-declared status for the turn. The chunk_seq watermark makes
// chunks that raced the snapshot idempotent (see appendChunk).
onSSE("turn_state", (chatID, p) => {
  if (p === undefined || chatID === "") {
    return;
  }
  setThinking(chatID, true);
  const msg = p.message;
  if (msg !== undefined && msg.id !== "") {
    setSnapshotSeq(chatID, msg.id, p.chunk_seq ?? 0);
    upsertMessage(chatID, msg);
  }
  if (p.status !== undefined && p.status !== "") {
    setAgentStatus(chatID, p.status, p.description ?? "");
  }
});

onSSE("message_updated", (chatID, m) => {
  if (m === undefined) {
    return;
  }
  upsertMessage(chatID, m);
});

onSSE("code_references", (chatID, p) => {
  if (p === undefined) {
    return;
  }
  // Full deduped list each time; setCodeReferences replaces (idempotent).
  setCodeReferences(chatID, p.message_id, p.references);
});

onSSE("tool_call", (chatID, p) => {
  if (p === undefined) {
    return;
  }
  upsertToolCall(chatID, p.message_id, p.tool_call, p.block_index);
});

onSSE("tool_call_update", (chatID, p) => {
  if (p === undefined) {
    return;
  }
  // tool_call_update payload doesn't carry block_index — by definition
  // we're updating an already-mounted tool, so the block index is
  // unused in upsertToolCall's update branch (only the first
  // tool_call event drives block placement).
  upsertToolCall(chatID, p.message_id, p.tool_call, 0);
  if (p.tool_call.status === "completed" && isRepoMutatingKind(p.tool_call.kind)) {
    markGitDirty();
  }
});
