// ---------------------------------------------------------------------------
// Editor types: shared types, state container, pure predicates, and diff
// source factories. Extracted to break circular imports between editor-core,
// editor-ui, and editor-openers.
// ---------------------------------------------------------------------------

import { signal, computed, type Signal, type ReadonlySignal } from "@cplieger/reactive";
import { join, split, HashedKeyError, MalformedKeyError } from "@cplieger/keyenc";
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

/** Construct a pending-change virtual path from chat + tool-call IDs.
 *
 *  The two ids are joined with keyenc so a tool-call id carrying a ":" cannot
 *  shift the split. Byte-identical to the old `pending:${chatID}:${toolCallID}`
 *  template for any id containing neither ":" nor "\\" (keyenc emits such a
 *  component verbatim), which is what lets already-persisted paths keep
 *  parsing — these keys are persisted per-device in localStorage through
 *  `persistOpenFiles` -> `uiState.save({editor_files})`. The prefix stays
 *  OUTSIDE the join so `isPendingPath` keeps working even in the (unreachable
 *  in practice) case where keyenc reduces the pair to a hashed identity. */
export function makePendingPath(chatID: string, toolCallID: string): string {
  return PENDING_PREFIX + join(chatID, toolCallID);
}

/** Recover the chat + tool-call ids from a pending-change path.
 *
 *  TOTAL, like the parser it replaces: a non-pending path, an unparseable
 *  key, or the wrong component count all return the empty pair, and every
 *  caller already treats an empty id as "not a pending change"
 *  (`editor-pending.ts`, `editor-core.ts`). keyenc's `split` throws on a
 *  hashed identity and on a key it did not produce, so both are caught here
 *  rather than escaping to callers.
 *
 *  BEHAVIOUR CHANGE (the only one): the old parser took `toolCallID` as
 *  rest-of-string, so `pending:a:b:c` parsed as chatID "a" / toolCallID "b:c".
 *  Requiring exactly two components means a LEGACY persisted path whose
 *  toolCallID contained a ":" now fails to parse once — that editor tab loads
 *  empty and can be closed. No data loss (a pending change lives server-side;
 *  the path is only a routing key) and no recurrence, because
 *  `makePendingPath` escapes such an id from now on. */
export function parsePendingPath(path: string): { chatID: string; toolCallID: string } {
  const none = { chatID: "", toolCallID: "" };
  if (!isPendingPath(path)) {
    return none;
  }
  let parts: string[];
  try {
    parts = split(path.slice(PENDING_PREFIX.length));
  } catch (e) {
    if (e instanceof HashedKeyError || e instanceof MalformedKeyError) {
      return none;
    }
    throw e;
  }
  const [chatID, toolCallID, ...extra] = parts;
  if (chatID === undefined || toolCallID === undefined || extra.length > 0) {
    return none;
  }
  return { chatID, toolCallID };
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
  /** Saved-on-disk content. Reactive so `dirty` can derive from it. */
  original: Signal<string>;
  /** Live editor-buffer content. Reactive so `dirty` can derive from it. */
  current: Signal<string>;
  loaded: boolean;
  error: string;
  mode: Signal<FileMode>;
  /** Dirty flag: true when `current` differs from `original`. Derived
   *  (computed) from those two signals; recomputes on every edit and on
   *  save (original := current). */
  dirty: ReadonlySignal<boolean>;
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
  private activePath = signal<string>("");

  getActivePath(): string {
    return this.activePath.value;
  }
  setActivePath(path: string): void {
    this.activePath.value = path;
  }

  /** Reactive: whether the active file has unsaved changes. Reads BOTH
   *  the `activePath` signal AND the active file's `dirty` computed, so
   *  it recomputes on edits (dirty flips) and on tab switch (activePath
   *  changes → re-tracks the newly-active file's `dirty`). Drives the
   *  module-level `activeDirty` computed below while keeping `activePath`
   *  encapsulated — the raw signal is never exported. */
  isActiveDirty(): boolean {
    const s = this.files.get(this.activePath.value);
    return s ? s.dirty.value : false;
  }

  getOpenFilePaths(): string[] {
    return [...this.files.keys()];
  }

  getDirtyPaths(): string[] {
    const out: string[] = [];
    for (const [, state] of this.files) {
      if (state.loaded && state.dirty.value) {
        out.push(state.path);
      }
    }
    return out;
  }

  freshState(path: string): FileState {
    // Reactive inputs: `mode`, `current`, `original`. Everything else is a
    // computed derived from them, so it auto-invalidates with no manual
    // cache busting. `cachedDiff`/`pendingHunkCount` depend ONLY on `mode`
    // (the diff's `diffSource` is a snapshot captured at mode-entry; the
    // editor is read-only in diff mode). `dirty` depends only on
    // `current`/`original`, so it flips on every edit and on save.
    const mode = signal<FileMode>({ kind: "edit", editing: false });
    const current = signal("");
    const original = signal("");
    const dirty = computed<boolean>(() => current.value !== original.value);
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
      original,
      current,
      loaded: false,
      error: "",
      mode,
      dirty,
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

/** Reactive flag: true when the active editor file has unsaved changes.
 *  The save-button effect in editor-core owns the button's disabled state
 *  by reading this (plus `isPending("editor.save_file")`). Recomputes on
 *  both edits and tab switches via `EditorState.isActiveDirty`. */
export const activeDirty = computed<boolean>(() => editorState.isActiveDirty());

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
