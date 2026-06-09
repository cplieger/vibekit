// ---------------------------------------------------------------------------
// Plan rendering: build and update plan cards with entry reconciliation.
//
// Extracted from messages.ts — the "Plans" section (lines 1137-1243).
// ---------------------------------------------------------------------------

import type { PlanEntry } from "./types.js";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { getActiveId } from "./store.js";
import { planToMarkdown, writePlanDraft, runPlan } from "./plan-actions.js";
import { openPlanDraftPath } from "./editor-openers.js";
import { el } from "@cplieger/reactive";

// ---------------------------------------------------------------------------
// Plan element builders
// ---------------------------------------------------------------------------

/** Build the plan card shell and reconcile entries into it. */
export function planElement(entries: readonly PlanEntry[]): HTMLDivElement {
  const card = el("div", { className: "plan-message" }) as HTMLDivElement;

  const header = el("div", { className: "plan-header" }, "Plan");
  card.appendChild(header);

  const list = el("div", { className: "plan-entries" }) as HTMLDivElement;
  card.appendChild(list);
  reconcilePlanEntries(list, entries);

  const actions = el("div", { className: "plan-actions" });
  const editBtn = el(
    "button",
    {
      className: "plan-edit-btn btn-small",
      title: "Open this plan in the editor for tweaks before handing it to the default agent",
    },
    "Edit",
  );
  const runBtn = el(
    "button",
    {
      className: "plan-run-btn btn-small",
      title: "Switch to the default agent and implement this plan",
    },
    "Run this plan",
  );
  actions.append(editBtn, runBtn);
  card.appendChild(actions);

  card.dataset["plan"] = JSON.stringify(entries);
  const latestMd = (): string => {
    const stored = card.dataset["plan"];
    if (stored === undefined) {
      return planToMarkdown([...entries]);
    }
    try {
      return planToMarkdown(JSON.parse(stored) as PlanEntry[]);
    } catch {
      return planToMarkdown([...entries]);
    }
  };
  editBtn.addEventListener("click", () => {
    void editPlanAction(getActiveId(), latestMd());
  });
  runBtn.addEventListener("click", () => {
    void runPlan(getActiveId(), latestMd());
  });

  return card;
}

/** Update an existing plan element's entries in place. */
export function updatePlanElement(el: HTMLDivElement, entries: readonly PlanEntry[]): void {
  el.dataset["plan"] = JSON.stringify(entries);
  const list = el.querySelector<HTMLDivElement>(":scope > .plan-entries");
  if (list !== null) {
    reconcilePlanEntries(list, entries);
  }
}

// ---------------------------------------------------------------------------
// Plan entry reconciliation
// ---------------------------------------------------------------------------

const planEntrySpec: ReconcileSpec<PlanEntry> = {
  key: (e) => e.content,
  mount: (e) => buildPlanRow(e),
  update: (el, e) => {
    updatePlanRow(el as HTMLDivElement, e);
  },
};

export function reconcilePlanEntries(list: HTMLDivElement, entries: readonly PlanEntry[]): void {
  reconcile(list, entries, planEntrySpec);
}

export function buildPlanRow(e: PlanEntry): HTMLDivElement {
  const row = el("div", { className: "plan-entry" }) as HTMLDivElement;
  updatePlanRow(row, e);
  return row;
}

export function updatePlanRow(row: HTMLDivElement, e: PlanEntry): void {
  const icon = e.status === "completed" ? "✅" : e.status === "in_progress" ? "🔄" : "⬜";
  row.replaceChildren(
    `${icon} ${e.content}`,
    ...(e.priority === "high" ? [" ", el("span", { className: "plan-hi" }, "[high]")] : []),
  );
  row.dataset["status"] = e.status;
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

async function editPlanAction(chatID: string, content: string): Promise<void> {
  if (chatID === "") {
    return;
  }
  const ok = await writePlanDraft(chatID, content);
  if (!ok) {
    return;
  }
  openPlanDraftPath(chatID);
}
