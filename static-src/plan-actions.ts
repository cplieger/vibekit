// ---------------------------------------------------------------------------
// Plan handoff: the Run / Edit actions on plan cards and the editor's
// "Send plan" toolbar button all route through this module so the flow
// lives in one place.
//
// KAS's built-in Plan mode is READ-ONLY — it has no write/execute tools and
// cannot switch itself out of Plan mode (there is no `switch_mode` tool on the
// acp wire; KAS's own Step-3 hand-off tells the *user* to switch). So the
// handoff must move the chat to an executing mode itself: when the chat is
// currently in `plan` mode, `runPlan` dispatches the `chat.set_mode` action to
// the bundled default executing mode (`vibe`, labelled "Default") and awaits it
// before sending the "Please implement this plan" prompt. From an already-
// executing mode the switch is skipped and the plan is sent as-is.
// ---------------------------------------------------------------------------

import type { PlanEntry } from "./types.js";
import { apiPutOrError, apiDelete } from "./api-client.js";
import { showBanner } from "./banner-stack.js";
import { runPlan as runPlanAction } from "./actions/messages.js";
import { setMode } from "./actions/chat.js";
import { get } from "./store.js";

/** Server-enforced plan-draft size ceiling (mirrors plan_draft.go's 256 KB
 *  cap). The Run path sends the plan straight as a prompt with no PUT, so it
 *  must apply the cap client-side — the draft PUT's 413 guard never runs for
 *  it. */
const PLAN_DRAFT_MAX_BYTES = 256 * 1024;

/** Bundled read-only planning mode id (KAS). */
const PLAN_MODE_ID = "plan";
/** Bundled default executing mode id (KAS; labelled "Default"). */
const EXECUTING_MODE_ID = "vibe";

/** Outcome of a plan handoff. `too_large` / `failed` are terminal errors that
 *  have already surfaced their own message; `sent` / `queued` are success
 *  (queued = buffered behind an in-flight turn, the draft kept as the durable
 *  copy). */
export type PlanHandoffResult = "sent" | "queued" | "too_large" | "failed";

/** Serialize a plan into markdown suitable for the draft file + prompt
 *  body. High-priority items are marked inline so the agent sees them. */
export function planToMarkdown(entries: PlanEntry[]): string {
  const lines: string[] = ["# Plan", ""];
  for (const e of entries) {
    const marker = e.status === "completed" ? "x" : " ";
    const hi = e.priority === "high" ? " **[high priority]**" : "";
    lines.push(`- [${marker}] ${e.content}${hi}`);
  }
  return lines.join("\n") + "\n";
}

/** Write the plan-draft file on the server. Returns true on success.
 *  On failure surfaces a dismissible error banner so the user sees
 *  *why* the Edit/Run click went nowhere — silent no-op is the worst
 *  UX: the buttons look broken. 413 (plan > 256 KB server cap) is
 *  the expected failure for large plans; other statuses (chat gone,
 *  disk full, permission) render with the server's error text. */
export async function writePlanDraft(chatID: string, content: string): Promise<boolean> {
  if (chatID === "") {
    return false;
  }
  const r = await apiPutOrError<{ ok?: boolean }>(
    `/api/chats/${encodeURIComponent(chatID)}/plan-draft`,
    { content },
  );
  if (!r.ok) {
    const msg =
      r.status === 413
        ? "Plan is too large to save (256 KB limit). Trim some items and try again."
        : r.error !== ""
          ? `Could not save plan draft: ${r.error}`
          : "Could not save plan draft.";
    showBanner(chatID, "plan_draft_write_failed", msg, "error", true);
    return false;
  }
  return true;
}

/** Delete the plan-draft file on the server. Silent best-effort. */
async function deletePlanDraft(chatID: string): Promise<void> {
  if (chatID === "") {
    return;
  }
  await apiDelete(`/api/chats/${encodeURIComponent(chatID)}/plan-draft`);
}

/** Number of UTF-8 bytes in `content` — matches how the server measures the
 *  256 KB plan-draft cap. */
function planByteSize(content: string): number {
  return new TextEncoder().encode(content).length;
}

/** If the chat sits in the read-only Plan mode, switch it to the bundled
 *  executing mode before the implement-prompt is sent. Awaited so the server
 *  applies `session/set_mode` ahead of the prompt; best-effort — a failed
 *  switch surfaces its own toast (chat.set_mode) and we still attempt the
 *  send. */
async function switchOutOfPlanMode(chatID: string): Promise<void> {
  if (get(chatID)?.current_mode_id !== PLAN_MODE_ID) {
    return;
  }
  await setMode.dispatch({ chatID, modeID: EXECUTING_MODE_ID });
}

/** Hand the plan to the running agent as a prompt. When the chat is in the
 *  read-only Plan mode this first switches it to an executing mode (KAS Plan
 *  mode can neither implement nor self-switch), then sends "Please implement
 *  this plan". Runs under the shared thinking lifecycle so the send button
 *  shows "busy" for the whole handoff.
 *
 *  The draft is deleted ONLY once the send is confirmed ("sent"). A queued
 *  send (turn in flight) keeps the draft as the durable copy — the prompt
 *  lives only in the client's in-memory queue until it drains, so deleting it
 *  optimistically would lose the plan on a reload before the drain. A rejected
 *  send (chat tombstoned/gone) likewise keeps the draft so the user can
 *  retry. */
export async function runPlan(chatID: string, content: string): Promise<PlanHandoffResult> {
  if (chatID === "" || content.trim() === "") {
    return "failed";
  }
  if (planByteSize(content) > PLAN_DRAFT_MAX_BYTES) {
    showBanner(
      chatID,
      "plan_run_too_large",
      "Plan is too large to send (256 KB limit). Trim some items and try again.",
      "error",
      true,
    );
    return "too_large";
  }
  await switchOutOfPlanMode(chatID);
  const result = await runPlanAction.dispatch({ chatID, content });
  if (result === null) {
    return "failed";
  }
  if (result === "sent") {
    await deletePlanDraft(chatID);
  }
  return result;
}
