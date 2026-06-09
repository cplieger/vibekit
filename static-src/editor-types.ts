// ---------------------------------------------------------------------------
// Editor types: shared types, state container, pure predicates, and diff
// source factories. Extracted to break circular imports between editor-core,
// editor-ui, and editor-openers.
// ---------------------------------------------------------------------------

import { signal, computed, type Signal, type ReadonlySignal } from "@cplieger/reactive";
import { lineDiff, type DiffLine } from "./diff.js";
import { countHunks } from "./diff-pane.js";
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

/** Construct a plan-draft virtual path from a chat ID. */
export function makePlanDraftPath(chatID: string): string {
  return PLAN_DRAFT_PREFIX + chatID;
}

/** Construct a pending-change virtual path from chat + tool-call IDs. */
export function makePendingPath(chatID: string, toolCallID: string): string {
  return `${PENDING_PREFIX}${chatID}:${toolCallID}`;
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
  mode: Signal<FileMode>;
  suggestions: Map<number, HunkSuggestion>;
  returnToGitDiff: { ref: string; repo: string } | null;
  /** Repo identifier for git-diff sources (empty string = default). */
  repo: string;
  pendingHunkDecisions: Map<number, "accept" | "reject">;
  /** Hunk count of the current diff. Derived (computed) from `mode`;
   *  auto-invalidates whenever `mode` is reassigned. */
  pendingHunkCount: ReadonlySignal<number>;
  /** Line diff of the current diff source. Derived (computed) from
   *  `mode`; auto-invalidates whenever `mode` is reassigned. */
  cachedDiff: ReadonlySignal<DiffLine[]>;
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
    // `mode` is the single reactive input. `cachedDiff` and
    // `pendingHunkCount` are computeds derived from it, so they
    // auto-invalidate the moment `mode` is reassigned — no manual
    // `= null` cache busting anywhere. The diff depends ONLY on
    // `mode` (its `diffSource` is a snapshot captured at mode-entry;
    // the editor is read-only in diff mode), so `current`/`original`
    // are not inputs and stay plain fields.
    const mode = signal<FileMode>({ kind: "edit", editing: false });
    const cachedDiff = computed<DiffLine[]>(() => {
      const m = mode.value;
      return m.kind === "diff" ? lineDiff(m.diffSource.oldContent, m.diffSource.newContent) : [];
    });
    const pendingHunkCount = computed<number>(() => {
      const m = mode.value;
      return m.kind === "diff" ? countHunks(cachedDiff.value) : 0;
    });
    return {
      path,
      original: "",
      current: "",
      loaded: false,
      error: "",
      mode,
      suggestions: new Map(),
      returnToGitDiff: null,
      repo: "",
      pendingHunkDecisions: new Map(),
      pendingHunkCount,
      cachedDiff,
    };
  }

  getCachedDiff(state: FileState): DiffLine[] {
    return state.cachedDiff.value;
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
