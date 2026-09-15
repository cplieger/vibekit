// ---------------------------------------------------------------------------
// Deep-link to one Settings CONTROL: `?highlight=<element-id>`.
//
// The tab segment was already deep-linkable (`/settings/permissions`), so this
// is the last mile — scroll the control into view and flash a ring around it.
// There is deliberately NO registry, no generator and no search box: vibekit has
// four settings panels, so the ids the panels already carry ARE the index, and a
// caller naming one that no longer exists degrades to doing nothing rather than
// to a stale table nobody reads.
//
// The point is not the URL. It is that a message NAMING a setting can be a link
// to it, so `openSetting` (an in-app jump, no URL round trip) is the surface
// most callers use; `?highlight=` exists so such a link survives being copied,
// bookmarked or pasted into a chat.
//
// One mechanic that is easy to get wrong here: the query string is read at MODULE
// LOAD, not at boot. `applyShareTarget` strips `location.search` and `pushRoute`
// compares only pathname + hash, so the parameter is gone by the time any route is
// applied. Reading it at import time is what makes it survive both.
//
// Landing on the control and marking it is `flash-target.ts`'s, shared with the
// PRs tab's own deep link.
// ---------------------------------------------------------------------------

import { openSettingsView } from "./tabs.js";
import { flashTarget } from "./flash-target.js";
import { forceSettingsTab } from "./settings-tabs.js";
import { pushRoute } from "./router.js";
import type { SettingsTab } from "./route-path.js";

/** The `?highlight=` value this page load carried, read once at import time
 *  (see the mechanic above). Consumed at most once. */
let pendingTarget: string | null = readTargetFromURL();

function readTargetFromURL(): string | null {
  try {
    const raw = new URLSearchParams(location.search).get("highlight");
    return raw === null || raw.trim() === "" ? null : raw.trim();
  } catch {
    // A malformed search string is not worth failing a boot over.
    return null;
  }
}

/** Scroll a Settings control into view and flash a ring around it.
 *
 *  Quiet on an unknown id by design: a caller's target may have been renamed or
 *  removed, and a jump that lands on the right PANEL having merely failed to
 *  find one control is a better outcome than an error the reader cannot act on.
 *  Returns nothing — there is no success signal to branch on. */
export function highlightControl(id: string): void {
  // An empty id means the CALLER had no target, which `flashTarget` would spend
  // its whole frame budget failing to find.
  if (id === "") {
    return;
  }
  flashTarget(() => document.getElementById(id));
}

/** Open Settings on `tab` and highlight `controlID`. The in-app form of the deep
 *  link: what a message naming a setting calls. */
export function openSetting(tab: SettingsTab, controlID: string): void {
  // Awaited through the promise rather than detached, because everything below
  // addresses the panel the open produces — and the highlight is a DOM write
  // against a view that has to exist first.
  void openSettingsView(tab).then(() => {
    applySetting(tab, controlID);
  });
}

/** Swap the panel, push the URL and highlight. */
function applySetting(tab: SettingsTab, controlID: string): void {
  // Swaps the panel and fires the tab's lazy loader; the URL is pushed here
  // rather than by forceSettingsTab, which is the router's own callee.
  forceSettingsTab(tab);
  pushRoute({ kind: "settings", tab });
  highlightControl(controlID);
}

/** Fire the `?highlight=` this page load carried, if any. Called from the
 *  router's settings branch once the panel's data loader has run. One-shot: a
 *  later popstate back to the same URL must not re-flash a control the reader
 *  has already been shown. */
export function flushURLHighlight(): void {
  const id = pendingTarget;
  if (id === null) {
    return;
  }
  pendingTarget = null;
  highlightControl(id);
}

/** Test-only: restore the module to its pre-boot state with a chosen target. */
export function _setPendingTargetForTest(id: string | null): void {
  pendingTarget = id;
}
