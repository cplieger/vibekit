// ---------------------------------------------------------------------------
// File browser: navigate workspace, create / delete / rename files and
// folders, upload via dialog or drag-drop.
//
// The picker modal (for attach-from-chat target selection) lives in
// files-picker.ts; the chat-input drag-drop overlay in files-drop.ts.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { swapViews } from "./view-swap.js";
import { onBus, BUS_KEYS_ESCAPE } from "./bus.js";
import {
  filesTabIdFor,
  getActiveTabKind,
  openTab,
  renameTab,
  setFilesRoute,
  openFilesView,
  toggleFilesView,
} from "./tabs.js";
import { openFile, openFileInBackground } from "./editor-openers.js";
import { openChange } from "./navigate.js";
import { fileDownloadURL } from "./utils-url.js";
import { onGitStatusChange, statusForPath, statusUnder } from "./git-status-store.js";
import { describeStatus } from "./git-types.js";
import { confirm as confirmDialog } from "./confirm.js";
// The browser's path is a WORKSPACE preference, so it rides config.json with
// the theme rather than a per-device blob: a second device opening the browser
// should land where this one was looking. patchSettings debounces and dedups, so
// a walk through four directories costs one round trip and a repeat of the same
// path costs none.
import { patchSettings } from "./persist.js";
import { fileIcon, FILE_ICONS } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { attachPathsToActiveChat } from "./chat.js";
import { initBrowserDragDrop } from "./files-browser-drop.js";
import { closeFilesSearch, initFilesSearch, resetFilesSearch } from "./files-search.js";
import { screenUploads } from "./upload-policy.js";
import * as toast from "./toast.js";
import {
  type FileEntry,
  fetchDir,
  formatSize,
  formatDate,
  joinPath,
  parentPath,
  FB_ROOT,
  normalizeDirPath,
  errorRow,
  sortEntries,
  initEditablePath,
  type FetchDirOpts,
  FB_ROW,
  FB_NAME,
  FB_NAME_LINK,
  FB_CHECK,
  FB_META,
} from "./files-shared.js";
import { fileRowsSkeleton, paintPlaceholder } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { setOnUploadComplete } from "./files-picker.js";
import {
  createFile,
  createFolder,
  renameFile,
  deleteFilesBatch,
  upload,
  downloadFiles,
} from "./actions/files.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { el } from "@cplieger/reactive";
import { reconcile } from "./reconcile.js";
import { FileBrowserState } from "./files-state.js";
export { FileBrowserState } from "./files-state.js";

type FbEntry = { kind: "parent" } | { kind: "entry"; entry: FileEntry };

/** ONE holder for every browser: only the bound tab fetches, and a switch must
 *  ABORT the outgoing tab's in-flight read or its response paints into the incoming
 *  tab's listing. It also keeps the picker from aborting a browser fetch. */
const browserFetchHolder: FetchDirOpts = { controllerHolder: { current: null } };
registerCleanup(() => browserFetchHolder.controllerHolder.current?.abort());

/** One state per files TAB, keyed by that tab's NORMALISED ref. `filesTabIdFor` and
 *  `filesTabForRoute` normalise both sides of their compare, so one folder has one key
 *  however its ref was spelled, and the ref itself is immutable. */
const browserStates = new Map<string, FileBrowserState>();

/** The ref of the files tab the shared DOM is bound to, or "" when none is. */
let boundRef = "";

/** The bound tab's state, or a DETACHED one when no files tab is bound.
 *
 *  The detached answer is what keeps every caller total without a null check. It is
 *  not in `browserStates`, so nothing can bind to it and nothing renders from it,
 *  and it is EMPTY, so every read-and-repaint path over it is a no-op rather than a
 *  lie. The callers that would do more than read are gated at their own site. */
function cur(): FileBrowserState {
  return browserStates.get(boundRef) ?? new FileBrowserState();
}

/** The state for one TAB ref, created at `normalizeDirPath(ref)` on first sight — the
 *  ONE creation site, so a ref from the tab set (arbitrary text bounded only by
 *  MaxRefBytes) enters the path space through the door that owns it. */
function stateFor(ref: string): FileBrowserState {
  const existing = browserStates.get(ref);
  if (existing !== undefined) {
    return existing;
  }
  const created = new FileBrowserState(normalizeDirPath(ref));
  browserStates.set(ref, created);
  return created;
}

