// Cross-chat conflict badges.
//
// The server detects drift when a chat snapshots a file whose disk
// content doesn't match what another chat last wrote there. It
// broadcasts `conflict_detected` SSE events; we surface each as a
// small warning chip on the matching tool-call card in the chat
// transcript. Clicking the chip opens the editor in diff mode
// comparing "what the other chat left on disk" (blob SHA in
// expected_sha) against "what's there now" (blob SHA in actual_sha).
//
// This module owns:
//
//   - the conflict registry (chatID+path → latest conflict record)
//   - the renderConflictBadge helper used by messages.ts when a
//     tool card completes
//   - the SSE handler that populates the registry on live events
//   - the `/api/checkpoints/{chatID}/conflicts` fetch on chat load
//     to replay past events so badges survive a page reload
//
// Conflicts are rare; the registry is small (a few entries per
// chat at most). We cap at 100 per chat to bound memory against
// a pathological runaway.

import { onSSE } from "./bus.js";
import { escText } from "./strings.js";
import { ICON_WARN_12 } from "./icons.js";
import { registerConflictChipRenderer } from "./messages-shared.js";
import { openConflictDiff as openConflictDiffAction, loadConflicts } from "./actions/conflicts.js";
import { bindLoadingState } from "./actions/index.js";

/** Cleanup functions for conflict chip loading-state bindings. */
const chipUnbindMap = new WeakMap<HTMLElement, () => void>();

/** One conflict record. Shape matches the server-side
 *  `ConflictPayload` Go struct 1:1. */
export interface Conflict {
  readonly path: string;
  readonly other_chat: string;
  readonly expected_sha: string;
  readonly actual_sha: string;
  readonly tag: string;
  readonly ts: number;
}

// Two-level registry: chatID → (path → Conflict). Only the LATEST
// conflict per (chat, path) is retained because the UI shows a single
// chip and a second drift on the same file supersedes the first.
// Each inner map is wrapped with oldest-entry tracking for O(1)
// amortized cap enforcement (rescan only on actual eviction).
interface TrackedMap {
  entries: Map<string, Conflict>;
  oldestPath: string | null;
  oldestTs: number;
}

const registry = new Map<string, TrackedMap>();

export const MAX_PER_CHAT = 100;

/** Rescan the inner map to find the oldest entry. O(k). */
function rescanOldest(tm: TrackedMap): void {
  tm.oldestPath = null;
  tm.oldestTs = Infinity;
  for (const [p, v] of tm.entries) {
    if (v.ts < tm.oldestTs) {
      tm.oldestPath = p;
      tm.oldestTs = v.ts;
    }
  }
}

/** Register a conflict locally. Keeps the NEWER record when (chat, path)
 *  already has one — defends against a late-arriving fetch replay
 *  clobbering a fresher SSE-delivered entry. Enforces the per-chat
 *  cap with O(1) amortized oldest-ts eviction.
 *  @internal Exported for property-based testing only. */
export function remember(chatID: string, c: Conflict): void {
  let tm = registry.get(chatID);
  if (tm === undefined) {
    tm = { entries: new Map<string, Conflict>(), oldestPath: null, oldestTs: Infinity };
    registry.set(chatID, tm);
  }
  const prior = tm.entries.get(c.path);
  if (prior !== undefined && prior.ts >= c.ts) {
    return;
  }
  tm.entries.set(c.path, c);
  // If we replaced the entry that was tracked as oldest, rescan to
  // find the true oldest before the cap check (the replacement may
  // have a newer ts, making a different entry the actual oldest).
  if (c.path === tm.oldestPath) {
    rescanOldest(tm);
  } else if (c.ts < tm.oldestTs) {
    tm.oldestPath = c.path;
    tm.oldestTs = c.ts;
  }
  // Cap enforcement — O(1) check + O(k) rescan only on eviction.
  if (tm.entries.size <= MAX_PER_CHAT) {
    return;
  }
  if (tm.oldestPath !== null) {
    tm.entries.delete(tm.oldestPath);
  }
  rescanOldest(tm);
}

/** Return the latest conflict record for a file in a chat, or null. */
export function getConflict(chatID: string, path: string): Conflict | null {
  return registry.get(chatID)?.entries.get(path) ?? null;
}

/** Drop every conflict for a chat (called on chat delete). */
export function clearConflicts(chatID: string): void {
  registry.delete(chatID);
}

