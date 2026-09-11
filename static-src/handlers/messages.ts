// ---------------------------------------------------------------------------
// SSE handlers for assistant messages and tool calls: appended, created,
// chunk, updated, tool_call, tool_call_update. Each forwards into the store,
// whose change events drive the transcript. Typed through onSSE.
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
  setChunkWatermark,
  noteLiveTurnMessage,
  noteTruncatedSnapshot,
  get,
} from "../store.js";
import { markGitDirty } from "../git.js";
import { isRepoMutatingKind } from "../tool-schema.js";
import { isStepSubtask } from "../step-subtask.js";
import type { Message, ToolCall } from "../types.js";

// The `=== undefined` guards below look unnecessary to the type checker — the wire
// decoder marks payloads non-nullable — but the SSE bus can hand us a malformed frame
// at runtime, and the suite exercises that path via `fireSSE(..., undefined)`.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

onSSE("message_appended", (chatID, m) => {
  if (m === undefined) {
    return;
  }
  appendMessage(chatID, m);
  // A persisted PROMPT row is the server saying it accepted a prompt, which is the ONLY
  // liveness signal a client that did not send it gets before the first chunk: `thinking`
  // is the sender's own dispatch and `turn_state` is connect-time synthesis. Without it a
  // stale `turn_open: false` from that client's last load derives a terminal outcome for a
  // turn that is starting. `markTurnLive` rather than `setThinking`, which would re-clear
  // the previous turn's verdicts on a replayed row. Released by the next settled
  // `turn_ended` on this chat or by the `transport:gap` reconcile, never by a page load.
  if (opensTurn(m)) {
    markTurnLive(chatID);
  }
});

onSSE("message_created", (chatID, m) => {
  if (m === undefined) {
    return;
  }
  // Upsert so future chunks target the right id, and mark which message is unpersisted so
  // a refetch's array replacement does not drop it.
  //
  // It does NOT latch `thinking`: this frame carries no attribution, and a chat-parented
  // run's step frames arrive on the LAUNCHING chat's connection, so a latch here reads a
  // run as this chat working with nothing to clear it — a step's own turn_end is dropped by
  // the workflow attribution gate. The chunk latch below is one frame later and attributed.
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

/** Does this appended row OPEN a turn, rather than join the one already running? A steer
 *  joins, so it asserts no liveness this handler has not already seen; `turns.ts` isPrompt
 *  is the twin that decides the same thing for the projection. */
function opensTurn(m: Pick<Message, "role" | "user_kind">): boolean {
  return m.role === "user" && m.user_kind !== "steer";
}

/** Latch `thinking` from streaming evidence, idempotently: `setThinking(true)` clears the
 *  previous turn's verdicts, so it must only run on the transition or every chunk would
 *  re-clear latches and churn the session signal. Each caller owns its own gate. */
function markTurnLive(chatID: string): void {
  if (chatID !== "" && get(chatID)?.thinking === false) {
    setThinking(chatID, true);
  }
}

// turn_state is connect-time synthesis and is never broadcast live, so a dropped frame
// is gone for good: one per busy chat, carrying the authoritative busy signal and the
// accumulated message the transcript would otherwise be blank without.
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
    setChunkWatermark(chatID, msg.id, p.chunk_seq ?? 0);
    // The snapshot is the server's unflushed buffer, so this id is
    // unpersisted by construction.
    noteLiveTurnMessage(chatID, msg.id);
    // BEFORE the upsert, so the body's first paint already carries the note: the
    // connect-time cap sends only the TAIL of a big turn, and a reader shown the
    // tail with nothing saying so reads a bounded payload as the whole reply.
    // `truncated` is a REQUIRED wire field, so an absent marker cannot mean
    // "complete" — it means an older server, which capped nothing.
    if (p.truncated) {
      noteTruncatedSnapshot(chatID, msg.id);
    }
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
  // `tool_call` event drives block placement. It returns the FOLDED call —
  // `kind` rides a delta only when that frame changed it, and the frame that
  // completes a write normally carries a status and nothing else, so the kind has
  // to come from the accumulated value. Off the return rather than a second
  // lookup: the fold already found the message through the store's index and the
  // call through its own scan.
  const call = applyToolCallDelta(chatID, p);
  if (p.status === "completed" && call !== undefined && isRepoMutatingKind(call.kind)) {
    // A repo-mutating call finishing is the FACT that the tree changed, where a
    // 15-second poll of 54 worktrees was a guess at it. The call also NAMES what
    // it touched, so the rescan is scoped to the owning repositories: without the
    // paths, a turn editing ten files in one repo pays ten whole-tree scans.
    markGitDirty(mutatedPaths(call));
  }
});

/** The workspace-relative paths a completed tool call says it touched.
 *
 *  Two sources because neither is complete alone: `locations` is what KAS reports for a
 *  read or a command, `diffs[].path` what a write carries, and a call can have either,
 *  both or neither. Both are already workspace-relative (`translate.relPath` is the funnel),
 *  which is the form `?paths=` resolution expects. EMPTY means "something changed and this
 *  call cannot say where", which `markGitDirty` reads as a full rescan — a scope derived
 *  from nothing rescans the wrong repository. */
function mutatedPaths(call: ToolCall): string[] {
  const paths: string[] = [];
  for (const l of call.locations ?? []) {
    if (l.path !== "") {
      paths.push(l.path);
    }
  }
  for (const d of call.diffs ?? []) {
    if (d.path !== "") {
      paths.push(d.path);
    }
  }
  return paths;
}