/** The folder a NEWLY opened Files tab starts at, seeded from settings and
 *  refreshed by every navigation.
 *
 *  A module-level value rather than a re-read of the settings payload, because
 *  `patchSettings` debounces: a reader is not guaranteed to see their own last
 *  write, and the recorder is the value in force. */
let defaultDir = FB_ROOT;

/** Seed the folder a NEWLY opened Files tab starts at, from the loaded settings.
 *  `""` (a fresh volume, or a workspace nobody has browsed) means the mounts
 *  listing. LOCAL only: the value came from the server, so patching it back is a
 *  write that says nothing, and the seed must not depend on `patchSettings`' dedup
 *  tracker having been seeded first. */
export function noteDefaultBrowsePath(path: string): void {
  defaultDir = normalizeDirPath(path);
}

/** That folder, FB_ROOT when none was recorded. */
export function defaultBrowsePath(): string {
  return defaultDir;
}

/** Record a navigation as the new default: the local value AND config.json. The
 *  navigation doors call this in place of a bare `patchSettings`, so the in-memory
 *  default and the persisted one cannot drift. */
function recordBrowsePath(path: string): void {
  defaultDir = normalizeDirPath(path);
  void patchSettings({ fb_path: path });
}

/** Bind the shared browser DOM to one files tab, WITHOUT loading.
 *
 *  The early return is what makes a repeat activation of the SAME browser a pure
 *  load rather than a re-paint of rows the fetch is about to replace. */
export function bindFilesTab(ref: string): void {
  if (boundRef === ref) {
    return;
  }
  const st = stateFor(ref);
  boundRef = ref;
  renameTab(filesTabIdFor(ref), filesRowName(st.currentPath));
  updateNavButtons();
  // From the state's cached entries, so the switch paints instantly and the fetch
  // that follows corrects it.
  renderList({ transition: false });
}

/** Bind and load — what a files tab's activation means. The factory's `refresh`. */
export function showFilesTab(ref: string): void {
  bindFilesTab(ref);
  loadDir();
}

/** Drop a closed tab's state, and unbind when it was the one on screen.
 *
 *  Deliberately does NOT write `fb_path`: under its narrowed meaning that field says
 *  where a NEWLY opened tab starts, so closing one of N tabs must not wipe a
 *  workspace-global preference, and the last folder anyone was looking at is still
 *  the best answer for the next tab. */
export function releaseFilesTab(ref: string): void {
  browserStates.delete(ref);
  if (boundRef !== ref) {
    return;
  }
  boundRef = "";
  resetFilesSearch();
  // Rows kept while hidden would replay their entry animation in unison on the next
  // display flip and skip fresh mounts in reconcile.
  $.fbList.replaceChildren();
}

/** Point one files TAB at a directory, from outside the module: a document history
 *  entry, or a pasted deep link, so `dir` is normalised rather than trusted.
 *
 *  By REF and not by "the bound tab", so either order is correct when an activation
 *  follows: an unbound state is pointed and the imminent `showFilesTab(ref)` loads it,
 *  a bound one is loaded here. */
export function pointFilesTab(ref: string, dir: string): void {
  const target = normalizeDirPath(dir);
  stateFor(ref).pointTo(target);
  recordBrowsePath(target);
  setFilesRoute(ref, target);
  renameTab(filesTabIdFor(ref), filesRowName(target));
  if (boundRef === ref) {
    updateNavButtons();
    loadWithTransition();
  }
}

/** The tab label for a folder: its last segment, or "Files" at the mounts listing.
 *  Twinned with tab-materialize.ts's `filesTabName`, which the factory spends at
 *  open; this is the rename the browser issues as the tab navigates. */
function filesRowName(dir: string): string {
  return dir === FB_ROOT ? "Files" : (dir.split("/").pop() ?? dir);
}

// --- Init ---

