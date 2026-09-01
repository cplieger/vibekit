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
  noteLiveTurnMessage,
  get,
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
  // target the right ID. Also live-turn evidence: the server emits it only
  // when a turn opens a buffer, and some turns this client did not prompt
  // (a KAS auto-wake, a run step, another device's send) never set
  // `thinking` any other way. Also marks which message is unpersisted, so
  // a refetch's array replacement doesn't drop it.
  markTurnLive(chatID);
  noteLiveTurnMessage(chatID, m.id);
  upsertMessage(chatID, m);
});

onSSE("message_chunk", (chatID, p) => {
  if (p === undefined) {
    return;
  }
  // Same live-turn evidence as message_created, covering a dropped or
  // reordered created frame.
  markTurnLive(chatID);
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

/** Latch `thinking` from streaming evidence, idempotently: `setThinking(true)`
 *  clears the previous turn's verdicts, so it must only run on the transition
 *  or every chunk would re-clear latches (and churn the session signal). */
function markTurnLive(chatID: string): void {
  if (chatID !== "" && get(chatID)?.thinking === false) {
    setThinking(chatID, true);
  }
}

// turn_state: connect-time synthesis of an in-flight turn (never broadcast
// live). Emitted once per busy chat in the SSE connect replay: an
// authoritative busy signal, the accumulated assistant message so the
// transcript isn't blank until the next chunk, and the agent's last
// self-declared status.
onSSE("turn_state", (chatID, p) => {
  if (p === undefined || chatID === "") {
    return;
  }
  setThinking(chatID, true);
  const msg = p.message;
  if (msg !== undefined && msg.id !== "") {
    setSnapshotSeq(chatID, msg.id, p.chunk_seq ?? 0);
    // The snapshot is the server's unflushed buffer, so this id is
    // unpersisted by construction.
    noteLiveTurnMessage(chatID, msg.id);
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
