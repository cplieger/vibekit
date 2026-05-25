// ---------------------------------------------------------------------------
// File browser: navigate workspace, create / delete / rename files and
// folders, upload via dialog or drag-drop.
//
// The picker modal (for attach-from-chat target selection) lives in
// files-picker.ts; the chat-input drag-drop overlay in files-drop.ts.
// ---------------------------------------------------------------------------

import { $, maybeViewTransition } from "./dom.js";
import { onBus, BUS_KEYS_ESCAPE } from "./bus.js";
import { toggleFilesView } from "./tabs.js";
import { openFile } from "./editor-openers.js";
import { confirm as confirmDialog } from "./confirm.js";
import * as uiState from "./ui-state.js";
import { fileIcon, FILE_ICONS } from "./icons.js";
import { pushRoute } from "./router.js";
import { attachPathToActiveChat } from "./chat.js";
import { initBrowserDragDrop } from "./files-browser-drop.js";
import {
  FileEntry, fetchDir, formatSize, formatDate, joinPath, parentPath, displayPath, errorRow,
  sortEntries, initEditablePath, FetchDirOpts,
} from "./files-shared.js";
import { setOnUploadComplete } from "./files-picker.js";
import { createFile, createFolder, renameFile, deleteFilesBatch, upload, downloadFiles } from "./actions/files.js";
import { bindLoadingStateMulti, registerCleanup } from "./actions/index.js";

/** Per-browser abort holder — prevents picker from aborting browser fetches. */
const browserFetchHolder: FetchDirOpts = { controllerHolder: { current: null } };
registerCleanup(() => browserFetchHolder.controllerHolder?.current?.abort());

export class FileBrowserState {
  currentPath = ".";
  history: string[] = ["."];
  historyIdx = 0;
  selected = new Set<string>();
  lastClickedName = "";
  entries: FileEntry[] = [];
  entryMap = new Map<string, FileEntry>();
  dirWritable = true;
  sortedNames: string[] = [];

  navigate(path: string): void {
    this.currentPath = path;
    this.selected.clear();
    this.lastClickedName = "";
    this.history.length = this.historyIdx + 1;
    this.history.push(path);
    this.historyIdx = this.history.length - 1;
  }

  goBack(): boolean {
    if (this.historyIdx <= 0) return false;
    this.historyIdx--;
    this.currentPath = this.history[this.historyIdx]!;
    this.selected.clear();
    this.lastClickedName = "";
    return true;
  }

  goForward(): boolean {
    if (this.historyIdx >= this.history.length - 1) return false;
    this.historyIdx++;
    this.currentPath = this.history[this.historyIdx]!;
    this.selected.clear();
    this.lastClickedName = "";
    return true;
  }

  reset(): void {
    this.currentPath = ".";
    this.history.length = 0;
    this.history.push(".");
    this.historyIdx = 0;
    this.selected.clear();
    this.lastClickedName = "";
    this.entries = [];
    this.entryMap.clear();
    this.dirWritable = true;
    this.sortedNames = [];
  }

  selectEntry(name: string): void {
    this.selected.add(name);
    this.lastClickedName = name;
  }

  deselectEntry(name: string): void {
    this.selected.delete(name);
    this.lastClickedName = name;
  }

  deselectAll(): void {
    this.selected.clear();
  }
}

const state = new FileBrowserState();

// --- Init ---

