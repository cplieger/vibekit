// ---------------------------------------------------------------------------
// PWA share target + URL shortcut handling.
//
// URL params consumed once at startup:
//   ?prompt=<text>    — populate the prompt input with shared text
//   ?agent=planner    — create a new planner session
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { createPlannerSession } from "./chat.js";

export function applyShareTarget(): void {
  const params = new URLSearchParams(location.search);

  const sharedText = params.get("prompt");
  if (sharedText !== null && sharedText !== "") {
    $.promptInput.value = sharedText;
    $.promptInput.focus();
  }

  if (params.get("agent") === "planner") {
    createPlannerSession();
  }

  // Strip query string so the URL doesn't keep the share data on reload.
  if (params.size > 0) {
    history.replaceState(null, "", location.pathname);
  }
}