export function initFileBrowser(): void {
  $.filesBtn.addEventListener("click", () => {
    void toggleFilesView(defaultBrowsePath());
  });
  $.fbBack.addEventListener("click", goBack);
  $.fbForward.addEventListener("click", goForward);
  $.fbNewFile.addEventListener("click", () => {
    newFile();
    $.fbNewFile.closest<HTMLDetailsElement>(".fb-new-menu")?.removeAttribute("open");
  });
  $.fbNewFolder.addEventListener("click", () => {
    newFolder();
    $.fbNewFolder.closest<HTMLDetailsElement>(".fb-new-menu")?.removeAttribute("open");
  });
  $.fbRename.addEventListener("click", renameSelected);
  $.fbDelete.addEventListener("click", deleteSelected);
  $.fbDownload.addEventListener("click", downloadSelected);
  $.fbUpload.addEventListener("click", uploadViaDialog);
  $.fbAddToChat.addEventListener("click", addSelectedToChat);

  initPathInput();
  initBrowserDragDrop({
    getCurrentPath: () => cur().currentPath,
    getEntryMap: () => cur().entryMap,
    reload: loadDir,
  });
  initFilesSearch({
    // "" when nothing is bound, which the bar refuses: the detached state's FB_ROOT
    // would search the MOUNTS ROOT instead of the tab's folder.
    getSearchPath: () => (boundRef === "" ? "" : cur().currentPath),
    // SHOW, never toggle. The search surface lives inside the files view, so a
    // Ctrl-F raised from another tab has to bring the browser forward first — and
    // this used to call `toggleFilesView` on the reasoning that the caller is
    // always another tab's context. That was true when find-in-files was only
    // reachable from an editor tab, and false for the two callers it has now: the
    // browser's own search button and Ctrl-F on the files tab both run with the
    // files tab ALREADY ACTIVE, where the toggle CLOSED it. The bar then opened
    // over a departed view, the user was bounced to whichever chat took the slot,
    // and the files view was left in search mode for the next time it opened —
    // which is what a reader sees as "the file browser opens in search mode" one
    // gesture later, so the leak was reported as inherited search state rather
    // than as a tab that closed itself.
    activateBrowser: () => {
      void openFilesView(defaultBrowsePath());
    },
    // Close FIRST: `#fb-list` is hidden while the bar is open, so navigating
    // first would paint the folder into an element nobody can see.
    openFolder: (path) => {
      closeFilesSearch();
      navigate(path);
    },
  });

  // Auto-disable buttons while any mutually-exclusive file operation is
  // in flight. Prevents races (e.g. rename + delete on the same selection).
  const fileOps = [
    "files.upload",
    "files.create_file",
    "files.create_folder",
    "files.rename",
    "files.delete",
  ] as const;
  bindLoadingState(fileOps, $.fbUpload, { preserveDisabled: true });
  bindLoadingState(fileOps, $.fbNewFile, { preserveDisabled: true });
  bindLoadingState(fileOps, $.fbNewFolder, { preserveDisabled: true });
  bindLoadingState(["files.download"], $.fbDownload, { preserveDisabled: true });
  bindLoadingState(fileOps, $.fbRename, { preserveDisabled: true });
  bindLoadingState(fileOps, $.fbDelete, { preserveDisabled: true });

  // Gated on the ACTIVE tab: every Escape in the app reaches this listener, and one
  // browser's selection is not the app's to clear from another view.
  onBus(BUS_KEYS_ESCAPE, () => {
    if (getActiveTabKind() !== "files") {
      return;
    }
    if (cur().selected.size > 0) {
      cur().deselectAll();
      updateActionButtons();
      updateRowHighlights();
    }
  });

  // F2 renames the selected single item. No-op for zero or multi.
  //
  // Keyed on the tab, not the view, for find-dispatch.ts's reason: the tab store
  // already knows which tab is active, so reading it is reading the answer rather
  // than inferring it from which view element happens to be unhidden. Ctrl-F
  // already asks the question this way, and two mechanisms for one question is
  // how they drift apart.
  document.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key !== "F2") {
      return;
    }
    if (getActiveTabKind() !== "files") {
      return;
    }
    if (cur().selected.size !== 1) {
      return;
    }
    e.preventDefault();
    renameSelected();
  });

  // BOUND rather than active, so a backgrounded browser still picks an upload up; the
  // gate stops an unbound loadDir painting the detached state into the shared list.
  setOnUploadComplete(() => {
    if (boundRef !== "") {
      loadDir();
    }
  });
}

// --- Path input ---

