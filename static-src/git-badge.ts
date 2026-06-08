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
import { refreshGitBadge as refreshGitBadgeAction } from "./actions/git-badge.js";
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

/** Wire SSE listeners + start the poll. Idempotent. */
export function initGitBadge(): void {
  if (started) {
    return;
  }
  started = true;
  onSSE("turn_ended", () => {
    void refreshGitBadge();
  });
  onSSE("forges_changed", () => {
    void refreshGitBadge();
  });
  // pollAction handles cleanup, pause-when-hidden (no need to refresh
  // the badge when the user can't see it), and refresh-on-focus (instant
  // freshness when the user returns to the tab). The action's result is
  // projected to the DOM via onSuccess.
  pollAction(refreshGitBadgeAction, undefined, {
    interval: POLL_INTERVAL_MS,
    onSuccess: ({ status, forges }) => {
      const state = deriveState(status ?? null, forges ?? null);
      const tooltip = deriveTooltip(state);
      applyBadge(state, tooltip);
    },
  });
}

/** Recompute the badge state from current server data. Safe to call
 *  often — coalesces by short-circuiting if nothing visible changed. */
export async function refreshGitBadge(): Promise<void> {
  const result = await refreshGitBadgeAction.dispatch(undefined);
  const statusRes = result?.status ?? null;
  const forgesRes = result?.forges ?? null;

  const state = deriveState(statusRes, forgesRes);
  const tooltip = deriveTooltip(state);
  applyBadge(state, tooltip);
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