export function initFileBrowser(): void {
  $.filesBtn.addEventListener("click", () => toggleFilesView(loadDir, resetFileBrowser));
  $.fbBack.addEventListener("click", goBack);
  $.fbForward.addEventListener("click", goForward);
  $.fbNewFile.addEventListener("click", newFile);
  $.fbNewFolder.addEventListener("click", newFolder);
  $.fbRename.addEventListener("click", renameSelected);
  $.fbDelete.addEventListener("click", deleteSelected);
  $.fbDownload.addEventListener("click", downloadSelected);
  $.fbUpload.addEventListener("click", uploadViaDialog);
  $.fbAddToChat.addEventListener("click", addSelectedToChat);

  initPathInput();
  initBrowserDragDrop({
    getCurrentPath: () => state.currentPath,
    getEntryMap: () => state.entryMap,
    reload: loadDir,
  });

  // Auto-disable buttons while any mutually-exclusive file operation is
  // in flight. Prevents races (e.g. rename + delete on the same selection).
  const fileOps = ["files.upload", "files.create_file", "files.create_folder", "files.rename", "files.delete"] as const;
  bindLoadingStateMulti(fileOps, $.fbUpload, { preserveDisabled: true });
  bindLoadingStateMulti(fileOps, $.fbNewFile, { preserveDisabled: true });
  bindLoadingStateMulti(fileOps, $.fbNewFolder, { preserveDisabled: true });
  bindLoadingStateMulti(["files.download"], $.fbDownload, { preserveDisabled: true });
  bindLoadingStateMulti(fileOps, $.fbRename, { preserveDisabled: true });
  bindLoadingStateMulti(fileOps, $.fbDelete, { preserveDisabled: true });

  // Escape deselects all.
  onBus(BUS_KEYS_ESCAPE, () => {
    if (state.selected.size > 0) {
      state.deselectAll();
      updateActionButtons();
      updateRowHighlights();
    }
  });

  // F2 renames the selected single item. No-op for zero or multi.
  document.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key !== "F2") return;
    if (document.getElementById("files-view")?.classList.contains("hidden") ?? true) return;
    if (state.selected.size !== 1) return;
    e.preventDefault();
    renameSelected();
  });

  // Picker uploads should refresh this view if it's open.
  setOnUploadComplete(() => loadDir());
}

/** Restore the file browser path from settings (called from restoreAll). */
export function restoreFileBrowser(path: string): void {
  if (path !== "") {
    state.currentPath = path;
    state.history[0] = path;
  }
}

/** Reset the file browser to root. */
function resetFileBrowser(): void {
  state.reset();
  uiState.save({ fb_path: "" });
}

export { loadDir as loadFileBrowser };

// --- Path input ---

function initPathInput(): void {
  $.fbPath.setAttribute("aria-label", "File browser path");
  initEditablePath($.fbPath, {
    onNavigate: (target) => navigate(target),
    getDisplayPath: () => displayPath(state.currentPath),
  });
}

// --- Dir load ---

function loadDir(): void {
  void fetchDir(state.currentPath, browserFetchHolder).then((d) => {
    if (d.error !== undefined) {
      if (d.error === "stale") return;
      state.entries = [];
      state.entryMap.clear();
      state.dirWritable = false;
      showError(d.error);
      updateWriteButtons();
      return;
    }
    state.entries = d.files;
    state.entryMap.clear();
    for (const e of state.entries) state.entryMap.set(e.name, e);
    state.dirWritable = d.writable;
    renderList();
  });
}

function loadDirAsync(): Promise<void> {
  return fetchDir(state.currentPath, browserFetchHolder).then((d) => {
    if (d.error !== undefined) return;
    state.entries = d.files;
    state.entryMap.clear();
    for (const e of state.entries) state.entryMap.set(e.name, e);
    state.dirWritable = d.writable;
    // transition:false so the DOM is updated synchronously — callers
    // chain inline rename on the freshly-created row immediately.
    renderList({ transition: false });
  });
}

function showError(msg: string): void {
  $.fbList.replaceChildren();
  $.fbList.appendChild(errorRow(msg, loadDir));
}

// --- Navigation ---

function navigate(path: string): void {
  state.navigate(path);
  uiState.save({ fb_path: path });
  pushRoute({ kind: "files", path });
  updateNavButtons();
  loadWithTransition();
}

function goBack(): void {
  if (!state.goBack()) return;
  uiState.save({ fb_path: state.currentPath });
  pushRoute({ kind: "files", path: state.currentPath });
  updateNavButtons();
  loadWithTransition();
}