function initPathInput(): void {
  $.fbPath.setAttribute("aria-label", "File browser path");
  initEditablePath($.fbPath, {
    onNavigate: (target) => {
      navigate(target);
    },
    getCurrentPath: () => cur().currentPath,
  });
}

// --- Dir load ---

/** Whether this surface is watching the shared git status yet. */
let gitWatched = false;

/** Start decorating rows with git status, on the browser's FIRST listing.
 *
 *  Not from `initFileBrowser`, which runs at boot: the store scans every worktree
 *  for its first subscriber, and a listing is the earliest moment a row can want a
 *  letter. Every door reaches a listing, so nothing has to arm this.
 *
 *  The repaint is in place (`repaintRows`), so a refresh cannot disturb the
 *  selection or scroll of a listing in use. */
function watchGitStatus(): void {
  if (gitWatched) {
    return;
  }
  gitWatched = true;
  registerCleanup(onGitStatusChange(repaintRows));
}

function loadDir(): void {
  watchGitStatus();
  // Only where there is nothing of this directory's own on screen AND the route has
  // not answered: a listing is refetched on every open and after every write, so an
  // arm gated on the container alone would clear rows the reader is working in, and a
  // directory that really is empty is an answer rather than an absence.
  const skeleton =
    cur().entries.length === 0 && !cur().answered
      ? skeletonTiming(() => paintPlaceholder($.fbList, fileRowsSkeleton))
      : null;
  void fetchDir(cur().currentPath, browserFetchHolder).then((d) => {
    // Read BEFORE the cancel: the placeholder shares this container with the rows, so
    // it is content on screen, and the fade is what keeps the rows from cutting over it.
    const onScreen = $.fbList.childElementCount > 0;
    skeleton?.cancel();
    if (d.error !== undefined) {
      if (d.error === "stale") {
        return;
      }
      // This tab's ORIGIN folder no longer loads: heal to the mounts listing once
      // rather than strand the reader on an error row. `fb_path` is NOT cleared —
      // one tab's failed read says nothing about a workspace-global preference.
      if (cur().pendingRestore && cur().currentPath !== FB_ROOT) {
        cur().pendingRestore = false;
        cur().reset();
        setFilesRoute(boundRef, FB_ROOT);
        updateNavButtons();
        loadDir();
        return;
      }
      cur().entries = [];
      cur().entryMap.clear();
      cur().dirWritable = false;
      showError(d.error);
      updateWriteButtons();
      return;
    }
    cur().pendingRestore = false;
    cur().entries = d.files;
    cur().answered = true;
    cur().entryMap.clear();
    for (const e of cur().entries) {
      cur().entryMap.set(e.name, e);
    }
    cur().dirWritable = d.writable;
    // First populate (empty list) renders WITHOUT the entry fade: the
    // view-open fade is usually still running, and a list fade would cancel
    // it (one-slot replacement) to animate rows the CSS stagger already
    // animates. Navigation between populated dirs keeps its fade.
    renderList({ transition: onScreen });
  });
}

function loadDirAsync(): Promise<void> {
  return fetchDir(cur().currentPath, browserFetchHolder).then((d) => {
    if (d.error !== undefined) {
      return;
    }
    cur().entries = d.files;
    cur().answered = true;
    cur().entryMap.clear();
    for (const e of cur().entries) {
      cur().entryMap.set(e.name, e);
    }
    cur().dirWritable = d.writable;
    // transition:false so the DOM is updated synchronously — callers
    // chain inline rename on the freshly-created row immediately.
    renderList({ transition: false });
  });
}

function showError(msg: string): void {
  $.fbList.replaceChildren();
  const row = errorRow(msg, loadDir);
  // Anywhere but the root: offer the way back to the mount listing.
  // Covers e.g. ".." above a nested granted root (its parent is not
  // browsable) and a directory deleted from under the browser.
  if (cur().currentPath !== FB_ROOT) {
    const home = el("button", { type: "button", className: "btn-small" }, "Go to root");
    home.addEventListener("click", () => {
      navigate(FB_ROOT);
    });
    row.appendChild(home);
  }
  $.fbList.appendChild(row);
}

// --- Navigation ---

/** Publish the bound tab's new directory: its ROW's route, its label, and the folder
 *  the next tab starts at. The route rather than a `pushRoute`, because the projection
 *  is the ONE URL writer and a second one means whichever emitted last wins. */
