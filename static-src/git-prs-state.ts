// ---------------------------------------------------------------------------
// Single source of truth for the git-prs tab's PR groups + paint hook.
//
// Owns the canonical `groups` array so the tab's paint() and the
// optimistic merge/close mutations (in actions/git-prs.ts) operate on
// ONE array — there is no second `lastGroups` to keep in sync. Lives in
// its own module to break the circular dependency between git-prs-tab.ts
// and actions/git-prs.ts.
// ---------------------------------------------------------------------------

import type { GitPR as PR, GitRepoGroup as RepoGroup } from "./git-types.js";

export interface PRRemoveResult {
  group: RepoGroup;
  pr: PR;
}

// --- Single source of truth ---

let groups: RepoGroup[] = [];
let paintFn: (() => void) | null = null;

/** Wire the repaint callback (called once by git-prs-tab at init). */
export function bindPRPaint(paint: () => void): void {
  paintFn = paint;
}

/** Read the canonical groups array. Mutations to its members are
 *  reflected by the next paint(); the tab reads exclusively through this. */
export function getPRGroups(): RepoGroup[] {
  return groups;
}

/** Replace the canonical groups array (called by refreshPRs after a fetch). */
export function setPRGroups(next: RepoGroup[]): void {
  groups = next;
}

// --- Optimistic mutations (used by actions/git-prs) ---

/** Remove a PR from the canonical groups by identity and repaint.
 *  Returns the info needed to roll back, or undefined if not found. */
export function removePRFromGroups(
  forgeId: string,
  owner: string,
  name: string,
  prNumber: number,
): PRRemoveResult | undefined {
  for (const g of groups) {
    if (g.forge_id !== forgeId || g.owner !== owner || g.name !== name) {
      continue;
    }
    const pi = g.prs.findIndex((p) => p.number === prNumber);
    if (pi === -1) {
      continue;
    }
    const pr = g.prs.splice(pi, 1)[0]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    paintFn?.();
    return { group: g, pr };
  }
  return undefined;
}

/** Re-insert a previously removed PR (rollback) into its current group,
 *  positioned by descending PR.number. Dedup-guarded against double rollback. */
export function reinsertPRInGroups(result: PRRemoveResult): void {
  const currentGroup = groups.find(
    (g) =>
      g.forge_id === result.group.forge_id &&
      g.owner === result.group.owner &&
      g.name === result.group.name,
  );
  if (!currentGroup) {
    return;
  }
  // find correct position by PR.number (descending — higher numbers first)
  let idx = currentGroup.prs.findIndex((p) => p.number < result.pr.number);
  if (idx === -1) {
    idx = currentGroup.prs.length;
  }
  // dedup guard against double-rollback
  if (currentGroup.prs.some((p) => p.number === result.pr.number)) {
    return;
  }
  currentGroup.prs.splice(idx, 0, result.pr);
  paintFn?.();
}
