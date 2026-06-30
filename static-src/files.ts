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
import { iconEl } from "./icon-el.js";
import { pushRoute } from "./router.js";
import { attachPathToActiveChat } from "./chat.js";
import { initBrowserDragDrop } from "./files-browser-drop.js";
import {
  type FileEntry,
  fetchDir,
  formatSize,
  formatDate,
  joinPath,
  parentPath,
  displayPath,
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
import { el } from "@cplieger/reactive";
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
    toggleFilesView(loadDir, resetFileBrowser);
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

  initPathInput();
  initBrowserDragDrop({
    getCurrentPath: () => state.currentPath,
    getEntryMap: () => state.entryMap,
    reload: loadDir,
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
  document.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key !== "F2") {
      return;
    }
    if (document.getElementById("files-view")?.classList.contains("hidden") ?? true) {
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
      state.entries = [];
      state.entryMap.clear();
      state.dirWritable = false;
      showError(d.error);
      updateWriteButtons();
      return;
    }
    state.entries = d.files;
    state.entryMap.clear();
    for (const e of state.entries) {
      state.entryMap.set(e.name, e);
    }
    state.dirWritable = d.writable;
    renderList();
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
  if (!state.goBack()) {
    return;
  }
  uiState.save({ fb_path: state.currentPath });
  pushRoute({ kind: "files", path: state.currentPath });
  updateNavButtons();
  loadWithTransition();
}

function goForward(): void {
  if (!state.goForward()) {
    return;
  }
  uiState.save({ fb_path: state.currentPath });
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

  const row = el(
    "div",
    {
      className: FB_ROW,
      role: "listitem",
      "data-name": entry.name,
      "data-is-dir": String(entry.isDir),
    },
    check,
    icon,
    name,
    meta,
  ) as HTMLDivElement;

  return row;
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
  for (const name of state.selected) {
    attachPathToActiveChat(joinPath(state.currentPath, name));
  }
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
    const a = el("a", {
      href: `/api/file/download?path=${encodeURIComponent(joinPath(state.currentPath, singleName))}`,
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
    if (input.files !== null && input.files.length > 0) {
      void upload.dispatch(
        { files: input.files, targetDir: state.currentPath },
        {
          onSuccess: (paths) => {
            loadDir();
            for (const p of paths) {
              attachPathToActiveChat(p);
            }
          },
        },
      );
    }
  });
  input.click();
}
