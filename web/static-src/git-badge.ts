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

const POLL_INTERVAL_MS = 15_000;

type BadgeState = "none" | "local" | "remote" | "both" | "error";

interface RepoStatus {
  repo: string;
  is_repo: boolean;
  branch: string;
  ahead: number;
  behind: number;
  has_dirty: boolean;
}
interface StatusAllResponse {
  repos: RepoStatus[];
}

interface ConfiguredForge {
  id: string;
  connected: boolean;
  last_error?: string;
}
interface ForgesListResponse {
  forges: ConfiguredForge[];
}

let started = false;
let lastState: BadgeState = "none";
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
      const tooltip = deriveTooltip(state, status ?? null, forges ?? null);
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
  const tooltip = deriveTooltip(state, statusRes, forgesRes);
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
    for (const f of forges.forges ?? []) {
       
      if (f.connected && f.last_error !== undefined && f.last_error !== "") {
        return "error";
      }
    }
  }
  if (status === null) {
    return "none";
  }

  let hasLocal = false;
  let hasRemote = false;
  for (const r of status.repos ?? []) {
     
    if (!r.is_repo) {
      continue;
    }
    if (r.has_dirty || r.ahead > 0) {
      hasLocal = true;
    }
    if (r.behind > 0) {
      hasRemote = true;
    }
    if (hasLocal && hasRemote) {
      break;
    }
  }
  if (hasLocal && hasRemote) {
    return "both";
  }
  if (hasLocal) {
    return "local";
  }
  if (hasRemote) {
    return "remote";
  }
  return "none";
}

/** @internal Tooltip text derived from the same data. */
function deriveTooltip(
  state: BadgeState,
  status: StatusAllResponse | null,
  forges: ForgesListResponse | null,
): string {
  if (state === "error") {
    const errored = (forges?.forges ?? []).filter(
      (f) => f.connected && f.last_error !== undefined && f.last_error !== "",
    );
    if (errored.length === 1) {
      return `Forge auth issue: ${errored[0]!.id}`; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
    return `${errored.length} forges with auth issues`;
  }
  if (status === null) {
    return "";
  }

  let dirty = 0;
  let behind = 0;
  for (const r of status.repos ?? []) {
     
    if (!r.is_repo) {
      continue;
    }
    if (r.has_dirty || r.ahead > 0) {
      dirty++;
    }
    if (r.behind > 0) {
      behind++;
    }
  }
  switch (state) {
    case "both":
      return `${dirty} repo${dirty === 1 ? "" : "s"} with local changes, ${behind} behind origin`;
    case "local":
      return `${dirty} repo${dirty === 1 ? "" : "s"} with local changes`;
    case "remote":
      return `${behind} repo${behind === 1 ? "" : "s"} behind origin`;
    default:
      return "";
  }
}

function applyBadge(state: BadgeState, tooltip: string): void {
  if (state === lastState && tooltip === lastTooltip) {
    return;
  }
  lastState = state;
  lastTooltip = tooltip;
  const el = $.gitBadge;
  if (state === "none") {
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
  el.dataset["state"] = state;
  // The badge has pointer-events: none in CSS, so hovering the badge
  // bubbles to the parent button. Set the tooltip there so the user
  // sees the rich badge state on hover instead of the static
  // "Toggle git" label.
  const btn = el.parentElement;
  if (btn !== null) {
    btn.setAttribute("data-tooltip", tooltip);
  }
}
