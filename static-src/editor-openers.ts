// ---------------------------------------------------------------------------
// Editor openers: file open, load, and fetch logic.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { effect } from "@cplieger/reactive";
import { openEditorView, closeTab, setTabDirty } from "./tabs.js";
import * as uiState from "./ui-state.js";
import { pushRoute } from "./router.js";
import { parseConflicts } from "./conflict.js";
import { abortSuggestion, clearSuggestionState } from "./editor-conflict.js";
import { apiGet } from "./api-client.js";
import { loadDiff as loadDiffAction } from "./actions/editor.js";
import type { FileMode, FileState } from "./editor-types.js";
import {
  fileStates,
  getActiveFilePath,
  setActiveFilePath,
  routeForPath,
  freshState,
} from "./editor-types.js";
import { isViewableImage } from "./file-extensions.js";
import {
  showReadMode,
  applyPendingLine,
  fetchAgentLines,
  pendingLines,
  clearAgentLineCache,
} from "./editor-ui.js";
import { restoreUI } from "./editor-modes.js";
import { registerCleanup } from "./actions/index.js";

// --- Active-load cancellation ---

/** Aborted on every activateFile call to cancel stale in-flight loads. */
let activeLoadController: AbortController | null = null;
registerCleanup(() => activeLoadController?.abort());

// --- Public openers ---

export function openFile(path: string, line?: number): void {
  // An image opens in image mode, not edit mode: `/api/file` refuses a binary
  // with a 415 and caps the read at 2 MB, so the text path could only ever show
  // that error. `.svg` lands here too, which is the point — it is DISPLAYED in
  // an `<img>`, where it is inert, and never offered as a link on this origin.
  if (isViewableImage(path)) {
    open(path, { mode: { kind: "image" } });
    return;
  }
  const opts: OpenOpts = { mode: { kind: "edit", editing: false } };
  if (line !== undefined) {
    opts.line = line;
  }
  open(path, opts);
}

export function openFileDiff(
  path: string,
  oldContent: string,
  newContent: string,
  opts: { oldLabel?: string; newLabel?: string } = {},
): void {
  open(path, {
    mode: {
      kind: "diff",
      diffSource: {
        oldContent,
        newContent,
        oldLabel: opts.oldLabel ?? "before",
        newLabel: opts.newLabel ?? "after",
        fromGit: false,
      },
    },
  });
}

/** Open a file's diff against a git ref, FETCHING both sides.
 *
 *  The counterpart to openFileDiff, which demands both contents up front. Here
 *  the caller has only a path — which is the shape every "this changed, let me
 *  look" affordance has: a turn's ledger row, a changed filename in a tool
 *  card. `fromGit: true` is what routes `open` into fetchGitDiffSources, so the
 *  pane fills itself and reports its own load failure.
 *
 *  An earlier openFileGitDiff died with the per-file-undo row it was attached
 *  to. This one exists for the opposite reason: a changed filename IS the link
 *  to its own diff now, so the openers a filename needs are load-bearing
 *  rather than incidental. */
export function openFileGitDiff(path: string, ref = "HEAD"): void {
  open(path, {
    mode: {
      kind: "diff",
      diffSource: {
        oldContent: "",
        newContent: "",
        oldLabel: ref,
        newLabel: "working tree",
        fromGit: true,
      },
    },
    ref,
  });
}

// openPendingDiff is GONE. It opened a `pending:<chat>:<toolCall>` virtual path
// served from GET /api/pending-changes/, and neither the path family nor the
// endpoint exists: KAS holds staged content and reviews a whole turn at once.

interface OpenOpts {
  mode: FileMode;
  line?: number;
  repo?: string;
  ref?: string;
}

// Per-file dirty->tab-indicator effects, disposed on close.
const dirtyTabUnbinds = new Map<string, () => void>();

function open(path: string, opts: OpenOpts): void {
  saveCurrentState();
  let state = fileStates.get(path);
  if (state === undefined) {
    state = freshState(path);
    fileStates.set(path, state);
    persistOpenFiles();
    const created = state;
    dirtyTabUnbinds.set(
      path,
      effect(() => {
        setTabDirty(`editor:${path}`, created.dirty.value);
      }),
    );
  }
  state.mode.value = opts.mode;
  if (opts.repo !== undefined) {
    state.repo = opts.repo;
  }
  if (opts.line !== undefined && opts.line > 0) {
    pendingLines.set(path, opts.line);
  }
  openEditorView(
    path,
    () => {
      activateFile(path);
    },
    () => {
      closeEditorFile(path);
    },
  );
  // Always activate — openEditorView may skip the callback if the tab was already active.
  activateFile(path);
  const line = opts.line;
  pushRoute(line !== undefined && line > 0 ? { kind: "file", path, line } : { kind: "file", path });

  if (opts.mode.kind === "diff" && opts.mode.diffSource.fromGit) {
    void fetchGitDiffSources(state, opts.repo ?? "", opts.ref ?? "HEAD");
  }
}

