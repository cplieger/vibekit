// ---------------------------------------------------------------------------
// The seams: one router from a SUBJECT in the transcript to the surface that
// answers it.
//
// The depth ladder gives every tool card three answers — did it (the claim),
// what changed (the peek), let me work with it (depth 2). This module owns that
// third step for every subject that has one, so a call site says WHAT the user
// clicked rather than WHICH surface to open:
//
//   a changed filename, a ledger row  -> openChange
//   `Review changes` on a turn        -> openChangeSet
//   a search hit, a path:line link    -> openAtLine
//   a fetched URL                     -> openExternal
//
// Why a router and not direct imports (which is what these call sites had):
//
//   - The same subject appeared in four places and each picked its own opener.
//     A changed filename in a tool card, the same filename in the turn footer's
//     ledger, a file row in the browser and a turn-approval row in the dock are
//     ONE intent, and they were drifting apart — the dock and the ledger already
//     disagreed with the tool card about which diff to show.
//   - The file browser's change decoration needs somewhere to send a click, and
//     it is not in the transcript at all. A router is the thing it can call
//     without importing the chat's internals.
//   - `Review changes` had no surface, and inventing one would have broken the
//     ladder's own rule: depth 2 lands in an EXISTING vibekit surface. Routing
//     it to the git view's changes tab is what keeps that rule true.
//
// NOT here: the command card's full output, which stays inside its own
// disclosure. That is the one depth 2 that does not leave the transcript, and it
// must not reach the shell panel — one global LIVE PTY whose next server frame
// can interleave with or erase anything written into it.
// ---------------------------------------------------------------------------

import { openFile, openFileGitDiff } from "./editor-openers.js";
import { toggleGitView } from "./tabs.js";
import { setGitTab } from "./git-tabs.js";
import { isSafeURL } from "./url-safety.js";
import { absPath } from "./workspace.js";

/** Open a file's CHANGE — its diff against HEAD.
 *
 *  vs HEAD rather than the calling card's own before/after pair, and that is the
 *  honest source for a "let me look" click: the write has already landed, so the
 *  working tree IS the after state and git holds the before. A card's own pair
 *  answers the narrower "what did THIS call do", which is what its `+N −M` link
 *  is for.
 *
 *  This is the seam between the client's two path spaces, and the ONE place the
 *  crossing happens. Its callers do not agree on the form they hold and cannot
 *  be made to: three of them (the turn footer's ledger row, a tool card's
 *  filename, a turn-approval file row) carry the agent's workspace-RELATIVE
 *  path, while the file browser carries an absolute one. The editor addresses
 *  files absolutely, so every caller is normalised here rather than each
 *  learning the rule. Before this, the three relative callers produced
 *  `GET /api/file?path=hello.sh`, which the granted-roots allow-list denied
 *  with 403 — so clicking a changed filename could never load its diff. */
export function openChange(path: string, ref = "HEAD"): void {
  if (path === "") {
    return;
  }
  openFileGitDiff(absPath(path), ref);
}

/** Open a MULTI-FILE review — the git view's changes tab.
 *
 *  There is no turn-scoped multi-file diff viewer, deliberately: the ladder's
 *  rule is that depth 2 lands in a surface that already exists, and the git
 *  view already lists every changed file and opens each one's diff on click. A
 *  bespoke viewer would be a second changed-files list to keep in sync.
 *
 *  The honest limitation, stated: this shows the WORKING TREE's changes, not
 *  only the turn's. For the common case they are the same set — the agent just
 *  made them — and where they differ the turn's own ledger is the scoped list.
 *  Scoping the git view to a path set is a filter it does not have. */
export function openChangeSet(): void {
  setGitTab("changes");
  toggleGitView("changes");
}

/** Open the editor at a line — a search hit, or a `path:line` reference.
 *
 *  Normalised through the same seam as openChange, and for the same reason: a
 *  read card's filename and a `path:line` reference in the agent's prose are
 *  both workspace-RELATIVE, so both produced `GET /api/file?path=…` requests
 *  the granted-roots allow-list denied. An absolute path passes through. */
export function openAtLine(path: string, line?: number): void {
  if (path === "") {
    return;
  }
  openFile(absPath(path), line);
}

/** Surface a URL the agent fetched. Returns false when the URL is not safe to
 *  offer, so the caller can render plain text instead of a dead link.
 *
 *  It never auto-opens: an SSE-driven `window.open` is popup-blocked, and a
 *  transcript that opens tabs by itself is worse than one that asks. */
export function openExternal(url: string): boolean {
  if (!isSafeURL(url)) {
    return false;
  }
  window.open(url, "_blank", "noopener,noreferrer");
  return true;
}