function goForward(): void {
  if (!state.goForward()) return;
  uiState.save({ fb_path: state.currentPath });
  pushRoute({ kind: "files", path: state.currentPath });
  updateNavButtons();
  loadWithTransition();
}

function loadWithTransition(): void {
  maybeViewTransition(() => loadDir());
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
    const frag = document.createDocumentFragment();
    if (state.currentPath !== ".") frag.appendChild(parentRow());
    for (const entry of sorted) frag.appendChild(entryRow(entry));

    $.fbList.replaceChildren(frag);

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
  const row = document.createElement("div");
  row.className = "fb-row";

  const checkSpan = document.createElement("span");
  checkSpan.className = "fb-check";

  const icon = document.createElement("span");
  icon.className = "fb-icon";
  icon.innerHTML = FILE_ICONS["folder"] ?? "";

  const nameSpan = document.createElement("span");
  nameSpan.className = "fb-name fb-name-link";
  nameSpan.textContent = "..";
  nameSpan.addEventListener("click", () => navigate(parentPath(state.currentPath)));

  const metaSpan = document.createElement("span");
  metaSpan.className = "fb-meta";

  row.append(checkSpan, icon, nameSpan, metaSpan);
  return row;
}

function entryRow(entry: FileEntry): HTMLDivElement {
  const row = document.createElement("div");
  row.className = "fb-row";
  row.setAttribute("role", "listitem");
  row.dataset["name"] = entry.name;
  row.dataset["isDir"] = String(entry.isDir);

  const check = document.createElement("input");
  check.type = "checkbox";
  check.className = "fb-check";
  check.checked = state.selected.has(entry.name);
  check.addEventListener("change", () => {
    if (check.checked) state.selectEntry(entry.name);
    else state.deselectEntry(entry.name);
    updateActionButtons();
    updateRowHighlights();
  });

  const icon = document.createElement("span");
  icon.className = "fb-icon";
  icon.innerHTML = fileIcon(entry.name, entry.isDir);

  const name = document.createElement("span");
  name.className = "fb-name fb-name-link";
  name.textContent = entry.name;
  name.addEventListener("click", (e: MouseEvent) => {
    if (e.shiftKey && state.lastClickedName !== "") {
      shiftSelect(state.lastClickedName, entry.name);
      return;
    }
    if (entry.isDir) navigate(joinPath(state.currentPath, entry.name));
    else openFile(joinPath(state.currentPath, entry.name));
  });

  const meta = document.createElement("span");
  meta.className = "fb-meta";
  const parts: string[] = [];
  if (!entry.isDir) parts.push(formatSize(entry.size));
  parts.push(formatDate(entry.modTime));
  parts.push(entry.mode);
  meta.textContent = parts.join("   ·   ");

  row.append(check, icon, name, meta);

  return row;
}

function shiftSelect(from: string, to: string): void {
  const a = state.sortedNames.indexOf(from);
  const b = state.sortedNames.indexOf(to);
  if (a === -1 || b === -1) return;
  const lo = Math.min(a, b);
  const hi = Math.max(a, b);
  for (let i = lo; i <= hi; i++) state.selected.add(state.sortedNames[i]!);
  state.lastClickedName = to;
  updateActionButtons();
  updateRowHighlights();
}

function updateRowHighlights(): void {
  for (const row of [...$.fbList.children]) {
    const el = row as HTMLDivElement;
    const name = el.dataset["name"];
    if (name === undefined) continue;
    el.classList.toggle("fb-row-selected", state.selected.has(name));
    const check = el.querySelector(".fb-check") as HTMLInputElement | null;
    if (check !== null) check.checked = state.selected.has(name);
  }
}

// --- Actions ---

function newFile(): void { createEntry("touch", "new file"); }
function newFolder(): void { createEntry("mkdir", "new folder"); }

