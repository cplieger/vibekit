// ---------------------------------------------------------------------------
// Git badge: the small dot on the sidebar git button. Shows derived
// state across every cloned repo + every connected forge.
//
// State priority (highest = most actionable):
//   error   — any connected forge has last_error set. Red.
//             Action: open Sources tab.
//   both    — at least one repo has local changes AND at least one is
//             behind origin. Purple.
//   local   — at least one repo has uncommitted/unpushed changes
//             (dirty or ahead). Amber.
//   remote  — at least one repo is behind origin. Blue.
//   (none)  — everything synced; badge hidden.
//
// A FAILED /api/forges fetch is deliberately NOT the error state: a blip leaves
// the badge on its last derived state instead of raising an alarm about the
// forges. forge-store.ts publishes that failure and the Sources tab renders it.
//
// This module owns NO fetch and NO timer; it subscribes to the two shared stores
// and paints. Neither carries remote state live (no `git fetch`), so `behind` lags
// until a navigation refreshes it.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { onGitStatusChange, currentRepos } from "./git-status-store.js";
import { initForgeStore, onForgeChange, currentForges, refreshForges } from "./forge-store.js";
import type { GitRepoStatusBadge } from "./git-types.js";
import type { ConfiguredForge } from "./wire/types.gen.js";

type BadgeState =
  | { kind: "none" }
  | { kind: "local"; dirtyCount: number }
  | { kind: "remote"; behindCount: number }
  | { kind: "both"; dirtyCount: number; behindCount: number }
  | { kind: "error"; forgeIds: string[] };

type RepoStatus = GitRepoStatusBadge;
interface StatusAllResponse {
  repos?: RepoStatus[] | null;
}

let started = false;
let lastState: BadgeState = { kind: "none" };
let lastTooltip = "";

/** Wire the badge to both shared stores. Idempotent.
 *
 *  Neither poll is started here: git-status-store.ts and forge-store.ts each own
 *  one, and the badge repaints from their subscriptions. That removed the last of
 *  the duplicate fetches — the status half moved first (which is what let the
 *  docs page and the file browser get per-path letters), the forges half
 *  followed, and the PR fan-out now reads the same forge list instead of asking
 *  for its own. */
export function initGitBadge(): void {
  if (started) {
    return;
  }
  started = true;
  initForgeStore();
  // Repaint whenever either store lands a result. Both fire immediately with
  // their current (initially empty) value, and subscribing to the git store is
  // what starts its one read.
  onGitStatusChange(() => {
    repaint();
  });
  onForgeChange(() => {
    repaint();
  });
}

/** Recompute the badge from current data, refreshing forges first. */
export async function refreshGitBadge(): Promise<void> {
  await refreshForges();
  repaint();
}

/** Project the current store data onto the badge DOM. */
function repaint(): void {
  const state = deriveState({ repos: [...currentRepos()] }, currentForges());
  applyBadge(state, deriveTooltip(state));
}

/** @internal Pure derivation for testing. */
function deriveState(status: StatusAllResponse, forges: readonly ConfiguredForge[]): BadgeState {
  // Forge error trumps everything: an unusable forge means PR/clone
  // operations would fail; surface it first.
  const erroredIds: string[] = [];
  for (const f of forges) {
    if (f.connected && f.last_error !== undefined && f.last_error !== "") {
      erroredIds.push(f.id);
    }
  }
  if (erroredIds.length > 0) {
    return { kind: "error", forgeIds: erroredIds };
  }

  let dirtyCount = 0;
  let behindCount = 0;
  for (const r of status.repos ?? []) {
    if (!r.is_repo) {
      continue;
    }
    if (r.has_dirty || r.ahead > 0) {
      dirtyCount++;
    }
    if (r.behind > 0) {
      behindCount++;
    }
  }
  if (dirtyCount > 0 && behindCount > 0) {
    return { kind: "both", dirtyCount, behindCount };
  }
  if (dirtyCount > 0) {
    return { kind: "local", dirtyCount };
  }
  if (behindCount > 0) {
    return { kind: "remote", behindCount };
  }
  return { kind: "none" };
}

/** @internal Tooltip text derived from the same data. */
function deriveTooltip(state: BadgeState): string {
  switch (state.kind) {
    case "error": {
      const ids = state.forgeIds;
      if (ids.length === 1) {
        return `Forge auth issue: ${ids[0]!}`; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
      return `${ids.length} forges with auth issues`;
    }
    case "both":
      return `${state.dirtyCount} repo${state.dirtyCount === 1 ? "" : "s"} with local changes, ${state.behindCount} behind origin`;
    case "local":
      return `${state.dirtyCount} repo${state.dirtyCount === 1 ? "" : "s"} with local changes`;
    case "remote":
      return `${state.behindCount} repo${state.behindCount === 1 ? "" : "s"} behind origin`;
    case "none":
      return "";
  }
}

function applyBadge(state: BadgeState, tooltip: string): void {
  if (state.kind === lastState.kind && tooltip === lastTooltip) {
    return;
  }
  lastState = state;
  lastTooltip = tooltip;
  const el = $.gitBadge;
  if (state.kind === "none") {
    el.classList.add("hidden");
    el.removeAttribute("data-state");
    // Restore the default sidebar button tooltip when no badge.
    const btn = el.parentElement;
    if (btn !== null) {
      btn.setAttribute("data-tooltip", "Toggle git");
    }
    return;
  }
  el.classList.remove("hidden");
  el.dataset["state"] = state.kind;
  // The badge has pointer-events: none in CSS, so hovering the badge
  // bubbles to the parent button. Set the tooltip there so the user
  // sees the rich badge state on hover instead of the static
  // "Toggle git" label.
  const btn = el.parentElement;
  if (btn !== null) {
    btn.setAttribute("data-tooltip", tooltip);
  }
}
