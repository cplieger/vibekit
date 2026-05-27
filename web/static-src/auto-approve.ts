// ---------------------------------------------------------------------------
// Auto-approve crew toggle: pill button in the prompt bar that controls
// whether subagent permission requests are auto-approved with allow_once.
//
// Visible only when the active chat has a crew card (subagents running).
// State persists with the chat file via the set_auto_approve_crew command.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getActive, activeVersion } from "./store.js";
import { effect } from "./signals.js";
import { setAutoApproveCrew } from "./actions/chat.js";
import { bindLoadingState } from "./actions/index.js";
import type { Session } from "./types.js";

/** Client-side cache augmentation — avoids O(n) scan on every render. */
interface SessionWithCrewCache extends Session {
  _hasCrew?: boolean;
}

/** Clear the crew cache for a session (e.g. after checkpoint restore). */
export function clearCrewCache(s: Session): void {
  delete (s as SessionWithCrewCache)._hasCrew;
}

let wired = false;

export function initAutoApprove(): void {
  if (wired) {
    return;
  }
  wired = true;

  const btn = $.autoApproveCrewBtn;
  btn.addEventListener("click", toggle);
  // Capture unbind for parity with other pill controllers; not called
  // because this pill lives for the lifetime of the page.
  void bindLoadingState("chat.set_auto_approve_crew", $.autoApproveCrewBtn);

  // Re-render on every store change (active chat switch, flag change).
  effect(() => {
    activeVersion.value;
    render();
  });
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
    if (found) {
      (session as SessionWithCrewCache)._hasCrew = true;
    }
  }
  const hasCrew = (session as SessionWithCrewCache)._hasCrew === true;
  btn.classList.toggle("hidden", !hasCrew);
  const active = session.auto_approve_crew;
  btn.classList.toggle("active", active);
  btn.setAttribute("aria-pressed", String(active));
  const label = active ? "Auto-approve subagent tools (on)" : "Auto-approve subagent tools (off)";
  btn.title = label;
  btn.setAttribute("aria-label", label);
}

function toggle(): void {
  const session = getActive();
  if (session === undefined) {
    return;
  }
  const newValue = !session.auto_approve_crew;
  void setAutoApproveCrew.dispatch({ chatID: session.id, enabled: newValue }, { silent: true });
}
