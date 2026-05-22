// ---------------------------------------------------------------------------
// SSE handlers for assistant messages + tool calls: appended, created,
// chunk, updated, tool_call, tool_call_update.
//
// Forwards into the store; the store emits change events that renderer.ts
// subscribes to. Typed through onSSE — no `unknown` unwrap boilerplate.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import {
  appendMessage, upsertMessage, appendChunk, upsertToolCall,
} from "../store.js";
import { markGitDirty } from "../git.js";
import { isRepoMutatingKind } from "../tool-schema.js";
import { clearBannerCodes } from "../banner-stack.js";
import { setSubagentActivity } from "../crew-card.js";

onSSE("message_appended", (chatID, m) => {
  if (m !== undefined) {
    appendMessage(chatID, m);
    // Agent-switched events resolve init-error banners.
    if (m.event_kind === "agent_switched") {
      clearBannerCodes(chatID, ["agent_not_found", "agent_config_error"]);
    }
  }
});

onSSE("message_created", (chatID, m) => {
  // message_created starts a new assistant bubble; upsert so future chunks
  // target the right ID. Content is empty until chunks arrive.
  if (m !== undefined) upsertMessage(chatID, m);
});

onSSE("message_chunk", (chatID, p) => {
  if (p !== undefined) appendChunk(chatID, p.message_id, p.delta);
});

onSSE("message_updated", (chatID, m) => {
  if (m !== undefined) upsertMessage(chatID, m);
});

onSSE("tool_call", (chatID, p) => {
  if (p !== undefined) upsertToolCall(chatID, p.message_id, p.tool_call);
});

onSSE("tool_call_update", (chatID, p) => {
  if (p !== undefined) {
    upsertToolCall(chatID, p.message_id, p.tool_call);
    if (p.tool_call.status === "completed" && isRepoMutatingKind(p.tool_call.kind)) {
      markGitDirty();
    }
  }
});

// Per-subagent activity stream: updates the activity line on the crew
// card row in real time (e.g. "Reading file.go", "Running tests",
// "Thinking..."). The event carries a sub_session_id and an event
// object with a human-readable label.
onSSE("subagent_activity", (_chatID, p) => {
  if (p === undefined) return;
  const sid = p.sub_session_id;
  const evt = p.event as Record<string, unknown> | undefined;
  if (sid === undefined || sid === "" || evt === undefined) return;
  // Extract a human-readable label from the activity event. kiro-cli
  // sends various shapes; we look for common fields in priority order.
  const label =
    (typeof evt["label"] === "string" ? evt["label"] : "") ||
    (typeof evt["title"] === "string" ? evt["title"] : "") ||
    (typeof evt["tool_name"] === "string" ? evt["tool_name"] : "") ||
    (typeof evt["status"] === "string" ? evt["status"] : "");
  if (label !== "") setSubagentActivity(sid, label);
});
