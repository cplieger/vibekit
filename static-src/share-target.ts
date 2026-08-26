// ---------------------------------------------------------------------------
// PWA share target + URL shortcut handling.
//
// URL params consumed once at startup:
//   ?prompt=<text>    — populate the prompt input with shared text
//   ?agent=planner    — create a new planner session
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { setComposerValue } from "./composer-value.js";
import { createPlannerSession } from "./chat.js";

export async function applyShareTarget(): Promise<void> {
  const params = new URLSearchParams(location.search);

  const sharedText = params.get("prompt");
  if (sharedText !== null && sharedText !== "") {
    // Announced, not assigned: the per-chat draft layer is already wired by the
    // time this runs (setupInput precedes it), and a silent write leaves it
    // holding nothing — so nothing schedules a save and a user who opens a
    // shared prompt and reloads before typing loses the text they were handed.
    setComposerValue(sharedText);
    $.promptInput.focus();
  }

  if (params.get("agent") === "planner") {
    // AWAITED, which is why this function became async: boot calls
    // applyShareTarget() and then applyInitialRoute(), which resolves the URL
    // against the tab strip. The planner tab has to be in it by then, and its id
    // is the server's now, so the wait is real. `shareWillCreate` also suppressed
    // boot's own starter chat on this path, so detaching would leave
    // applyInitialRoute() looking at an empty strip.
    await createPlannerSession();
  }

  // Strip query string so the URL doesn't keep the share data on reload.
  if (params.size > 0) {
    history.replaceState(null, "", location.pathname);
  }
}
