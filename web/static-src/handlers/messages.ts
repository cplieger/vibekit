// ---------------------------------------------------------------------------
// SSE handlers for assistant messages + tool calls: appended, created,
// chunk, updated, tool_call, tool_call_update.
//
// Forwards into the store; the store emits change events that renderer.ts
// subscribes to. Typed through onSSE — no `unknown` unwrap boilerplate.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { appendMessage, upsertMessage, appendChunk, upsertToolCall } from "../store.js";
import { markGitDirty } from "../git.js";
import { isRepoMutatingKind } from "../tool-schema.js";
import { clearBannerCodes } from "../banner-stack.js";
import { setSubagentActivity } from "../crew-card.js";

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
  // Agent-switched events resolve init-error banners.
  if (m.event_kind === "agent_switched") {
    clearBannerCodes(chatID, ["agent_not_found", "agent_config_error"]);
  }
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
  appendChunk(chatID, p.message_id, p.delta, p.is_reasoning ?? false, p.block_index);
});

onSSE("message_updated", (chatID, m) => {
  if (m === undefined) {
    return;
  }
  upsertMessage(chatID, m);
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

// Per-subagent activity stream: updates the activity line on the crew
// card row in real time (e.g. "Reading file.go", "Running tests",
// "Thinking..."). The event carries a sub_session_id and an event
// object with a human-readable label.

/** Typed shape of the subagent_activity SSE payload's event field. */
interface SubagentActivityEvent {
  label?: string;
  title?: string;
  tool_name?: string;
  status?: string;
}

onSSE("subagent_activity", (_chatID, p) => {
  if (p === undefined) {
    return;
  }
  const sid = p.sub_session_id;
  if (typeof sid !== "string" || sid === "") {
    return;
  }
  const evt = p.event;
  if (evt === null || evt === undefined || typeof evt !== "object") {
    return;
  }
  const e = evt as SubagentActivityEvent;
  // Extract a human-readable label from the activity event. kiro-cli
  // sends various shapes; we look for common fields in priority order.
  const label = e.label ?? e.title ?? e.tool_name ?? e.status ?? "";
  if (label !== "") {
    setSubagentActivity(sid, label);
  }
});
