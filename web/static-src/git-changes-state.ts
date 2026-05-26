// ---------------------------------------------------------------------------
// Shared state helpers for git-changes optimistic mutations.
// Extracted to break the circular dependency between git-changes-tab.ts
// and actions/git-changes.ts (mirrors git-prs-state.ts pattern).
// ---------------------------------------------------------------------------

// --- Types ---

interface FileEntry {
  path: string;
  status: string;
  staged: boolean;
  display: string;
}

interface RepoStatus {
  repo: string;
  is_repo: boolean;
  branch: string;
  remote: string;
  ahead: number;
  behind: number;
  files: FileEntry[];
  has_dirty: boolean;
  stashes: number;
}

export interface StageResult {
  repo: string;
  entries: Array<{ file: FileEntry; index: number }>;
}

export interface RemoveResult {
  repo: string;
  entries: Array<{ file: FileEntry; index: number }>;
}

// --- Mutable state reference (set by git-changes-tab at init time) ---

let stateRef: { repos: RepoStatus[]; paint: () => void } | null = null;

/** Called by git-changes-tab to wire up the shared state. */
export function bindChangesState(ref: { repos: RepoStatus[]; paint: () => void }): void {
  stateRef = ref;
}

/** Update the repos array reference (called after refreshChanges assigns a new array). */
export function updateReposRef(repos: RepoStatus[]): void {
  if (stateRef !== null) stateRef.repos = repos;
}

// --- Optimistic mutations ---

/** Mark files as staged optimistically. Returns undo info. */
export function stageFiles(repo: string, paths: string[]): StageResult | undefined {
  if (stateRef === null) return undefined;
  const r = stateRef.repos.find((s) => s.repo === repo);
  if (!r) return undefined;
  const pathSet = new Set(paths);
  const entries: StageResult["entries"] = [];
  for (let i = 0; i < r.files.length; i++) {
    const f = r.files[i]!;
    if (pathSet.has(f.path) && !f.staged) {
      entries.push({ file: { ...f }, index: i });
      f.staged = true;
    }
  }
  if (entries.length > 0) stateRef.paint();
  return { repo, entries };
}

/** Rollback: restore files to unstaged. */
export function rollbackStage(op: StageResult | undefined): void {
  if (!op || !stateRef) return;
  const r = stateRef.repos.find((s) => s.repo === op.repo);
  if (!r) return;
  for (const { file } of op.entries) {
    const f = r.files.find((e) => e.path === file.path);
    if (f) f.staged = false;
  }
  stateRef.paint();
}

/** Mark files as unstaged optimistically. Returns undo info. */
export function unstageFiles(repo: string, paths: string[]): StageResult | undefined {
  if (stateRef === null) return undefined;
  const r = stateRef.repos.find((s) => s.repo === repo);
  if (!r) return undefined;
  const pathSet = new Set(paths);
  const entries: StageResult["entries"] = [];
  for (let i = 0; i < r.files.length; i++) {
    const f = r.files[i]!;
    if (pathSet.has(f.path) && f.staged) {
      entries.push({ file: { ...f }, index: i });
      f.staged = false;
    }
  }
  if (entries.length > 0) stateRef.paint();
  return { repo, entries };
}

/** Rollback: restore files to staged. */
export function rollbackUnstage(op: StageResult | undefined): void {
  if (!op || !stateRef) return;
  const r = stateRef.repos.find((s) => s.repo === op.repo);
  if (!r) return;
  for (const { file } of op.entries) {
    const f = r.files.find((e) => e.path === file.path);
    if (f) f.staged = true;
  }
  stateRef.paint();
}

/** Remove files from the changes list optimistically. Returns undo info. */
export function removeFiles(repo: string, paths: string[]): RemoveResult | undefined {
  if (stateRef === null) return undefined;
  const r = stateRef.repos.find((s) => s.repo === repo);
  if (!r) return undefined;
  const pathSet = new Set(paths);
  const entries: RemoveResult["entries"] = [];
  // Collect in reverse order so indices stay valid during splice
  for (let i = r.files.length - 1; i >= 0; i--) {
    const f = r.files[i]!;
    if (pathSet.has(f.path) && !f.staged) {
      entries.push({ file: { ...f }, index: i });
      r.files.splice(i, 1);
    }
  }
  if (entries.length > 0) {
    r.has_dirty = r.files.some((f) => !f.staged);
    stateRef.paint();
  }
  return { repo, entries };
}

/** Rollback: re-insert removed files at their original positions. */
export function rollbackRemove(op: RemoveResult | undefined): void {
  if (!op || !stateRef) return;
  const r = stateRef.repos.find((s) => s.repo === op.repo);
  if (!r) return;
  // entries were collected in reverse order, restore in forward order
  for (const { file, index } of [...op.entries].reverse()) {
    if (!r.files.some((f) => f.path === file.path)) {
      r.files.splice(index, 0, file);
    }
  }
  r.has_dirty = r.files.some((f) => !f.staged);
  stateRef.paint();
}
