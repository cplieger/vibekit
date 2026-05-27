// ---------------------------------------------------------------------------
// Editor types: shared types, state container, pure predicates, and diff
// source factories. Extracted to break circular imports between editor-core,
// editor-ui, and editor-openers.
// ---------------------------------------------------------------------------

import { lineDiff, type DiffLine } from "./diff.js";
import type { ConflictFile } from "./conflict.js";

// --- Virtual path routing ---

const PLAN_DRAFT_PREFIX = "plan-draft:";
const PENDING_PREFIX = "pending:";

// --- Diff label constants ---

const DIFF_LABEL_DISK = "on disk";
const DIFF_LABEL_PROPOSED = "proposed";
const DIFF_LABEL_WORKING_TREE = "working tree";

// --- Pure predicates ---

export function isPlanDraftPath(path: string): boolean {
  return path.startsWith(PLAN_DRAFT_PREFIX);
}

export function planDraftChatID(path: string): string {
  return isPlanDraftPath(path) ? path.slice(PLAN_DRAFT_PREFIX.length) : "";
}

export function isPendingPath(path: string): boolean {
  return path.startsWith(PENDING_PREFIX);
}

export function parsePendingPath(path: string): { chatID: string; toolCallID: string } {
  if (!isPendingPath(path)) {
    return { chatID: "", toolCallID: "" };
  }
  const rest = path.slice(PENDING_PREFIX.length);
  const split = rest.indexOf(":");
  if (split === -1) {
    return { chatID: rest, toolCallID: "" };
  }
  return { chatID: rest.slice(0, split), toolCallID: rest.slice(split + 1) };
}

export function routeForPath(path: string): {
  readURL: string;
  writeURL: string;
  displayPath: string;
} {
  if (isPlanDraftPath(path)) {
    const id = planDraftChatID(path);
    const url = `/api/chats/${encodeURIComponent(id)}/plan-draft`;
    return { readURL: url, writeURL: url, displayPath: `plan draft · ${id.slice(0, 12)}` };
  }
  if (isPendingPath(path)) {
    const { toolCallID } = parsePendingPath(path);
    const url = `/api/pending-changes/${encodeURIComponent(toolCallID)}`;
    return { readURL: url, writeURL: url, displayPath: "pending change" };
  }
  const url = `/api/file?path=${encodeURIComponent(path)}`;
  return { readURL: url, writeURL: url, displayPath: path };
}

// --- Types ---

interface DiffSource {
  oldContent: string;
  newContent: string;
  oldLabel: string;
  newLabel: string;
  fromGit: boolean;
}

/** Factory: pending-change diff (on disk vs proposed). */
export function pendingDiffSource(oldContent: string, newContent: string): DiffSource {
  return {
    oldContent,
    newContent,
    oldLabel: DIFF_LABEL_DISK,
    newLabel: DIFF_LABEL_PROPOSED,
    fromGit: false,
  };
}

/** Factory: git diff (ref vs working tree). */
export function gitDiffSource(ref: string, oldContent: string, newContent: string): DiffSource {
  return {
    oldContent,
    newContent,
    oldLabel: ref,
    newLabel: DIFF_LABEL_WORKING_TREE,
    fromGit: true,
  };
}

/** Factory: unsaved-changes diff (saved vs unsaved). */
export function unsavedDiffSource(oldContent: string, newContent: string): DiffSource {
  return { oldContent, newContent, oldLabel: "saved", newLabel: "unsaved", fromGit: false };
}

/** Discriminated union for editor file mode — makes invalid state combinations unrepresentable. */
export type FileMode =
  | { kind: "edit"; editing: boolean }
  | { kind: "diff"; diffSource: DiffSource }
  | { kind: "conflict"; conflict: ConflictFile; editing: true };

export interface FileState {
  path: string;
  original: string;
  current: string;
  loaded: boolean;
  error: string;
  mode: FileMode;
  suggestions: Map<number, HunkSuggestion>;
  returnToGitDiff: { ref: string; repo: string } | null;
  /** Repo identifier for git-diff sources (empty string = default). */
  repo: string;
  pendingHunkDecisions: Map<number, "accept" | "reject">;
  pendingHunkCount: number | null;
  cachedDiff: DiffLine[] | null;
}

interface HunkSuggestion {
  loading: boolean;
  preview: string | null;
  error: string;
}

// --- EditorState class: encapsulates shared mutable state ---

class EditorState {
  readonly files = new Map<string, FileState>();
  private activePath = "";

  getActivePath(): string {
    return this.activePath;
  }
  setActivePath(path: string): void {
    this.activePath = path;
  }

  getOpenFilePaths(): string[] {
    return [...this.files.keys()];
  }

  getDirtyPaths(): string[] {
    const out: string[] = [];
    for (const [, state] of this.files) {
      if (state.loaded && state.current !== state.original) {
        out.push(state.path);
      }
    }
    return out;
  }

  freshState(path: string): FileState {
    return {
      path,
      original: "",
      current: "",
      loaded: false,
      error: "",
      mode: { kind: "edit", editing: false },
      suggestions: new Map(),
      returnToGitDiff: null,
      repo: "",
      pendingHunkDecisions: new Map(),
      pendingHunkCount: null,
      cachedDiff: null,
    };
  }

  getCachedDiff(state: FileState): DiffLine[] {
    if (state.cachedDiff !== null) {
      return state.cachedDiff;
    }
    if (state.mode.kind !== "diff") {
      return [];
    }
    const diff = lineDiff(state.mode.diffSource.oldContent, state.mode.diffSource.newContent);
    state.cachedDiff = diff;
    return diff;
  }
}

/** Singleton instance — sub-modules operate on this rather than module-level variables. */
const editorState = new EditorState();

// --- Exports (delegate to singleton) ---

export const fileStates = editorState.files;
export function setActiveFilePath(path: string): void {
  editorState.setActivePath(path);
}
export function getCachedDiff(state: FileState): DiffLine[] {
  return editorState.getCachedDiff(state);
}
export function freshState(path: string): FileState {
  return editorState.freshState(path);
}
export function getActiveFilePath(): string {
  return editorState.getActivePath();
}
export function getOpenFilePaths(): string[] {
  return editorState.getOpenFilePaths();
}
export function getDirtyEditorPaths(): string[] {
  return editorState.getDirtyPaths();
}

// --- Late-bound closeEditorFile (set by editor-openers.ts at init) ---

let closeFileFn: (path: string) => void = () => {
  /* noop */
};
export function registerCloseFile(fn: (path: string) => void): void {
  closeFileFn = fn;
}
export function closeFile(path: string): void {
  closeFileFn(path);
}