function publishDir(path: string): void {
  recordBrowsePath(path);
  setFilesRoute(boundRef, path);
  renameTab(filesTabIdFor(boundRef), filesRowName(path));
  updateNavButtons();
  loadWithTransition();
}

function navigate(path: string): void {
  cur().navigate(path);
  publishDir(path);
}

function goBack(): void {
  if (!cur().goBack()) {
    return;
  }
  publishDir(cur().currentPath);
}

function goForward(): void {
  if (!cur().goForward()) {
    return;
  }
  publishDir(cur().currentPath);
}

function loadWithTransition(): void {
  // The load is async, so this callback only STARTS the fetch and there is no
  // incoming element to animate here — renderList() fades the list in when the
  // new directory lands. Routing the gesture through swapViews still cancels a
  // previous entry animation immediately: a new navigation gesture wins now,
  // not when its response arrives.
  swapViews(() => {
    loadDir();
  });
}

// --- Button state ---

function updateNavButtons(): void {
  $.fbBack.disabled = cur().historyIdx <= 0;
  $.fbForward.disabled = cur().historyIdx >= cur().history.length - 1;
  $.fbPath.value = cur().currentPath;
  $.fbPath.readOnly = true;
  updateToolbarContext();
}

function updateActionButtons(): void {
  const count = cur().selected.size;
  const single = count === 1;
  const any = count > 0;
  // Download: enabled when at least one item is selected.
  $.fbDownload.disabled = !any;
  $.fbRename.disabled = !single || !cur().dirWritable;
  $.fbDelete.disabled = !any || !cur().dirWritable;
  $.fbAddToChat.disabled = !any;
  updateWriteButtons();
}

/** State the mobile toolbar's priority: navigation while browsing, selection
 *  actions while a selection exists. Both sets are still present on desktop. */
function updateToolbarContext(): void {
  $.fbBack
    .closest<HTMLElement>(".view-toolbar-inner")
    ?.classList.toggle("has-selection", cur().selected.size > 0);
}

function updateWriteButtons(): void {
  $.fbNewFile.disabled = !cur().dirWritable;
  $.fbNewFolder.disabled = !cur().dirWritable;
  $.fbUpload.disabled = !cur().dirWritable;
  $.fbNewFile
    .closest<HTMLDetailsElement>(".fb-new-menu")
    ?.toggleAttribute("data-unavailable", !cur().dirWritable);
  updateToolbarContext();
}

// --- Render ---

function renderList(opts: { transition?: boolean } = {}): void {
  updateNavButtons();

  const swap = (): HTMLElement => {
    const sorted = sortEntries(cur().entries);
    cur().sortedNames = sorted.map((e) => e.name);

    $.fbList.setAttribute("role", "list");

    const items: FbEntry[] = [];
    if (cur().currentPath !== FB_ROOT) {
      items.push({ kind: "parent" });
    }
    for (const entry of sorted) {
      items.push({ kind: "entry", entry });
    }

    reconcile($.fbList, items, {
      key: (e: FbEntry) => (e.kind === "parent" ? "__parent__" : `entry:${e.entry.name}`),
      mount: (e: FbEntry) => (e.kind === "parent" ? parentRow() : entryRow(e.entry)),
      update: (row: HTMLElement, e: FbEntry) => {
        if (e.kind !== "entry") {
          return;
        }
        // Sync metadata that can change when the directory was re-fetched
        // (size, modTime, mode). Selection state is driven by
        // updateRowHighlights() — leave that out of update.
        const meta = row.querySelector(`.${FB_META}`);
        if (meta !== null) {
          const parts: string[] = [];
          if (!e.entry.isDir) {
            parts.push(formatSize(e.entry.size));
          }
          parts.push(formatDate(e.entry.modTime));
          parts.push(e.entry.mode);
          meta.textContent = parts.join("   ·   ");
        }
      },
    });

    updateActionButtons();
    updateRowHighlights();
    return $.fbList;
  };

  // Fade the list in when navigating between directories. Callers that must
  // not animate pass transition: false — createEntry chains inline rename on
  // the freshly-created row and a whole-list fade there reads as flicker, and
  // a re-render must not cancel a running entry fade (one-slot replacement).
  // The swap itself is synchronous on both branches.
  if (opts.transition === false) {
    swap();
  } else {
    swapViews(swap);
  }
}

