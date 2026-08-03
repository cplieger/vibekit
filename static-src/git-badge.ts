// ---------------------------------------------------------------------------
// Git badge: the small dot on the sidebar git button. Shows derived
// state across every cloned repo + every connected forge.
//
// State priority (highest = most actionable):
//   error   — any connected forge has last_error set, OR /api/forges
//             couldn't load. Red. Action: open Sources tab.
//   both    — at least one repo has local changes AND at least one is
//             behind origin. Purple.
//   local   — at least one repo has uncommitted/unpushed changes
//             (dirty or ahead). Amber.
//   remote  — at least one repo is behind origin. Blue.
//   (none)  — everything synced; badge hidden.
//
// Polling cadence:
//   - Every 15 seconds: cheap status-all (no fetch) → updates local
//     state instantly when files change.
//   - On user-initiated refresh (Refresh all button, tab switch into
//     Changes/PRs): full status with fetch → updates remote state.
//   - SSE turn_ended → cheap re-check (the agent likely changed
//     files locally; remote stays the same).
//   - SSE forges_changed → /api/forges re-check for error state.
//
// Background remote-fetch interval is intentionally NOT here — the
// status-all endpoint we poll skips fetch precisely so we don't
// hammer N forges every 15s. Remote state updates lag accordingly,
// which is fine: it gets refreshed on every navigation.
// ---------------------------------------------------------------------------

import { onSSE } from "./bus.js";
import { $ } from "./dom.js";
import { refreshForges as refreshForgesAction } from "./actions/git-badge.js";
import { initGitStatusStore, onGitStatusChange, currentRepos } from "./git-status-store.js";
import { pollAction } from "./actions/index.js";
import type { GitRepoStatusBadge } from "./git-types.js";
import type { ConfiguredForge } from "./wire/types.gen.js";

const POLL_INTERVAL_MS = 15_000;

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

interface ForgesListResponse {
  forges?: ConfiguredForge[] | null;
}

let started = false;
let lastState: BadgeState = { kind: "none" };
let lastTooltip = "";
/** Last forges response. The badge still owns this fetch — only the git STATUS
 *  half moved to the shared store. */
let lastForges: ForgesListResponse | null = null;

/** Wire SSE listeners + start the badge. Idempotent.
 *
 *  The git-status poll is NOT started here any more: git-status-store.ts owns
 *  it, and the badge repaints from its subscription. That removed one of the two
 *  independent /api/git/status-all pollers, and is what let the docs page and
 *  the file browser get per-path letters without adding a third. */
export function initGitBadge(): void {
  if (started) {
    return;
  }
  started = true;
  initGitStatusStore();
  onSSE("forges_changed", () => {
    void refreshGitBadge();
  });
  // Repaint whenever the shared store lands a new poll result. Fires
  // immediately with the current (initially empty) value.
  onGitStatusChange(() => {
    repaint();
  });
  // The forges half keeps its own poll: it is a different endpoint with a
  // different failure mode (an auth error trumps every status).
  pollAction(refreshForgesAction, undefined, {
    interval: POLL_INTERVAL_MS,
    onSuccess: (forges) => {
      lastForges = forges;
      repaint();
    },
  });
}

/** Recompute the badge from current data, refreshing forges first. */
export async function refreshGitBadge(): Promise<void> {
  lastForges = await refreshForgesAction.dispatch(undefined);
  repaint();
}

/** Project the current store + forges data onto the badge DOM. */
function repaint(): void {
  const state = deriveState({ repos: [...currentRepos()] }, lastForges);
  applyBadge(state, deriveTooltip(state));
}

/** @internal Pure derivation for testing. */
function deriveState(
  status: StatusAllResponse | null,
  forges: ForgesListResponse | null,
): BadgeState {
  // Forge error trumps everything: an unusable forge means PR/clone
  // operations would fail; surface it first.
  if (forges !== null) {
    const erroredIds: string[] = [];
    for (const f of forges.forges ?? []) {
      if (f.connected && f.last_error !== undefined && f.last_error !== "") {
        erroredIds.push(f.id);
      }
    }
    if (erroredIds.length > 0) {
      return { kind: "error", forgeIds: erroredIds };
    }
  }
  if (status === null) {
    return { kind: "none" };
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
