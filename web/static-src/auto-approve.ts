// ---------------------------------------------------------------------------
// Auto-approve crew toggle: pill button in the prompt bar that controls
// whether subagent permission requests are auto-approved with allow_once.
//
// Visible only when the active chat has a crew card (subagents running).
// State persists with the chat file via the set_auto_approve_crew command.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getActive, version } from "./store.js";
import { effect } from "./signals.js";
import { setAutoApproveCrewAction } from "./actions/chat.js";
import type { Session } from "./types.js";

/** Client-side cache augmentation — avoids O(n) scan on every render. */
interface SessionWithCrewCache extends Session { _hasCrew?: boolean; }

let wired = false;

export function initAutoApprove(): void {
  if (wired) return;
  wired = true;

  const btn = $.autoApproveCrewBtn;
  btn.addEventListener("click", toggle);

  // Re-render on every store change (active chat switch, flag change).
  effect(() => { version.value; render(); });
}

function render(): void {
  const btn = $.autoApproveCrewBtn;
  const session = getActive();
  if (session === undefined) {
    btn.classList.add("hidden");
    return;
  }
  // Cache the hasCrew flag on the session object. Once true it never
  // reverts (crew events are permanent markers), so we skip the O(n)
  // scan on subsequent renders.
  if ((session as SessionWithCrewCache)._hasCrew !== true) {
    const found = session.messages.some((m) => m.event_kind === "crew");
    if (found) (session as SessionWithCrewCache)._hasCrew = true;
  }
  const hasCrew = (session as SessionWithCrewCache)._hasCrew === true;
  btn.classList.toggle("hidden", !hasCrew);
  const active = session.auto_approve_crew;
  btn.classList.toggle("active", active);
  btn.setAttribute("aria-pressed", String(active));
  btn.title = active
    ? "Auto-approve subagent tools (on)"
    : "Auto-approve subagent tools (off)";
}

function toggle(): void {
  const session = getActive();
  if (session === undefined) return;
  const newValue = !session.auto_approve_crew;
  void setAutoApproveCrewAction.dispatch({ chatID: session.id, enabled: newValue });
}