/** Middle-click opens in the BACKGROUND. No engine emits `click` for a middle button,
 *  so the row's own open handler cannot also fire — disjoint by construction.
 *
 *  The `mousedown` companion cancels the PLATFORM default, which `preventDefault` on
 *  auxclick does not reach: autoscroll and X11 middle-click paste are both driven by
 *  mousedown, and `.fb-list-wrap` IS a scroller. */
function wireBackgroundOpen(row: HTMLElement, open: () => void): void {
  row.addEventListener("auxclick", (e: MouseEvent) => {
    if (e.button !== 1) {
      return;
    }
    if ((e.target as HTMLElement).closest(`.${FB_CHECK}, .fb-git-letter`) !== null) {
      return;
    }
    e.preventDefault();
    open();
  });
  row.addEventListener("mousedown", (e: MouseEvent) => {
    if (e.button === 1) {
      e.preventDefault();
    }
  });
}

function parentRow(): HTMLDivElement {
  const checkSpan = el("span", { className: FB_CHECK });

  const icon = el("span", { className: "fb-icon" }, iconEl(FILE_ICONS["folder"] ?? ""));

  const nameSpan = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, "..");
  nameSpan.addEventListener("click", () => {
    navigate(parentPath(cur().currentPath));
  });

  const metaSpan = el("span", { className: FB_META });

  const row = el(
    "div",
    { className: FB_ROW },
    checkSpan,
    icon,
    nameSpan,
    metaSpan,
  ) as HTMLDivElement;
  // The same pair every listing row gets, so ".." is not the one row where the
  // gesture does nothing. One folder branch: this row carries no checkbox or badge.
  wireBackgroundOpen(row, () => {
    void openTab({
      kind: "files",
      ref: parentPath(cur().currentPath),
      activate: false,
    });
  });
  return row;
}

/** The git letter badge for one row: the file's own status, or for a directory
 *  the worst status beneath it. Clicking a file's badge opens its change.
 *
 *  The colour comes from the app's EXISTING `git-st-<letter>` vocabulary rather
 *  than a browser-local one. Those rules were authored for a per-letter tint and
 *  had no emitter at all — the git view renders one grey letter — so adopting
 *  them here gives one alphabet ONE palette instead of adding a third. */
function statusBadge(absPath: string, isDir: boolean): HTMLElement | null {
  const letter = isDir ? statusUnder(absPath) : statusForPath(absPath);
  if (letter === "") {
    return null;
  }
  const label = describeStatus(letter);
  const badge = el("span", {
    className: `fb-git-letter git-st-${letter.toLowerCase()}`,
    "data-tooltip": isDir ? `Contains changes: ${label}` : label,
    "aria-label": isDir ? `Contains changes: ${label}` : `Git status: ${label}`,
  });
  badge.textContent = letter;
  if (!isDir) {
    badge.classList.add("fb-git-clickable");
    badge.setAttribute("role", "button");
    badge.addEventListener("click", (e: MouseEvent) => {
      e.stopPropagation();
      openChange(absPath);
    });
  }
  return badge;
}

/** One listing row. */
function entryRow(entry: FileEntry): HTMLDivElement {
  const check = el("input", {
    type: "checkbox",
    className: FB_CHECK,
    checked: cur().selected.has(entry.name),
  }) as HTMLInputElement;
  check.addEventListener("change", () => {
    if (check.checked) {
      cur().selectEntry(entry.name);
    } else {
      cur().deselectEntry(entry.name);
    }
    updateActionButtons();
    updateRowHighlights();
  });

  const icon = el("span", { className: "fb-icon" }, iconEl(fileIcon(entry.name, entry.isDir)));

  const name = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, entry.name);
  name.addEventListener("click", (e: MouseEvent) => {
    if (e.shiftKey && cur().lastClickedName !== "") {
      shiftSelect(cur().lastClickedName, entry.name);
      return;
    }
    if (entry.isDir) {
      navigate(joinPath(cur().currentPath, entry.name));
    } else {
      openFile(joinPath(cur().currentPath, entry.name));
    }
  });

  const parts: string[] = [];
  if (!entry.isDir) {
    parts.push(formatSize(entry.size));
  }
  parts.push(formatDate(entry.modTime));
  parts.push(entry.mode);
  const meta = el("span", { className: FB_META }, parts.join("   ·   "));

  const abs = joinPath(cur().currentPath, entry.name);
  const badge = statusBadge(abs, entry.isDir);

  const row = el(
    "div",
    {
      className: FB_ROW,
      role: "listitem",
      "data-name": entry.name,
      "data-is-dir": String(entry.isDir),
      "data-path": abs,
    },
    check,
    icon,
    name,
    ...(badge !== null ? [badge] : []),
    meta,
  ) as HTMLDivElement;

  wireBackgroundOpen(row, () => {
    if (entry.isDir) {
      void openTab({ kind: "files", ref: normalizeDirPath(abs), activate: false });
    } else {
      openFileInBackground(abs);
    }
  });

  return row;
}

