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
  applyToolCallDelta,
  setCodeReferences,
  setThinking,
  setAgentStatus,
  setSnapshotSeq,
  noteLiveTurnMessage,
  get,
} from "../store.js";
import type { ToolCallUpdatePayload, ToolKind } from "../wire/types.gen.js";
import { markGitDirty } from "../git.js";
import { isRepoMutatingKind } from "../tool-schema.js";
import { isStepSubtask } from "../step-subtask.js";

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
  // target the right ID, and mark which message is unpersisted so a refetch's
  // array replacement doesn't drop it.
  //
  // It does NOT latch `thinking`, and that is the fix for the tab dot reading
  // "working" for the length of a workflow run. This frame carries no
  // attribution at all, and a chat-parented run's step frames arrive on the
  // LAUNCHING chat's connection: the step opens a turn here (the run executes on
  // this chat's session), so this was the frame that turned the dot purple for
  // work the chat's own agent is not doing — and nothing clears it, because a
  // step's own turn_end is dropped by the workflow attribution gate. The chunk
  // latch below covers every case this one was written for (a KAS auto-wake,
  // another device's send) one frame later, and it can tell a step apart.
  noteLiveTurnMessage(chatID, m.id);
  upsertMessage(chatID, m);
});

onSSE("message_chunk", (chatID, p) => {
  if (p === undefined) {
    return;
  }
  // The ONE live-turn latch, and the only frame that carries the attribution the
  // decision needs. A `wf:` subtask id is a workflow STEP: the run's work, not
  // this chat's agent, so it must not make the chat read as busy — the RUN's own
  // tab dot carries that. A SUBAGENT's uuid still latches, because a delegate
  // halts the main agent, so the chat genuinely is working.
  if (!isStepSubtask(p.agent_subtask_id ?? "")) {
    markTurnLive(chatID);
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

/** Latch `thinking` from streaming evidence, idempotently: `setThinking(true)`
 *  clears the previous turn's verdicts, so it must only run on the transition
 *  or every chunk would re-clear latches (and churn the session signal).
 *
 *  Its ONE caller is the `message_chunk` handler, which gates it on attribution.
 *  A run step's frames arrive on the launching chat's connection, so "a frame
 *  arrived" is not the same claim as "this chat's agent is working". */
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
  // `workflow_step` marks a replayed turn a workflow RUN owns: apply the
  // snapshot, do not set thinking. The server still EMITS it because the
  // snapshot is the only copy of an in-flight step's transcript, so skipping the
  // event would lose that content on every refresh — while latching thinking
  // would re-assert "this chat is working" on every reconnect for the whole run,
  // with nothing to clear it.
  if (p.workflow_step !== true) {
    setThinking(chatID, true);
  }
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
  // A DELTA addressed by id, so the fold is the store's and the frame carries no
  // block_index: by definition the card is already mounted, and only the first
  // `tool_call` event drives block placement.
  applyToolCallDelta(chatID, p);
  // `kind` is on the delta only when this frame CHANGED it, so the completed
  // call's kind is read off the store rather than off the frame — the frame that
  // completes a write usually carries a status and nothing else.
  const kind = toolCallKind(chatID, p);
  if (p.status === "completed" && kind !== undefined && isRepoMutatingKind(kind)) {
    markGitDirty();
  }
});

/** The kind of the tool call a delta addresses, from the store.
 *
 *  Not from the frame: `kind` rides a delta only when that frame changed it, and
 *  the frame that completes a call normally changes the status alone. Reading it
 *  off the frame made every completed edit look like a non-mutating tool. */
function toolCallKind(chatID: string, d: ToolCallUpdatePayload): ToolKind | undefined {
  const msg = get(chatID)?.messages.find((m) => m.id === d.message_id);
  return msg?.tool_calls?.find((tc) => tc.id === d.tool_call_id)?.kind;
}