function createEntry(action: "touch" | "mkdir", name: string): void {
  const actionFn = action === "mkdir" ? createFolder : createFile;
  void actionFn.dispatch({
    dir: state.currentPath,
    name,
  }).then(async (r) => {
    if (r === null) return;
    await loadDirAsync();
    startInlineRename(name);
  });
}

function addSelectedToChat(): void {
  if (state.selected.size === 0) return;
  // Directory attachments are plain paths like anywhere else — the chat
  // input doesn't care whether it's a file or folder, both are text.
  for (const name of state.selected) {
    attachPathToActiveChat(joinPath(state.currentPath, name));
  }
}

function renameSelected(): void {
  if (state.selected.size !== 1) return;
  startInlineRename([...state.selected][0]!);
}

function startInlineRename(targetName: string): void {
  const row = [...$.fbList.children].find(
    (el) => (el as HTMLDivElement).dataset["name"] === targetName,
  ) as HTMLDivElement | undefined;
  if (row === undefined) return;

  const nameEl = row.querySelector(".fb-name") as HTMLElement;
  const original = nameEl.textContent ?? "";

  const input = document.createElement("input");
  input.type = "text";
  input.className = "fb-name-edit";
  input.value = original;
  nameEl.replaceWith(input);
  input.focus();
  const dotIdx = original.lastIndexOf(".");
  if (dotIdx > 0) input.setSelectionRange(0, dotIdx);
  else input.select();

  let committed = false;
  const restore = (text: string): HTMLElement => {
    const span = document.createElement("span");
    span.className = "fb-name fb-name-link";
    span.textContent = text;
    input.replaceWith(span);
    return span;
  };

  const commit = (): void => {
    if (committed) return;
    committed = true;
    const newName = input.value.trim();
    const span = restore(newName !== "" ? newName : original);
    if (newName === "" || newName === original) return;

    void renameFile.dispatch({ dir: state.currentPath, original, newName }, {
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
    });
  };

  const cancel = (): void => {
    if (committed) return;
    committed = true;
    restore(original);
  };

  input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter") { e.preventDefault(); commit(); }
    else if (e.key === "Escape") { e.preventDefault(); cancel(); }
  });
  input.addEventListener("blur", () => commit());
}

function deleteSelected(): void {
  if (state.selected.size === 0) return;
  const names = [...state.selected];
  const label = names.length === 1 ? names[0]! : `${String(names.length)} items`;
  const capturedDir = state.currentPath;
  void (async () => {
    const ok = await confirmDialog(`Delete ${label}? This cannot be undone.`, "Delete", "destructive");
    if (!ok) return;
    void deleteFilesBatch.dispatch({ dir: capturedDir, names, listEl: $.fbList }, {
      onSuccess: () => {
        state.deselectAll();
        updateActionButtons();
        setTimeout(loadDir, 200);
      },
      onError: () => {
        loadDir();
      },
    });
  })();
}

function downloadSelected(): void {
  if (state.selected.size === 0) return;
  const names = [...state.selected];
  // Single file (non-directory): use the simple GET endpoint.
  // NOTE: No double-click guard here — the anchor-click approach is
  // idempotent (browser deduplicates rapid same-URL downloads). If this
  // ever becomes an issue, disable the button briefly via setTimeout.
  if (names.length === 1 && state.entryMap.get(names[0]!)?.isDir !== true) {
    const a = document.createElement("a");
    a.href = `/api/file/download?path=${encodeURIComponent(joinPath(state.currentPath, names[0]!))}`;
    a.download = names[0]!;
    a.rel = "noopener";
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
  const input = document.createElement("input");
  input.type = "file";
  input.multiple = true;
  input.addEventListener("change", () => {
    if (input.files !== null && input.files.length > 0) {
      void upload.dispatch({ files: input.files, targetDir: state.currentPath }, {
        onSuccess: (paths) => {
          loadDir();
          for (const p of paths) attachPathToActiveChat(p);
        },
      });
    }
  });
  input.click();
}


