// ---------------------------------------------------------------------------
// Editor openers: file open, load, and fetch logic.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { effect } from "@cplieger/reactive";
import { openEditorView, tabIdFor, setTabDirty, getActiveTabId } from "./tabs.js";
import { pushRoute } from "./router.js";
import { parseConflicts } from "./conflict.js";
import { abortSuggestion, clearSuggestionState } from "./editor-conflict.js";
import { apiGet, apiGetOrError } from "./api-client.js";
import { editorDocSkeleton, paintPlaceholder } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import type { SkeletonTimingController } from "@cplieger/ui-primitives/skeleton";
import { loadDiff as loadDiffAction } from "./actions/editor.js";
import type { FileMode, FileState } from "./editor-types.js";
import {
  fileStates,
  getActiveFilePath,
  setActiveFilePath,
  routeForPath,
  freshState,
  unsavedDiffSource,
} from "./editor-types.js";
import { isViewableImage } from "./file-extensions.js";
import {
  showReadMode,
  applyPendingLine,
  fetchAgentLines,
  pendingLines,
  clearAgentLineCache,
  renderEditModeUI,
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
    state.error.value = `Failed to load diff: ${o.error.message}`;
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
  const { oldContent, newContent, error, baseLabel, workingLabel } = result;
  state.mode.value = {
    kind: "diff",
    diffSource: {
      ...m.diffSource,
      // Both captions are whatever the load FOUND there, not what was asked for:
      // a file git owns no revision of gets "not in git" rather than an empty pane
      // captioned "HEAD", which would claim HEAD holds the file and holds it
      // empty, and a file that is gone from the working tree gets "deleted"
      // rather than an empty pane captioned "working tree".
      oldLabel: baseLabel,
      newLabel: workingLabel,
      oldContent,
      newContent,
    },
  };
  if (!state.loaded) {
    state.original.value = newContent;
    state.current.value = newContent;
  }
  state.loaded = true;
  state.error.value = error;
  if (getActiveFilePath() === state.path) {
    restoreUI(state);
  }
}

/** Whether the pane paints from content the state already holds, leaving the
 *  buffer read to serve only the Edit button. True for a card's own before/after
 *  pair, false for a git diff, which has nothing until its fetch answers. */
function paintsWithoutBuffer(state: FileState): boolean {
  const m = state.mode.value;
  return m.kind === "diff" && !m.diffSource.fromGit;
}

