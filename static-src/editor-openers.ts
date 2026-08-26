// ---------------------------------------------------------------------------
// Editor openers: file open, load, and fetch logic.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { effect } from "@cplieger/reactive";
import { openEditorView, tabIdFor, setTabDirty, getActiveTabId } from "./tabs.js";
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

/** This module's state for one path, created on first sight.
 *
 *  TWO callers, and that is what the tab collection made necessary: `open()`,
 *  which is a reader deliberately opening a file, and `activateFile`, which is
 *  the editor tab's `onShow` and therefore also runs for a tab this device did
 *  not open — one restored from the server's set at boot, or opened on another
 *  device. Before the collection there was a third path
 *  (`restoreEditorTabs(ui.editor_files)`) seeding the map from a second list of
 *  the same paths; an editor tab's path IS its subject's `ref` now, so the seed is
 *  the activation itself.
 *
 *  The dirty binding is installed here rather than at `open()` for the same
 *  reason: a restored tab is entitled to its unsaved mark. */
function ensureFileState(path: string): FileState {
  const existing = fileStates.get(path);
  if (existing !== undefined) {
    return existing;
  }
  const created = freshState(path);
  fileStates.set(path, created);
  dirtyTabUnbinds.set(
    path,
    effect(() => {
      // Resolved on every run rather than captured: the tab id is opaque and
      // server-minted, so it does not exist until `open_tab` has answered, and
      // this effect's first run happens before that. `setTabDirty` no-ops on ""
      // and the effect re-runs on the next dirty change, by which time the row is
      // there.
      setTabDirty(tabIdFor("editor", path), created.dirty.value);
    }),
  );
  return created;
}

function open(path: string, opts: OpenOpts): void {
  saveCurrentState();
  const state = ensureFileState(path);
  state.mode.value = opts.mode;
  if (opts.repo !== undefined) {
    state.repo = opts.repo;
  }
  if (opts.line !== undefined && opts.line > 0) {
    pendingLines.set(path, opts.line);
  }
  // activateTab skips onShow for exactly one case: the tab was ALREADY active,
  // so activation is a no-op and nothing loads the file. Read before the open,
  // because openEditorView is what changes the answer.
  //
  // Activating unconditionally afterwards ran a FIRST open twice, and each
  // activation issues a /api/file read against a fresh AbortController — the
  // second one aborted the first, so the wasted round trip was invisible.
  //
  // The non-empty check is what keeps that true under OPAQUE ids: `tabIdFor`
  // answers "" for a file with no tab and `getActiveTabId` answers "" for an empty
  // strip, so a bare comparison reads two absences as a match and re-fires the
  // fallback on the first open into an empty strip — the same wasted round trip,
  // through a different door.
  const openID = tabIdFor("editor", path);
  const wasActive = openID !== "" && getActiveTabId() === openID;
  // Only the TAB half of this function moved to the projection. Everything above
  // — the mode, the repo, the pending line — is written BEFORE the tab exists and
  // has to be: they are this opener's arguments, and `activateFile` reads them the
  // moment the tab is activated. So the open is fired and the route is pushed
  // without waiting, exactly as before, and the two halves that DO need the row
  // (the already-active re-activation, and the dirty binding above) find it
  // through the one lookup.
  void openEditorView(path).then(() => {
    if (wasActive) {
      activateFile(path);
    }
  });
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
  const { oldContent, newContent, error, baseLabel } = result;
  state.mode.value = {
    kind: "diff",
    diffSource: {
      ...m.diffSource,
      // The base pane's caption is whatever the load FOUND there, not the ref
      // that was asked for: a file git owns no revision of gets "not in git"
      // rather than an empty pane captioned "HEAD", which would claim HEAD holds
      // the file and holds it empty.
      oldLabel: baseLabel,
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
  // CREATED if absent. This is the editor tab's `onShow`, so it runs for a tab
  // this device did not open — restored from the server's set at boot, or opened
  // on another device — and returning early there left the view blank with a tab
  // above it. The path is all the state needs.
  const state = ensureFileState(path);
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

// persistOpenFiles is GONE, and so is `ui-state.editor_files`. An editor tab's
// path IS its subject's `ref`, so the open set is already in the one collection
// that decides what is open — a second list of the same paths could only disagree
// with it, and did: a path in `editor_files` with no tab in `tab_order` was
// recovered as a synthetic id, which is the last consumer of the retired
// `editor:<path>` convention.

/** Tear down one open file's client state.
 *
 *  This is the editor tab's `onClose`, and nothing else calls it. It does NOT
 *  close the tab: the tab store is what invoked it, and calling back into
 *  closeTab was both redundant and the second half of an infinite loop — the
 *  store fired onClose while the tab was still present, so the call re-entered,
 *  fired onClose again, and recursed until the stack died. Every editor tab was
 *  unclosable.
 *
 *  Ownership runs one way now. The store owns the tab, this owns the file state,
 *  and neither reaches into the other. To close a file programmatically, close
 *  its tab: `closeTab(tabIdFor("editor", path))`. */
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
  const activeFilePath = getActiveFilePath();
  if (activeFilePath === path) {
    setActiveFilePath("");
  }
}