export async function fetchGitDiffSources(
  state: FileState,
  repo: string,
  ref: string,
): Promise<void> {
  const o = await loadDiffAction.dispatch({ path: state.path, repo, ref }).outcome;
  if (o.status === "cancelled") {
    // A superseded/cancelled load is not an error state for the pane.
    return;
  }
  if (o.status === "error") {
    state.loaded = true;
    // The diff pane is the primary failure surface; show the real reason
    // alongside the framework's toast instead of a generic placeholder.
    state.error = `Failed to load diff: ${o.error.message}`;
    if (getActiveFilePath() === state.path) {
      restoreUI(state);
    }
    return;
  }
  const result = o.value;
  const m = state.mode.value;
  if (m.kind !== "diff") {
    return;
  }
  if (!fileStates.has(state.path)) {
    return;
  }
  const { oldContent, newContent, error } = result;
  state.mode.value = {
    kind: "diff",
    diffSource: {
      ...m.diffSource,
      oldContent,
      newContent,
    },
  };
  if (!state.loaded) {
    state.original.value = newContent;
    state.current.value = newContent;
  }
  state.loaded = true;
  state.error = error;
  if (getActiveFilePath() === state.path) {
    restoreUI(state);
  }
}

export function activateFile(path: string): void {
  saveCurrentState();
  abortSuggestion(); // cancel any in-flight suggestion for the old file
  activeLoadController?.abort();
  activeLoadController = new AbortController();
  setActiveFilePath(path);
  const state = fileStates.get(path);
  if (state === undefined) {
    return;
  }
  $.editorFilename.textContent = routeForPath(path).displayPath;
  $.editorError.classList.add("hidden");
  $.editorHighlight.parentElement?.scrollTo(0, 0);

  const m = state.mode.value;
  // An image has no text buffer, so there is nothing for `loadFile` to fetch
  // (the JSON route would answer 415) and no lines for the agent-line gutter to
  // mark. Both are skipped rather than tolerated: the surface paints from the
  // path alone, and `loaded` is set so a re-activation does not try again.
  if (m.kind === "image") {
    state.loaded = true;
    restoreUI(state);
    return;
  }

  void fetchAgentLines(path);

  if (m.kind === "diff" && m.diffSource.fromGit && !state.loaded) {
    $.editorCode.textContent = "Loading diff...";
    showReadMode();
    return;
  }
  if (!state.loaded) {
    void loadFile(state, activeLoadController.signal);
    return;
  }
  restoreUI(state);
  applyPendingLine(state.path);
}

function saveCurrentState(): void {
  const activeFilePath = getActiveFilePath();
  if (activeFilePath === "") {
    return;
  }
  const state = fileStates.get(activeFilePath);
  if (
    state !== undefined &&
    state.loaded &&
    ((state.mode.value.kind === "edit" && state.mode.value.editing) ||
      state.mode.value.kind === "conflict")
  ) {
    state.current.value = $.editorContent.value;
  }
}

async function loadFile(state: FileState, signal?: AbortSignal): Promise<void> {
  $.editorCode.textContent = "Loading...";
  showReadMode();
  $.editorEditBtn.disabled = true;

  const d = await apiGet<{ content?: string; content_hash?: string; error?: string }>(
    routeForPath(state.path).readURL,
    signal,
  );
  if (signal?.aborted === true) {
    return;
  }
  if (d === null) {
    state.error = "Failed to load file";
    state.loaded = true;
    restoreUI(state);
    return;
  }
  if (d.error !== undefined) {
    state.error = d.error;
    state.loaded = true;
    restoreUI(state);
    return;
  }
  state.original.value = d.content ?? "";
  state.current.value = state.original.value;
  state.loadedHash = d.content_hash ?? "";
  state.loaded = true;
  state.error = "";
  const parsed = parseConflicts(state.current.value);
  if (parsed.hunks.length > 0 && state.mode.value.kind === "edit") {
    state.mode.value = { kind: "conflict", conflict: parsed, editing: true };
  }
  restoreUI(state);
  applyPendingLine(state.path);
}

function persistOpenFiles(): void {
  uiState.save({ editor_files: [...fileStates.keys()] });
}

export function closeEditorFile(path: string): void {
  const state = fileStates.get(path);
  if (state?.mode.value.kind === "conflict") {
    abortSuggestion(path);
  }
  dirtyTabUnbinds.get(path)?.();
  dirtyTabUnbinds.delete(path);
  fileStates.delete(path);
  pendingLines.delete(path);
  clearAgentLineCache(path);
  clearSuggestionState(path);
  closeTab(`editor:${path}`);
  const activeFilePath = getActiveFilePath();
  if (activeFilePath === path) {
    setActiveFilePath("");
  }
  persistOpenFiles();
}
