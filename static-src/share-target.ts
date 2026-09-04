// ---------------------------------------------------------------------------
// PWA share target + URL shortcut handling.
//
// URL params consumed once at startup:
//   ?prompt=<text>    — populate the prompt input with shared text
//   ?agent=planner    — create a new planner session
// A launch into a RUNNING window carries them elsewhere: see initLaunchQueue.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { setComposerValue } from "./composer-value.js";
import { createPlannerSession } from "./chat.js";

/** The one field of the Launch Handler API this app reads. Declared locally
 *  because the DOM lib has no `launchQueue`, and read off `window` as a
 *  capability rather than tested for. */
interface LaunchQueue {
  setConsumer: (consumer: (params: { readonly targetURL?: string }) => void) => void;
}

export async function applyShareTarget(): Promise<void> {
  const params = new URLSearchParams(location.search);
  await applyLaunchParams(params);
  // Strip query string so the URL doesn't keep the share data on reload.
  if (params.size > 0) {
    history.replaceState(null, "", location.pathname);
  }
}

/** Deliver a launch that FOCUSED this window instead of navigating it.
 *
 *  `focus-existing` (manifest) is what stops a taskbar restore from throwing the
 *  live page and its SSE stream away. The cost is that a shortcut or share into a
 *  running window changes no URL, so its params arrive ONLY here — with no consumer
 *  the queue buffers them forever and the gesture does nothing. */
export function initLaunchQueue(): void {
  const queue = (globalThis as { readonly launchQueue?: LaunchQueue }).launchQueue;
  if (queue === undefined) {
    return;
  }
  queue.setConsumer((params) => {
    const target = params.targetURL ?? "";
    if (target === "") {
      return;
    }
    void applyLaunchParams(new URL(target, location.href).searchParams);
  });
}

async function applyLaunchParams(params: URLSearchParams): Promise<void> {
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
}
