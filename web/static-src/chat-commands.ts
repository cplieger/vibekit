// ---------------------------------------------------------------------------
// Low-level "send a command that affects a chat's turn state" primitives.
//
// Any module that needs to post prompt or switch-model imports from here.
// The shared lifecycle rules live in exactly one place:
//
//   1. `store.setThinking(chatID, true)` before posting.
//   2. 409 busy on prompts queues the text for the next turn end.
//   3. Other failures clear thinking so send-state settles on "blocked"
//      (transport.ts already called setLastError).
//   4. Success leaves thinking=true; SSE turn_ended / error will clear it.
//
// This module is a *leaf* from the dependency-graph standpoint: it imports
// only store, session-context, and transport. Callers higher in
// the tree (chat.ts, plan-actions.ts) layer domain behaviour on top.
//
// Terminology: "switch model" kills the ACP session, starts a fresh one
// with the chosen model, and primes it with the transcript. kiro-cli
// cannot swap models mid-session. Agent changes are deliberately NOT
// part of this command — the running agent itself is responsible for
// switching modes via a `switch_mode` permission request; vibekit never
// forces an agent change on a live chat.
//
// Compaction and mode changes are intentionally NOT exposed as typed
// client commands:
//   - /compact is reachable via the slash-command menu: user types
//     "/compact", the text flows through sendPromptTo as a normal
//     prompt, and kiro-cli natively parses it and compacts history.
//   - session/set_mode is agent-driven: the agent issues a switch_mode
//     tool call that the user approves via the permission dialog, and
//     the server executes the set_mode call on the user's behalf.
// ---------------------------------------------------------------------------

import { newMessageID } from "./transport.js";
import { getCurrentAgent, getCurrentModel } from "./session-context.js";
import { getActiveFilePath, getOpenFilePaths } from "./editor-types.js";
import { takeAttachments, addAttachment } from "./attachments.js";
import { switchModelAction, resolvePendingChangeAction, sendPromptAction } from "./actions/chat.js";
import { setLastQueuedAttachments } from "./store.js";

/** Options for the low-level prompt sender. */
export interface SendPromptOpts {
  agent?: string;
  model?: string;
  attachments?: readonly unknown[];
}

/** Post a prompt to a chat with the shared thinking + 409-queue
 *  lifecycle. Returns "sent" on 2xx, "queued" on 409 (the prompt
 *  will drain when the in-flight turn ends), or "failed" on any other
 *  error. Callers that want to know whether the server took the
 *  prompt (e.g. plan handoff deleting its draft) inspect the result;
 *  most callers still ignore it. */
export async function sendPromptTo(
  chatID: string, text: string, opts: SendPromptOpts = {},
): Promise<"sent" | "queued" | "failed"> {
  const attachments = opts.attachments !== undefined ? [...opts.attachments] : takeAttachments();
  const result = await sendPromptAction.dispatch({
    chatID, text,
    messageID: newMessageID(),
    agent: opts.agent ?? getCurrentAgent(),
    model: opts.model ?? getCurrentModel(),
    activeFile: getActiveFilePath(),
    openFiles: getOpenFilePaths(),
    attachments,
  });
  if (result === null) {
    // Restore attachments on failure so the user doesn't lose them.
    if (opts.attachments === undefined) {
      for (const a of attachments) addAttachment((a as { path: string }).path);
    }
    return "failed";
  }
  if (result === "queued" && attachments.length > 0) {
    setLastQueuedAttachments(chatID, attachments);
  }
  return result;
}

/** Send a standalone switch_model command. Used by the model picker
 *  on the chat view. Shares the thinking lifecycle with sendPromptTo
 *  so the send button reflects "busy" while the server primes the
 *  new bridge.
 *
 *  switch_model emits no `turn_ended` (no assistant reply), so we
 *  clear `thinking` on any non-409 response. 409 is NOT expected —
 *  we queue model switches client-side before they hit the wire (see
 *  model-switcher.ts) — but the defensive unwind is kept in case
 *  a race slips through.
 *
 *  Returns true when the server accepted the switch (or we queued it);
 *  false when the call failed and the caller should clear any in-flight
 *  UI state. */
export async function switchModel(chatID: string, model: string): Promise<boolean> {
  if (chatID === "") return false;
  const result = await switchModelAction.dispatch({ chatID, model });
  return result !== null && result;
}

/** Resolve a single pending change (accept or reject). Shared by
 *  supervised-pill.ts, editor-pending.ts, and messages-actions.ts. */
export function resolvePendingChange(
  chatID: string, toolCallID: string, action: "accept" | "reject",
): void {
  void resolvePendingChangeAction.dispatch({ chatID, toolCallID, action });
}