/** Repaint every row's git letter in place, which is what the poll refreshes. In
 *  place rather than a reload, because a 30-second poll must not blow away the
 *  user's selection or scroll. */
function repaintRows(): void {
  for (const row of $.fbList.querySelectorAll<HTMLElement>(`.${FB_ROW}[data-path]`)) {
    const abs = row.dataset["path"] ?? "";
    const isDir = row.dataset["isDir"] === "true";
    row.querySelector(".fb-git-letter")?.remove();
    const badge = statusBadge(abs, isDir);
    if (badge !== null) {
      row.insertBefore(badge, row.querySelector(`.${FB_META}`));
    }
  }
}

/** @internal Test seam: the poll's repaint callback, without the poll. */
export function _repaintRowsForTest(): void {
  repaintRows();
}

function shiftSelect(from: string, to: string): void {
  const a = cur().sortedNames.indexOf(from);
  const b = cur().sortedNames.indexOf(to);
  if (a === -1 || b === -1) {
    return;
  }
  const lo = Math.min(a, b);
  const hi = Math.max(a, b);
  for (let i = lo; i <= hi; i++) {
    cur().selected.add(cur().sortedNames[i]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  cur().lastClickedName = to;
  updateActionButtons();
  updateRowHighlights();
}

function updateRowHighlights(): void {
  for (const row of [...$.fbList.children]) {
    const node = row as HTMLDivElement;
    const name = node.dataset["name"];
    if (name === undefined) {
      continue;
    }
    node.classList.toggle("fb-row-selected", cur().selected.has(name));
    const check = node.querySelector<HTMLInputElement>(`.${FB_CHECK}`);
    if (check !== null) {
      check.checked = cur().selected.has(name);
    }
  }
}

// --- Actions ---

function newFile(): void {
  createEntry("touch", "new file");
}
function newFolder(): void {
  createEntry("mkdir", "new folder");
}

function createEntry(action: "touch" | "mkdir", name: string): void {
  const actionFn = action === "mkdir" ? createFolder : createFile;
  void actionFn.dispatch(
    {
      dir: cur().currentPath,
      name,
    },
    {
      onSuccess: () => {
        void loadDirAsync().then(() => {
          startInlineRename(name);
        });
      },
    },
  );
}

function addSelectedToChat(): void {
  if (cur().selected.size === 0) {
    return;
  }
  // Directory attachments are plain paths like anywhere else — the chat
  // input doesn't care whether it's a file or folder, both are text.
  //
  // DETACHED: a toolbar click with nothing after it that reads the chat.
  void attachPathsToActiveChat(
    [...cur().selected].map((name) => joinPath(cur().currentPath, name)),
  );
}

function renameSelected(): void {
  if (cur().selected.size !== 1) {
    return;
  }
  startInlineRename([...cur().selected][0]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
}

function startInlineRename(targetName: string): void {
  const row = [...$.fbList.children].find(
    (child) => (child as HTMLDivElement).dataset["name"] === targetName,
  ) as HTMLDivElement | undefined;
  if (row === undefined) {
    return;
  }

  const nameEl = row.querySelector(`.${FB_NAME}`)!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  const original = nameEl.textContent ?? ""; // eslint-disable-line @typescript-eslint/no-unnecessary-condition

  const input = el("input", {
    type: "text",
    className: "fb-name-edit",
    value: original,
  }) as HTMLInputElement;
  nameEl.replaceWith(input);
  input.focus();
  const dotIdx = original.lastIndexOf(".");
  if (dotIdx > 0) {
    input.setSelectionRange(0, dotIdx);
  } else {
    input.select();
  }

  let committed = false;
  const restore = (text: string): HTMLElement => {
    const span = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, text);
    input.replaceWith(span);
    return span;
  };

  const commit = (): void => {
    if (committed) {
      return;
    }
    committed = true;
    const newName = input.value.trim();
    const span = restore(newName !== "" ? newName : original);
    if (newName === "" || newName === original) {
      return;
    }

    void renameFile.dispatch(
      { dir: cur().currentPath, original, newName },
      {
        onSuccess: () => {
          // Reload the directory to rebuild rows with click handlers and
          // correct sort order (fixes stale handler + sort-after-rename).
          cur().deselectAll();
          updateActionButtons();
          loadDir();
        },
        onError: () => {
          span.textContent = original;
        },
      },
    );
  };

  const cancel = (): void => {
    if (committed) {
      return;
    }
    committed = true;
    restore(original);
  };

  input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      commit();
    } else if (e.key === "Escape") {
      e.preventDefault();
      cancel();
    }
  });
  input.addEventListener("blur", () => {
    commit();
  });
}