export function activateFile(path: string): void {
  saveCurrentState();
  abortSuggestion(); // cancel any in-flight suggestion for the old file
  activeLoadController?.abort();
  activeLoadController = new AbortController();
  // CREATED if absent. This is the editor tab's `onShow`, so it runs for a tab
  // this device did not open — restored from the server's set at boot, or opened
  // on another device — and returning early there left the view blank with a tab
  // above it. The path is all the state needs.
  //
  // BEFORE `setActiveFilePath`, which is load-bearing: that write is the active-path
  // signal, and editor-core's git-diff effect re-runs on it and reads this file's
  // `error` and `mode` signals. Created afterwards, the effect's run for this path
  // finds no state, so it subscribes to neither and never re-runs for the load that
  // follows — leaving the control's answer for a restored tab pinned until the next
  // git-status scan. `open()` already creates the state first, which is why only the
  // restored-tab route was affected.
  const state = ensureFileState(path);
  setActiveFilePath(path);
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
    // A diff holding both sides is painted BEFORE the read, not behind it: the
    // read fills the buffer Edit needs, and until it lands the pane would
    // otherwise sit on the file the reader came from.
    if (paintsWithoutBuffer(state)) {
      restoreUI(state);
    }
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

/** A failed buffer read, which is not always a failed PANE.
 *
 *  Where the pane paints itself the diff stays and `loaded` stays false, which is
 *  what withholds Edit from a file there is nothing to edit. Deliberately silent:
 *  the reader asked for a diff and got one, and the absent control is the signal. */
function failBufferLoad(state: FileState, message: string): void {
  if (paintsWithoutBuffer(state)) {
    return;
  }
  state.error.value = message;
  state.loaded = true;
  restoreUI(state);
}

async function loadFile(state: FileState, signal?: AbortSignal): Promise<void> {
  // The placeholder is for a pane with nothing of its own to show. Writing it over
  // a self-contained diff is the read taking the pane down with it.
  let skeleton: SkeletonTimingController | null = null;
  if (!paintsWithoutBuffer(state)) {
    // The pane still holds the OUTGOING file's bytes: this buffer has never
    // loaded (the one caller is `activateFile`'s `!state.loaded` branch), so
    // nothing here belongs to the file being opened and the clear leads. It is
    // also what makes the pane empty for the door below — measured on the live
    // app, a known extension leaves 157 `span.hl-*` children and an unknown one
    // leaves a bare text node, so no child selector can tell one file's content
    // from another's.
    $.editorCode.replaceChildren();
    showReadMode();
    $.editorEditBtn.disabled = true;
    skeleton = skeletonTiming(() => paintPlaceholder($.editorCode, editorDocSkeleton), {
      ...(signal !== undefined ? { signal } : {}),
    });
  }

  const d = await apiGet<{ content?: string; content_hash?: string; error?: string }>(
    routeForPath(state.path).readURL,
    signal,
  );
  skeleton?.cancel();
  if (signal?.aborted === true) {
    return;
  }
  if (d === null) {
    failBufferLoad(state, "Failed to load file");
    return;
  }
  if (d.error !== undefined) {
    failBufferLoad(state, d.error);
    return;
  }
  adoptDiskBytes(state, d.content ?? "", d.content_hash ?? "");
  state.loaded = true;
  restoreUI(state);
  applyPendingLine(state.path);
}

/** Take the bytes on disk as this buffer's clean state, and let them decide the
 *  mode. TWO callers, `loadFile`'s tail and `refreshFile`'s clean-and-moved arm: a
 *  second copy of the conflict rule is a second thing that can disagree about what
 *  mode a buffer is in. The demotion arm is `editor-conflict.ts`'s, so a conflict
 *  resolved ON DISK is answered the same way as one resolved in the buffer. */
function adoptDiskBytes(state: FileState, content: string, hash: string): void {
  state.original.value = content;
  state.current.value = content;
  state.loadedHash = hash;
  state.error.value = "";
  const parsed = parseConflicts(content);
  const mode = state.mode.value.kind;
  if (parsed.hunks.length > 0 && (mode === "edit" || mode === "conflict")) {
    state.mode.value = { kind: "conflict", conflict: parsed, editing: true };
  } else if (parsed.hunks.length === 0 && mode === "conflict") {
    state.mode.value = { kind: "edit", editing: false };
  }
}

// --- Refresh: re-read a buffer that already holds bytes ---

/** Supersedes an older refresh for one path, and nothing else in this file can:
 *  `activeLoadController` is aborted only by `activateFile`, so two refreshes for one
 *  path otherwise run to completion side by side with nothing saying which is newer. */
let refreshGen = 0;

/** Re-read this file's bytes and adopt them if they moved. An editor tab's `refresh`,
 *  and the one door that may overwrite a buffer — a DIRTY buffer is the only copy of
 *  the reader's text and keeps it. */
export function refreshFile(path: string): void {
  const state = fileStates.get(path);
  // The OPEN path owns the never-loaded read: `activateFile`'s `!state.loaded` branch
  // has one in flight through the controller this function would otherwise reuse, and
  // both readings of sharing it are bad — aborting kills that read, reusing puts two
  // concurrent reads on one buffer.
  if (state?.loaded !== true) {
    return;
  }
  const m = state.mode.value;
  if (m.kind === "image") {
    return; // the surface paints from the path; there is no buffer to be stale
  }
  if (m.kind === "diff" && m.diffSource.fromGit) {
    // Both sides come from git, so the buffer read says nothing about this pane.
    // `oldLabel` IS the ref (`gitDiffSource`), and the repo rides the state.
    void fetchGitDiffSources(state, state.repo, m.diffSource.oldLabel);
    return;
  }
  const gen = ++refreshGen;
  void apiGetOrError<FileRead>(routeForPath(path).readURL, activeLoadController?.signal).then(
    (r) => {
      // Guards EVERY write below, `renderEditModeUI` and the `$.editorError` sentence
      // included.
      if (gen !== refreshGen) {
        return;
      }
      applyRefreshedRead(state, r);
    },
  );
}

interface FileRead {
  content?: string;
  content_hash?: string;
  error?: string;
}

function applyRefreshedRead(
  state: FileState,
  r: { ok: boolean; status: number; data: FileRead | null },
): void {
  if (!r.ok || r.data === null) {
    // The reader's move, so it goes to the pane's own failure channel and the buffer
    // is KEPT in `current` — it may be the only copy. Every other status is a
    // background read failing over valid content, which must say nothing.
    const gone = readGoneSentence(r.status);
    if (gone !== null) {
      state.error.value = gone;
      restoreUI(state);
    }
    return;
  }
  const d = r.data;
  if (d.error !== undefined) {
    state.error.value = d.error;
    restoreUI(state);
    return;
  }
  const content = d.content ?? "";
  const hash = d.content_hash ?? "";
  // An absent hash on either side compares "" === "" and reads as UNCHANGED, so a
  // refresh never replaces a buffer it cannot prove moved.
  if (hash === state.loadedHash) {
    return;
  }
  if (!state.dirty.value) {
    adoptDiskBytes(state, content, hash);
    restoreUI(state);
    return;
  }
  state.original.value = content;
  state.loadedHash = hash;
  if (state.mode.value.kind === "edit") {
    state.mode.value = {
      kind: "diff",
      diffSource: unsavedDiffSource(content, state.current.value),
    };
  }
  // `state.mode` has no painting subscriber, so the repaint is explicit; and
  // `restoreUI` reads a non-empty `state.error.value` as a failed PANE and would blank
  // the very diff this arm exists to show, so the sentence goes to `$.editorError`.
  renderEditModeUI(state);
  $.editorError.textContent = "This file changed on disk. Your unsaved edits are kept.";
  $.editorError.classList.remove("hidden");
}

/** The two read statuses that are an ANSWER about the file rather than a failed read. */
function readGoneSentence(status: number): string | null {
  if (status === 404) {
    return "This file is no longer on disk.";
  }
  if (status === 415) {
    return "This file is no longer text.";
  }
  return null;
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
