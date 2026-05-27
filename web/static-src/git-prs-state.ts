// ---------------------------------------------------------------------------
// Shared state helpers for git-prs optimistic mutations.
// Extracted to break the circular dependency between git-prs-tab.ts
// and actions/git-prs.ts.
// ---------------------------------------------------------------------------

// --- Types ---

interface PR {
  number: number;
  title: string;
  state: string;
  draft?: boolean;
  mergeable?: boolean;
  source_branch: string;
  target_branch: string;
  url?: string;
  author?: string;
  created_at?: number;
  updated_at?: number;
}

interface RepoGroup {
  forge_id: string;
  forge_kind: string;
  forge_host: string;
  owner: string;
  name: string;
  full_name: string;
  prs: PR[];
  error?: string;
}

export interface PRRemoveResult {
  group: RepoGroup;
  pr: PR;
}

// --- Mutable state reference (set by git-prs-tab at init time) ---

let groupsRef: { groups: RepoGroup[]; paint: () => void } | null = null;

/** Called by git-prs-tab to wire up the shared state. */
export function bindPRState(ref: { groups: RepoGroup[]; paint: () => void }): void {
  groupsRef = ref;
}

/** Update the groups array reference (called after refreshPRs assigns a new array). */
export function updateGroupsRef(groups: RepoGroup[]): void {
  if (groupsRef !== null) {
    groupsRef.groups = groups;
  }
}

// --- Accessors for optimistic mutations (used by actions/git-prs) ---

/** Remove a PR from lastGroups by identity and repaint. Returns info needed to rollback. */
export function removePRFromGroups(
  forgeId: string,
  owner: string,
  name: string,
  prNumber: number,
): PRRemoveResult | undefined {
  if (groupsRef === null) {
    return undefined;
  }
  for (const g of groupsRef.groups) {
    if (g.forge_id !== forgeId || g.owner !== owner || g.name !== name) {
      continue;
    }
    const pi = g.prs.findIndex((p) => p.number === prNumber);
    if (pi === -1) {
      continue;
    }
    const pr = g.prs.splice(pi, 1)[0]!;
    groupsRef.paint();
    return { group: g, pr };
  }
  return undefined;
}

/** Re-insert a previously removed PR (rollback). Uses PR.number ordering. */
export function reinsertPRInGroups(result: PRRemoveResult): void {
  if (groupsRef === null) {
    return;
  }
  const currentGroup = groupsRef.groups.find(
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
  groupsRef.paint();
}