function deleteSelected(): void {
  if (cur().selected.size === 0) {
    return;
  }
  const names = [...cur().selected];
  const label = names.length === 1 ? names[0]! : `${String(names.length)} items`; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  const capturedDir = cur().currentPath;
  void (async () => {
    const ok = await confirmDialog(
      `Delete ${label}? This cannot be undone.`,
      "Delete",
      "destructive",
    );
    if (!ok) {
      return;
    }
    void deleteFilesBatch.dispatch(
      { dir: capturedDir, names, listEl: $.fbList },
      {
        onSuccess: () => {
          cur().deselectAll();
          updateActionButtons();
          setTimeout(loadDir, 200);
        },
        onError: () => {
          loadDir();
        },
      },
    );
  })();
}

function downloadSelected(): void {
  if (cur().selected.size === 0) {
    return;
  }
  const names = [...cur().selected];
  // Single file (non-directory): use the simple GET endpoint.
  // NOTE: No double-click guard here — the anchor-click approach is
  // idempotent (browser deduplicates rapid same-URL downloads). If this
  // ever becomes an issue, disable the button briefly via setTimeout.
  const singleName = names.length === 1 ? names[0] : undefined;
  if (singleName !== undefined && cur().entryMap.get(singleName)?.isDir !== true) {
    // A same-origin anchor to this route, and it is safe for exactly one reason:
    // the server answers `Content-Disposition: attachment`, so a `.svg` — which
    // arrives as `Content-Type: image/svg+xml` and is script-capable when
    // navigated to — is SAVED rather than rendered as a document on vibekit's
    // origin. The `download` attribute is the same instruction from this side.
    // Never turn this into a "view in a tab" affordance.
    const a = el("a", {
      href: fileDownloadURL(joinPath(cur().currentPath, singleName)),
      download: singleName,
      rel: "noopener",
    });
    document.body.appendChild(a);
    a.click();
    a.remove();
    return;
  }
  // Multiple items or includes a directory: POST for zip.
  const paths = names.map((n) => joinPath(cur().currentPath, n));
  void downloadFiles.dispatch({ paths });
}

function uploadViaDialog(): void {
  const input = el("input", { type: "file", multiple: true }) as HTMLInputElement;
  input.addEventListener("change", () => {
    if (input.files === null || input.files.length === 0) {
      return;
    }
    // Screened even though the file came from a dialog: the user chose the file,
    // not its size against a limit they cannot see from the OS picker, and
    // multi-select makes the TOTAL cap reachable without any single file being
    // large. Naming the file here beats a 413 that names nothing.
    const screened = screenUploads(input.files);
    if (screened.skipped !== "") {
      toast.error(screened.skipped);
    }
    if (screened.files === null) {
      return;
    }
    void upload.dispatch(
      { files: screened.files, targetDir: cur().currentPath },
      {
        onSuccess: (paths) => {
          loadDir();
          // DETACHED: an upload callback with nothing after it that reads the chat.
          void attachPathsToActiveChat(paths);
        },
      },
    );
  });
  input.click();
}
