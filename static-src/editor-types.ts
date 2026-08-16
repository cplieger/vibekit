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

// --- Diff label constants ---

const DIFF_LABEL_WORKING_TREE = "working tree";

// --- Pure predicates ---

// THE `pending:` VIRTUAL PATH FAMILY IS GONE. isPendingPath, makePendingPath,
// parsePendingPath and pendingDiffSource addressed a staged write held in
// vibekit's memory and served from GET /api/pending-changes/. There are no staged
// writes and no such endpoint: KAS holds the content and reviews a whole turn at
// once, so a path scheme with nothing behind it is a route to a 404.
//
// routeForPath therefore has one branch, which is why it now reads as a plain
// URL builder rather than a router.
export function routeForPath(path: string): {
  readURL: string;
  writeURL: string;
  displayPath: string;
} {
  const url = `/api/file?path=${encodeURIComponent(path)}`;
  return { readURL: url, writeURL: url, displayPath: path };
}

/** Whether a path's READ state renders its markdown instead of showing raw
 *  source.
 *
 *  Keyed on the path rather than on a mode variant: the read surface is a
 *  function of what the file IS, so the state and the content it paints cannot
 *  drift apart. Editing is unaffected — the toggle shows source for every file. */
export function rendersMarkdown(path: string): boolean {
  const lower = path.toLowerCase();
  return lower.endsWith(".md") || lower.endsWith(".markdown");
}

// --- Types ---

interface DiffSource {
  oldContent: string;
  newContent: string;
  oldLabel: string;
  newLabel: string;
  fromGit: boolean;
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
  | { kind: "conflict"; conflict: ConflictFile; editing: true }
  // A variant rather than an early branch in `open()`, because `restoreUI`
  // switches exhaustively on this discriminant: adding a member makes tsc point
  // at every site that has to handle it, which an `if` at the entry point does
  // not. It carries no payload — the path is the whole input, and the bytes come
  // from the file route rather than from a loaded buffer.
  | { kind: "image" };

export interface FileState {
  path: string;
  /** Saved-on-disk content. Reactive so `dirty` can derive from it. */
  original: Signal<string>;
  /** Live editor-buffer content. Reactive so `dirty` can derive from it. */
  current: Signal<string>;
  loaded: boolean;
  /** Digest of the bytes this buffer LOADED, from the read response. Sent back on
   *  save so the server can refuse a write over an external change. Empty when
   *  unknown (a source that did not supply one), which degrades to the old
   *  write-unconditionally behaviour rather than blocking the save. */
  loadedHash: string;
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
      loadedHash: "",
      error: "",
      mode,
      dirty,
      suggestions: new Map(),
      returnToGitDiff: null,
      repo: "",
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

// There is no late-bound closeFile indirection. It existed so editor-pending.ts
// could close a `pending:` tab without importing editor-openers.ts, and that
// module is gone; every remaining caller imports closeEditorFile directly.