/** @internal Clear the entire registry. Test-only. */
export function _resetRegistry(): void {
  registry.clear();
}

/** @internal Get the number of entries for a chat. Test-only. */
export function _registrySize(chatID: string): number {
  return registry.get(chatID)?.entries.size ?? 0;
}

// Wire the SSE path.
onSSE("conflict_detected", (chatID, payload) => {
  // Server sends the full Conflict shape. We don't trust blindly —
  // drop if the payload is missing required fields.
  const c = payload as Partial<Conflict>;
  if (typeof c.path !== "string" || c.path === "") {
    return;
  }
  if (typeof c.other_chat !== "string") {
    return;
  }
  if (typeof c.expected_sha !== "string") {
    return;
  }
  if (typeof c.actual_sha !== "string") {
    return;
  }
  remember(chatID, {
    path: c.path,
    other_chat: c.other_chat,
    expected_sha: c.expected_sha,
    actual_sha: c.actual_sha,
    tag: typeof c.tag === "string" ? c.tag : "",
    ts: typeof c.ts === "number" ? c.ts : Date.now(),
  });
  // Ask the transcript to re-decorate any tool card pointing at
  // this path. messages.ts exposes a light refresh hook that
  // traverses [data-filename] nodes; we import it lazily so this
  // module stays free of direct DOM dependencies when only the
  // registry is used (e.g. from tests).
  void import("./messages-shared.js")
    .then((m) => {
      if (c.path !== undefined) {
        m.refreshConflictBadges(chatID, c.path);
      }
    })
    .catch((e: unknown) => {
      console.warn("[conflicts] badge refresh failed", e);
    });
});

/** Fetch every past conflict for a chat and populate the registry.
 *  Called from chat.ts on first activation so badges are present
 *  after a page reload (SSE replay only covers recent events; the
 *  ring can wrap). Best-effort — a failed fetch just means the
 *  user doesn't see stale badges, not an error path. */
export async function loadConflictsFor(chatID: string): Promise<void> {
  const resp = await loadConflicts.dispatch(chatID);
  const list = resp?.conflicts ?? [];
  for (const c of list) {
    remember(chatID, c);
  }
}

/** Render an inline conflict chip into the given tool-edit actions
 *  row. Idempotent: repeated calls refresh the chip's label, tooltip,
 *  and dataset so a second drift on the same file updates visible
 *  state. Clicking the chip triggers openConflictDiff. */
export function renderConflictChip(row: HTMLElement, chatID: string, path: string): void {
  const c = getConflict(chatID, path);
  const existing = row.querySelector<HTMLElement>(".conflict-chip");
  if (c === null) {
    if (existing !== null) {
      chipUnbindMap.get(existing)?.();
      chipUnbindMap.delete(existing);
      existing.remove();
    }
    return;
  }
  let chip = existing;
  if (chip === null) {
    chip = document.createElement("button");
    chip.type = "button";
    chip.className = "conflict-chip";
    chip.innerHTML = `${ICON_WARN_12}<span class="conflict-chip-label"></span>`;
    chip.addEventListener("click", () => {
      void openConflictDiff(chatID, path);
    });
    row.appendChild(chip);
    chipUnbindMap.set(chip, bindLoadingState("conflicts.open_diff", chip));
  }
  // Refresh visible state on every call so second-drift overwrites
  // stale label/tooltip captured at first render.
  chip.title = `Chat ${c.other_chat} last left a different version here. Click to compare.`;
  const label = chip.querySelector(".conflict-chip-label");
  if (label !== null) {
    label.textContent = `Drift vs ${c.other_chat}`;
  }
  chip.dataset.tag = c.tag;
  chip.dataset.other = c.other_chat;
  chip.setAttribute("aria-label", `Conflict with chat ${c.other_chat} on ${escText(path)}`);
}

// Register with messages-shared so it can render chips without importing conflicts.ts.
registerConflictChipRenderer(renderConflictChip);

/** Open the editor in diff mode comparing the blob the other chat
 *  left against the blob this chat saw. Dispatches through the action
 *  framework which handles error toasting. */
async function openConflictDiff(chatID: string, path: string): Promise<void> {
  const c = getConflict(chatID, path);
  if (c === null) {
    return;
  }
  await openConflictDiffAction.dispatch({
    chatID,
    path,
    expectedSha: c.expected_sha,
    actualSha: c.actual_sha,
    otherChat: c.other_chat,
  });
}
