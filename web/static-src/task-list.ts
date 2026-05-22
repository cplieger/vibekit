// ---------------------------------------------------------------------------
// Task list pill: shows the agent's running plan as a checklist in the
// prompt bar. Badge shows pending + in-progress count. Popover shows
// all plan entries with status indicators.
// ---------------------------------------------------------------------------

import type { PlanEntry } from "./types.js";
import { getActive, version } from "./store.js";
import { effect } from "./signals.js";
import { escText } from "./strings.js";

const STATUS_ICON: Record<string, string> = {
  completed: "\u2705",   // green check
  in_progress: "\u23f3", // hourglass
  pending: "\u25cb",     // circle
};

export function initTaskListPill(): void {
  effect(() => { version.value; refreshTaskList(); });
}

function refreshTaskList(): void {
  const pill = document.getElementById("task-list-pill");
  const badge = document.getElementById("task-list-badge");
  const items = document.getElementById("task-list-items");
  if (pill === null || badge === null || items === null) return;

  const session = getActive();
  if (session === undefined) {
    pill.classList.add("hidden");
    return;
  }

  // Find the latest plan entries from messages.
  let plan: PlanEntry[] = [];
  for (let i = session.messages.length - 1; i >= 0; i--) {
    const m = session.messages[i]!;
    if (m.plan !== undefined && m.plan.length > 0) {
      plan = m.plan;
      break;
    }
  }

  if (plan.length === 0) {
    pill.classList.add("hidden");
    return;
  }

  pill.classList.remove("hidden");

  // Badge: count of pending + in_progress items.
  const active = plan.filter((e) => e.status !== "completed").length;
  if (active > 0) {
    badge.textContent = String(active);
    badge.classList.remove("hidden");
  } else {
    badge.classList.add("hidden");
  }

  // Popover items.
  items.replaceChildren();
  for (const entry of plan) {
    const row = document.createElement("div");
    row.className = `task-item task-${entry.status}`;
    const icon = STATUS_ICON[entry.status] ?? STATUS_ICON["pending"]!;
    row.innerHTML = `<span class="task-icon">${icon}</span><span class="task-text">${escText(entry.content)}</span>`;
    items.appendChild(row);
  }
}
