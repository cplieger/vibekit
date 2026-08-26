// ---------------------------------------------------------------------------
// File browser: navigate workspace, create / delete / rename files and
// folders, upload via dialog or drag-drop.
//
// The picker modal (for attach-from-chat target selection) lives in
// files-picker.ts; the chat-input drag-drop overlay in files-drop.ts.
// ---------------------------------------------------------------------------

import { $, maybeViewTransition } from "./dom.js";
import { onBus, BUS_KEYS_ESCAPE } from "./bus.js";
import { getActiveTabKind, showFilesView, toggleFilesView } from "./tabs.js";
import { openFile } from "./editor-openers.js";
import { openChange } from "./navigate.js";
import { fileDownloadURL } from "./utils-url.js";
import {
  initGitStatusStore,
  onGitStatusChange,
  statusForPath,
  statusUnder,
} from "./git-status-store.js";
import { describeStatus } from "./git-types.js";
import { activeSession } from "./store.js";
import type { Session } from "./types.js";
import { confirm as confirmDialog } from "./confirm.js";
// The browser's path is a WORKSPACE preference, so it rides config.json with
// the theme rather than a per-device blob: a second device opening the browser
// should land where this one was looking. patchSettings debounces and dedups, so
// a walk through four directories costs one round trip and a repeat of the same
// path costs none.
import { patchSettings } from "./persist.js";
import { fileIcon, FILE_ICONS } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { pushRoute } from "./router.js";
import { attachPathsToActiveChat } from "./chat.js";
import { initBrowserDragDrop } from "./files-browser-drop.js";
import { initFilesSearch, resetFilesSearch } from "./files-search.js";
import { screenUploads } from "./upload-policy.js";
import * as toast from "./toast.js";
import {
  type FileEntry,
  fetchDir,
  formatSize,
  formatDate,
  joinPath,
  parentPath,
  displayPath,
  withAncestors,
  matchesRelative,
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
import { el, effect } from "@cplieger/reactive";
import { reconcile } from "./reconcile.js";
import { FileBrowserState } from "./files-state.js";
export { FileBrowserState } from "./files-state.js";

type FbEntry = { kind: "parent" } | { kind: "entry"; entry: FileEntry };

/** Per-browser abort holder — prevents picker from aborting browser fetches. */
const browserFetchHolder: FetchDirOpts = { controllerHolder: { current: null } };
registerCleanup(() => browserFetchHolder.controllerHolder.current?.abort());

const state = new FileBrowserState();

// --- Init ---

export function initFileBrowser(): void {
  $.filesBtn.addEventListener("click", () => {
    void toggleFilesView();
  });
  $.fbBack.addEventListener("click", goBack);
  $.fbForward.addEventListener("click", goForward);
  $.fbNewFile.addEventListener("click", newFile);
  $.fbNewFolder.addEventListener("click", newFolder);
  $.fbRename.addEventListener("click", renameSelected);
  $.fbDelete.addEventListener("click", deleteSelected);
  $.fbDownload.addEventListener("click", downloadSelected);
  $.fbUpload.addEventListener("click", uploadViaDialog);
  $.fbAddToChat.addEventListener("click", addSelectedToChat);
  $.fbChatFilter.addEventListener("click", () => {
    $.fbChatFilter.setAttribute("aria-pressed", String(toggleChatFilter()));
  });

  // The shared status poll, and a repaint on each of its results. Idempotent, so
  // it does not matter that the git badge and the docs page start it too. The
  // repaint is in place (see repaintRows) — a 15s poll must not disturb the
  // selection or the scroll position of a listing the user is working in.
  initGitStatusStore();
  registerCleanup(onGitStatusChange(repaintRows));

  // The filter's OTHER input is which chat is active, and the browser is a
  // singleton tab that stays open across chat switches — so without this the
  // filter would keep showing the previous chat's set until the next poll, i.e.
  // claiming one chat's writes belong to another for up to 15 seconds. Gated on
  // the filter being on and guarded by a set comparison, because `activeSession`
  // re-derives on every streaming chunk.
  effect(() => {
    const changed = chatFilterOn ? changedPathsOf(activeSession.value) : new Set<string>();
    const key = [...changed].sort().join("\n");
    if (key === lastChangedKey) {
      return;
    }
    lastChangedKey = key;
    repaintRows();
  });

  initPathInput();
  initBrowserDragDrop({
    getCurrentPath: () => state.currentPath,
    getEntryMap: () => state.entryMap,
    reload: loadDir,
  });
  initFilesSearch({
    getSearchPath: () => state.currentPath,
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
      void showFilesView();
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

  // Escape deselects all.
  onBus(BUS_KEYS_ESCAPE, () => {
    if (state.selected.size > 0) {
      state.deselectAll();
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
    if (state.selected.size !== 1) {
      return;
    }
    e.preventDefault();
    renameSelected();
  });

  // Picker uploads should refresh this view if it's open.
  setOnUploadComplete(() => {
    loadDir();
  });
}

/** True while currentPath came from a restore (the persisted fb_path or a
 *  /files deep link) and has not yet loaded successfully. A stale
 *  restored path — one outside the granted browse roots (e.g. saved
 *  before the allow-list conversion, or a revoked VIBEKIT_BROWSE_ROOTS
 *  grant) or since-deleted — auto-falls back to the root mount listing
 *  instead of stranding the user on an error row. In-session
 *  navigation failures keep the error row (with a Go-to-root escape)
 *  so a transient server error isn't papered over. */
let pendingRestore = false;

/** Restore the file browser path from settings (called from restoreAll). */
export function restoreFileBrowser(path: string): void {
  if (path !== "") {
    state.currentPath = path;
    state.history[0] = path;
    pendingRestore = true;
  }
}

/** Reset the file browser to root. */
function resetFileBrowser(): void {
  state.reset();
  resetFilesSearch();
  // Clear the DOM too: rows kept while hidden would replay their entry
  // animation in unison on the next display flip (a block translate) and
  // skip fresh mounts in reconcile. A fresh mount every open keeps the
  // entry animation deterministic; entries are refetched on every open.
  $.fbList.replaceChildren();
  void patchSettings({ fb_path: "" });
}

// `resetFileBrowser` is exported for the tab factory (tab-materialize.ts), which
// has to be able to describe a files tab's close for EVERY door rather than only
// the two inside this module. It stays the module's own function; the export just
// makes the behaviour reachable from the one place that must name it once.
export { loadDir as loadFileBrowser, resetFileBrowser };

// --- Path input ---

function initPathInput(): void {
  $.fbPath.setAttribute("aria-label", "File browser path");
  initEditablePath($.fbPath, {
    onNavigate: (target) => {
      navigate(target);
    },
    getDisplayPath: () => displayPath(state.currentPath),
  });
}

// --- Dir load ---

function loadDir(): void {
  void fetchDir(state.currentPath, browserFetchHolder).then((d) => {
    if (d.error !== undefined) {
      if (d.error === "stale") {
        return;
      }
      // Restored path no longer loads (outside the granted roots, or
      // deleted since): heal to the root mount listing once instead of
      // stranding the user on an error row they never navigated to.
      if (pendingRestore && state.currentPath !== ".") {
        pendingRestore = false;
        state.reset();
        void patchSettings({ fb_path: "" });
        updateNavButtons();
        loadDir();
        return;
      }
      state.entries = [];
      state.entryMap.clear();
      state.dirWritable = false;
      showError(d.error);
      updateWriteButtons();
      return;
    }
    pendingRestore = false;
    state.entries = d.files;
    state.entryMap.clear();
    for (const e of state.entries) {
      state.entryMap.set(e.name, e);
    }
    state.dirWritable = d.writable;
    // First populate (empty list) renders WITHOUT a view transition: the
    // view-open transition is usually still animating, and serializing a
    // second one behind it turns the row entry into a geometry-only morph
    // ("translate without fade"). Navigation between populated dirs keeps
    // its crossfade.
    renderList({ transition: $.fbList.childElementCount > 0 });
  });
}

function loadDirAsync(): Promise<void> {
  return fetchDir(state.currentPath, browserFetchHolder).then((d) => {
    if (d.error !== undefined) {
      return;
    }
    state.entries = d.files;
    state.entryMap.clear();
    for (const e of state.entries) {
      state.entryMap.set(e.name, e);
    }
    state.dirWritable = d.writable;
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
  if (state.currentPath !== ".") {
    const home = el("button", { type: "button", className: "btn-small" }, "Go to root");
    home.addEventListener("click", () => {
      navigate(".");
    });
    row.appendChild(home);
  }
  $.fbList.appendChild(row);
}

// --- Navigation ---

function navigate(path: string): void {
  state.navigate(path);
  void patchSettings({ fb_path: path });
  pushRoute({ kind: "files", path });
  updateNavButtons();
  loadWithTransition();
}

function goBack(): void {
  if (!state.goBack()) {
    return;
  }
  void patchSettings({ fb_path: state.currentPath });
  pushRoute({ kind: "files", path: state.currentPath });
  updateNavButtons();
  loadWithTransition();
}

function goForward(): void {
  if (!state.goForward()) {
    return;
  }
  void patchSettings({ fb_path: state.currentPath });
  pushRoute({ kind: "files", path: state.currentPath });
  updateNavButtons();
  loadWithTransition();
}

function loadWithTransition(): void {
  maybeViewTransition(() => {
    loadDir();
  });
}

// --- Button state ---

function updateNavButtons(): void {
  $.fbBack.disabled = state.historyIdx <= 0;
  $.fbForward.disabled = state.historyIdx >= state.history.length - 1;
  $.fbPath.value = displayPath(state.currentPath);
  $.fbPath.readOnly = true;
}

function updateActionButtons(): void {
  const count = state.selected.size;
  const single = count === 1;
  const any = count > 0;
  // Download: enabled when at least one item is selected.
  $.fbDownload.disabled = !any;
  $.fbRename.disabled = !single || !state.dirWritable;
  $.fbDelete.disabled = !any || !state.dirWritable;
  $.fbAddToChat.disabled = !any;
  updateWriteButtons();
}

function updateWriteButtons(): void {
  $.fbNewFile.disabled = !state.dirWritable;
  $.fbNewFolder.disabled = !state.dirWritable;
  $.fbUpload.disabled = !state.dirWritable;
}

// --- Render ---

function renderList(opts: { transition?: boolean } = {}): void {
  updateNavButtons();

  const swap = (): void => {
    const sorted = sortEntries(state.entries);
    state.sortedNames = sorted.map((e) => e.name);

    $.fbList.setAttribute("role", "list");

    const items: FbEntry[] = [];
    if (state.currentPath !== ".") {
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

    // Staggered list entry (design system): 30ms per row, capped at 8.
    // Set by visual position on every render; only freshly-mounted rows
    // animate, so updating the property on kept rows is inert. Runs before
    // the frame paints, so delays apply from the animation's first frame.
    let idx = 0;
    for (const row of $.fbList.children) {
      (row as HTMLElement).style.setProperty("--stagger-index", String(Math.min(idx, 8)));
      idx++;
    }

    updateActionButtons();
    updateRowHighlights();
  };

  // Wrap the content swap in a view transition for a subtle crossfade
  // when navigating between directories. Falls back to instant swap
  // on browsers without the API.
  //
  // Callers that need synchronous DOM updates (e.g. createEntry, which
  // immediately starts inline rename on the freshly-created row) pass
  // transition: false so renderList() returns with the new DOM in
  // place rather than scheduling the swap inside a microtask.
  if (opts.transition === false) {
    swap();
  } else {
    maybeViewTransition(swap);
  }
}

function parentRow(): HTMLDivElement {
  const checkSpan = el("span", { className: FB_CHECK });

  const icon = el("span", { className: "fb-icon" }, iconEl(FILE_ICONS["folder"] ?? ""));

  const nameSpan = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, "..");
  nameSpan.addEventListener("click", () => {
    navigate(parentPath(state.currentPath));
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
  return row;
}

/** Every workspace-relative path one chat changed, plus their ancestors.
 *
 *  No new request and no reverse query: `changed_files` is stamped on each turn's
 *  final assistant message, so "what did this chat touch" is a fold over data the
 *  transcript already holds. The opposite question — "which chat owns this path"
 *  — is deliberately NOT built; it would need a server-side path→chat index that
 *  nothing else wants.
 *
 *  Nor is the per-row "turn that last touched this file" link §3.10 sketches: a
 *  turn's ordinal in the loaded window is not its ordinal in the session (the
 *  store is paginated), so the link would name the wrong turn on any chat long
 *  enough to page — and the honest source, the rail's session-wide index, is a
 *  fetch and a cross-view jump the done-when does not ask for. */
function changedPathsOf(s: Session | undefined): ReadonlySet<string> {
  const rels: string[] = [];
  for (const m of s?.messages ?? []) {
    rels.push(...Object.keys(m.changed_files ?? {}));
  }
  return withAncestors(rels);
}

/** Serialized change set the last repaint was painted from. Lets the
 *  active-session effect skip the ~20/second repaints a streaming turn would
 *  otherwise trigger without changing a single row. */
let lastChangedKey = "";

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

function entryRow(entry: FileEntry): HTMLDivElement {
  const check = el("input", {
    type: "checkbox",
    className: FB_CHECK,
    checked: state.selected.has(entry.name),
  }) as HTMLInputElement;
  check.addEventListener("change", () => {
    if (check.checked) {
      state.selectEntry(entry.name);
    } else {
      state.deselectEntry(entry.name);
    }
    updateActionButtons();
    updateRowHighlights();
  });

  const icon = el("span", { className: "fb-icon" }, iconEl(fileIcon(entry.name, entry.isDir)));

  const name = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, entry.name);
  name.addEventListener("click", (e: MouseEvent) => {
    if (e.shiftKey && state.lastClickedName !== "") {
      shiftSelect(state.lastClickedName, entry.name);
      return;
    }
    if (entry.isDir) {
      navigate(joinPath(state.currentPath, entry.name));
    } else {
      openFile(joinPath(state.currentPath, entry.name));
    }
  });

  const parts: string[] = [];
  if (!entry.isDir) {
    parts.push(formatSize(entry.size));
  }
  parts.push(formatDate(entry.modTime));
  parts.push(entry.mode);
  const meta = el("span", { className: FB_META }, parts.join("   ·   "));

  const abs = joinPath(state.currentPath, entry.name);
  const badge = statusBadge(abs, entry.isDir);
  const mine = chatFilterOn && matchesRelative(abs, changedPathsOf(activeSession.peek()));

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
  // The filter DIMS rather than hides. Hiding would make the listing lie about
  // what is on disk, and a folder whose only changed child is filtered out would
  // read as empty.
  row.classList.toggle("fb-row-unattributed", chatFilterOn && !mine);

  return row;
}

/** "Changed by this chat" — off by default. */
let chatFilterOn = false;

/** Toggle the attribution filter and repaint the current listing. Returns the
 *  new state; the caller mirrors it onto the button's `aria-pressed`, which is
 *  also what makes the button LOOK pressed (see `.icon-btn[aria-pressed]`). No
 *  second class on the list: one signal for one piece of state. */
export function toggleChatFilter(): boolean {
  chatFilterOn = !chatFilterOn;
  repaintRows();
  return chatFilterOn;
}

/** Repaint every row's decoration in place: the git letter (which the poll
 *  refreshes) and the attribution dim. In place rather than a reload, because a
 *  30-second poll must not blow away the user's selection or scroll. */
function repaintRows(): void {
  const changed = chatFilterOn ? changedPathsOf(activeSession.peek()) : new Set<string>();
  for (const row of $.fbList.querySelectorAll<HTMLElement>(`.${FB_ROW}[data-path]`)) {
    const abs = row.dataset["path"] ?? "";
    const isDir = row.dataset["isDir"] === "true";
    row.querySelector(".fb-git-letter")?.remove();
    const badge = statusBadge(abs, isDir);
    if (badge !== null) {
      row.insertBefore(badge, row.querySelector(`.${FB_META}`));
    }
    row.classList.toggle("fb-row-unattributed", chatFilterOn && !matchesRelative(abs, changed));
  }
}

/** @internal Test seam: the poll's repaint callback, without the poll. */
export function _repaintRowsForTest(): void {
  repaintRows();
}

function shiftSelect(from: string, to: string): void {
  const a = state.sortedNames.indexOf(from);
  const b = state.sortedNames.indexOf(to);
  if (a === -1 || b === -1) {
    return;
  }
  const lo = Math.min(a, b);
  const hi = Math.max(a, b);
  for (let i = lo; i <= hi; i++) {
    state.selected.add(state.sortedNames[i]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  state.lastClickedName = to;
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
    node.classList.toggle("fb-row-selected", state.selected.has(name));
    const check = node.querySelector<HTMLInputElement>(`.${FB_CHECK}`);
    if (check !== null) {
      check.checked = state.selected.has(name);
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
      dir: state.currentPath,
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
  if (state.selected.size === 0) {
    return;
  }
  // Directory attachments are plain paths like anywhere else — the chat
  // input doesn't care whether it's a file or folder, both are text.
  //
  // DETACHED: a toolbar click with nothing after it that reads the chat.
  void attachPathsToActiveChat(
    [...state.selected].map((name) => joinPath(state.currentPath, name)),
  );
}

function renameSelected(): void {
  if (state.selected.size !== 1) {
    return;
  }
  startInlineRename([...state.selected][0]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
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
      { dir: state.currentPath, original, newName },
      {
        onSuccess: () => {
          // Reload the directory to rebuild rows with click handlers and
          // correct sort order (fixes stale handler + sort-after-rename).
          state.deselectAll();
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
  if (state.selected.size === 0) {
    return;
  }
  const names = [...state.selected];
  const label = names.length === 1 ? names[0]! : `${String(names.length)} items`; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  const capturedDir = state.currentPath;
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
          state.deselectAll();
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
  if (state.selected.size === 0) {
    return;
  }
  const names = [...state.selected];
  // Single file (non-directory): use the simple GET endpoint.
  // NOTE: No double-click guard here — the anchor-click approach is
  // idempotent (browser deduplicates rapid same-URL downloads). If this
  // ever becomes an issue, disable the button briefly via setTimeout.
  const singleName = names.length === 1 ? names[0] : undefined;
  if (singleName !== undefined && state.entryMap.get(singleName)?.isDir !== true) {
    // A same-origin anchor to this route, and it is safe for exactly one reason:
    // the server answers `Content-Disposition: attachment`, so a `.svg` — which
    // arrives as `Content-Type: image/svg+xml` and is script-capable when
    // navigated to — is SAVED rather than rendered as a document on vibekit's
    // origin. The `download` attribute is the same instruction from this side.
    // Never turn this into a "view in a tab" affordance.
    const a = el("a", {
      href: fileDownloadURL(joinPath(state.currentPath, singleName)),
      download: singleName,
      rel: "noopener",
    });
    document.body.appendChild(a);
    a.click();
    a.remove();
    return;
  }
  // Multiple items or includes a directory: POST for zip.
  const paths = names.map((n) => joinPath(state.currentPath, n));
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
      { files: screened.files, targetDir: state.currentPath },
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
