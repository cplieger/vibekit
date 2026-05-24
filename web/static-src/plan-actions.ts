// ---------------------------------------------------------------------------
// Plan handoff: the Run / Edit actions on plan cards and the editor's
// "Send plan" toolbar button all route through this module so the flow
// lives in one place.
//
// The planner agent is expected to declare session modes that include a
// "build" or "implement" variant — when the agent decides the plan is
// ready, it issues a `switch_mode` tool call that the user approves,
// and the next prompt runs in the new mode. vibekit does NOT force an
// agent swap behind the user's back; we just hand the plan to the
// running agent as a prompt and let it choose what to do.
// ---------------------------------------------------------------------------

import type { PlanEntry } from "./types.js";
import { apiPutOrError, apiDelete } from "./api-client.js";
import { showBanner } from "./banner-stack.js";
import { runPlanAction } from "./actions/messages.js";

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
  if (chatID === "") return false;
  const r = await apiPutOrError<{ ok?: boolean }>(
    `/api/chats/${encodeURIComponent(chatID)}/plan-draft`, { content },
  );
  if (!r.ok) {
    const msg = r.status === 413
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
  if (chatID === "") return;
  await apiDelete(`/api/chats/${encodeURIComponent(chatID)}/plan-draft`);
}

/** Hand the plan to the running agent as a prompt. The agent decides
 *  whether to switch modes / agents — vibekit doesn't force anything.
 *  Runs under the shared thinking lifecycle so the send button shows
 *  "busy" for the whole handoff.
 *
 *  Only deletes the draft on send/queue. If the prompt was rejected
 *  (e.g. parent chat tombstoned mid-handoff, chat frozen because a
 *  tangent landed between the Edit and Send clicks), the draft stays
 *  on disk so the user's work survives the failure and they can retry
 *  from the editor. */
export async function runPlan(chatID: string, content: string): Promise<boolean> {
  if (chatID === "" || content.trim() === "") return false;
  const result = await runPlanAction.dispatch({ chatID, content });
  if (result === null) return false;
  await deletePlanDraft(chatID);
  return true;
}
