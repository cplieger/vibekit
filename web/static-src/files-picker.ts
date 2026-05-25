// ---------------------------------------------------------------------------
// File picker: modal for attaching workspace files to the chat.
//
// Supports multi-select via checkboxes. The "Attach" button sends all
// selected paths to the active chat's input. Folders navigate on name
// click but can also be selected (attaches the folder path). The
// "Upload here" button uploads from the local OS into the current
// folder, then auto-attaches the uploaded paths.
// ---------------------------------------------------------------------------

import { closeModal, openModal } from "./modals.js";
import { fileIcon, FILE_ICONS } from "./icons.js";
import { fetchDir, joinPath, parentPath, displayPath, errorRow, sortEntries, initEditablePath, type FetchDirOpts } from "./files-shared.js";
export type { FileEntry } from "./files-shared.js";
import { attachPathToActiveChat } from "./chat.js";
import { el } from "./dom.js";
import { upload } from "./actions/files.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";

let currentPath = ".";
const selected = new Set<string>();
let onUploadComplete: (() => void) | null = null;

/** Per-picker abort holder — prevents browser from aborting picker fetches. */
const pickerFetchHolder: FetchDirOpts = { controllerHolder: { current: null } };
registerCleanup(() => pickerFetchHolder.controllerHolder?.current?.abort());

export function setOnUploadComplete(fn: () => void): void { onUploadComplete = fn; }

export function openFilePicker(preUploadFiles?: FileList, startPath = "."): void {
  currentPath = startPath;
  selected.clear();
  loadDir();
  syncAttachBtn();
  openModal(el<HTMLDivElement>("filepicker-modal"));
  if (preUploadFiles !== undefined && preUploadFiles.length > 0) {
    performUpload(preUploadFiles);
  }
}

export function initFilePicker(): void {
  el("filepicker-close").addEventListener("click", () => {
    closeModal(el<HTMLDivElement>("filepicker-modal"));
  });

  // "Upload here" button: OS file dialog → upload → auto-attach.
  el("filepicker-upload").addEventListener("click", () => {
    const input = document.createElement("input");
    input.type = "file";
    input.multiple = true;
    input.addEventListener("change", () => {
      if (input.files === null || input.files.length === 0) return;
      performUpload(input.files);
    });
    input.click();
  });

  bindLoadingState("files.upload", el<HTMLButtonElement>("filepicker-upload"), { preserveDisabled: true });

  // "Attach" button: attach all selected paths to the chat.
  el("filepicker-attach").addEventListener("click", () => {
    if (selected.size === 0) return;
    for (const name of selected) {
      attachPathToActiveChat(joinPath(currentPath, name));
    }
    selected.clear();
    closeModal(el<HTMLDivElement>("filepicker-modal"));
  });

  const pathEl = el<HTMLInputElement>("filepicker-path");
  initEditablePath(pathEl, {
    onNavigate: (target) => {
      currentPath = target;
      selected.clear();
      loadDir();
      syncAttachBtn();
    },
    getDisplayPath: () => displayPath(currentPath),
  });
}

function performUpload(files: FileList): void {
  const modal = el<HTMLDivElement>("filepicker-modal");
  void upload.dispatch({ files, targetDir: currentPath }, {
    onSuccess: (paths) => {
      onUploadComplete?.();
      for (const p of paths) attachPathToActiveChat(p);
      closeModal(modal);
    },
  });
}

function syncAttachBtn(): void {
  const btn = el<HTMLButtonElement>("filepicker-attach");
  btn.disabled = selected.size === 0;
  btn.textContent = selected.size > 0
    ? `Attach ${String(selected.size)} item${selected.size > 1 ? "s" : ""}`
    : "Attach";
}

function loadDir(): void {
  const list = el<HTMLDivElement>("filepicker-list");
  const pathEl = el<HTMLInputElement>("filepicker-path");
  pathEl.value = displayPath(currentPath);
  pathEl.readOnly = true;
  list.replaceChildren();

  void fetchDir(currentPath, pickerFetchHolder).then((d) => {
    if (d.error !== undefined) {
      if (d.error === "stale") return;
      list.appendChild(errorRow(d.error, () => loadDir()));
      return;
    }

    const sorted = sortEntries(d.files);

    if (currentPath !== ".") list.appendChild(upRow());
    for (const f of sorted) {
      list.appendChild(entryRow(f.name, f.isDir));
    }

    if (sorted.length === 0 && currentPath === ".") {
      const empty = document.createElement("div");
      empty.className = "fb-row";
      const meta = document.createElement("span");
      meta.className = "fb-meta";
      meta.textContent = "Empty";
      empty.appendChild(meta);
      list.appendChild(empty);
    }
  });
}

function upRow(): HTMLDivElement {
  const row = document.createElement("div");
  row.className = "fb-row";

  const icon = document.createElement("span");
  icon.className = "fb-icon";
  icon.innerHTML = FILE_ICONS["folder"] ?? "";

  const nameSpan = document.createElement("span");
  nameSpan.className = "fb-name fb-name-link";
  nameSpan.textContent = "..";
  nameSpan.addEventListener("click", () => {
    currentPath = parentPath(currentPath);
    selected.clear();
    loadDir();
    syncAttachBtn();
  });

  row.append(icon, nameSpan);
  return row;
}

function entryRow(name: string, isDir: boolean): HTMLDivElement {
  const row = document.createElement("div");
  row.className = "fb-row";

  // Checkbox for multi-select.
  const check = document.createElement("input");
  check.type = "checkbox";
  check.className = "fb-check";
  check.checked = selected.has(name);
  check.addEventListener("change", () => {
    if (check.checked) selected.add(name);
    else selected.delete(name);
    syncAttachBtn();
  });

  const icon = document.createElement("span");
  icon.className = "fb-icon";
  icon.innerHTML = isDir ? (FILE_ICONS["folder"] ?? "") : fileIcon(name, false);

  const label = document.createElement("span");
  label.className = "fb-name fb-name-link";
  label.textContent = name;
  label.addEventListener("click", () => {
    if (isDir) {
      currentPath = joinPath(currentPath, name);
      selected.clear();
      loadDir();
      syncAttachBtn();
    } else {
      // Toggle checkbox on name click for files.
      check.checked = !check.checked;
      if (check.checked) selected.add(name);
      else selected.delete(name);
      syncAttachBtn();
    }
  });

  row.append(check, icon, label);
  return row;
}
