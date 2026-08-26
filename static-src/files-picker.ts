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
import { iconEl } from "./icon-el.js";
import {
  fetchDir,
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
import { attachPathsToActiveChat } from "./chat.js";
import { byId } from "./dom.js";
import { upload } from "./actions/files.js";
import { screenUploads } from "./upload-policy.js";
import * as toast from "./toast.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { el } from "@cplieger/reactive";

type DirEntry = { kind: "up" } | { kind: "file"; name: string; isDir: boolean };

let currentPath = ".";
const selected = new Set<string>();
let onUploadComplete: (() => void) | null = null;

/** Per-picker abort holder — prevents browser from aborting picker fetches. */
const pickerFetchHolder: FetchDirOpts = { controllerHolder: { current: null } };
registerCleanup(() => pickerFetchHolder.controllerHolder.current?.abort());

export function setOnUploadComplete(fn: () => void): void {
  onUploadComplete = fn;
}

export function openFilePicker(preUploadFiles?: FileList, startPath = "."): void {
  currentPath = startPath;
  selected.clear();
  loadDir();
  syncAttachBtn();
  openModal(byId<HTMLDivElement>("filepicker-modal"));
  if (preUploadFiles !== undefined && preUploadFiles.length > 0) {
    performUpload(preUploadFiles);
  }
}

export function initFilePicker(): void {
  byId("filepicker-close").addEventListener("click", () => {
    closeModal(byId<HTMLDivElement>("filepicker-modal"));
  });

  // "Upload here" button: OS file dialog → upload → auto-attach.
  byId("filepicker-upload").addEventListener("click", () => {
    const input = el("input", { type: "file", multiple: true }) as HTMLInputElement;
    input.addEventListener("change", () => {
      if (input.files === null || input.files.length === 0) {
        return;
      }
      performUpload(input.files);
    });
    input.click();
  });

  bindLoadingState("files.upload", byId<HTMLButtonElement>("filepicker-upload"), {
    preserveDisabled: true,
  });

  // "Attach" button: attach all selected paths to the chat.
  byId("filepicker-attach").addEventListener("click", () => {
    if (selected.size === 0) {
      return;
    }
    // DETACHED: the modal closes on this click and nothing after reads the chat.
    void attachPathsToActiveChat([...selected].map((name) => joinPath(currentPath, name)));
    selected.clear();
    closeModal(byId<HTMLDivElement>("filepicker-modal"));
  });

  const pathEl = byId<HTMLInputElement>("filepicker-path");
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
  const modal = byId<HTMLDivElement>("filepicker-modal");
  // The fourth upload door, screened like the other three: the modal stays open
  // on a refusal so the user can pick again, which is only useful if the message
  // names what was wrong.
  const screened = screenUploads(files);
  if (screened.skipped !== "") {
    toast.error(screened.skipped);
  }
  if (screened.files === null) {
    return;
  }
  void upload.dispatch(
    { files: screened.files, targetDir: currentPath },
    {
      onSuccess: (paths) => {
        onUploadComplete?.();
        // DETACHED: an upload callback; the modal close below is independent of
        // whether the chat exists yet.
        void attachPathsToActiveChat(paths);
        closeModal(modal);
      },
    },
  );
}

function syncAttachBtn(): void {
  const btn = byId<HTMLButtonElement>("filepicker-attach");
  btn.disabled = selected.size === 0;
  btn.textContent =
    selected.size > 0
      ? `Attach ${String(selected.size)} item${selected.size > 1 ? "s" : ""}`
      : "Attach";
}

function loadDir(): void {
  const list = byId<HTMLDivElement>("filepicker-list");
  const pathEl = byId<HTMLInputElement>("filepicker-path");
  pathEl.value = displayPath(currentPath);
  pathEl.readOnly = true;

  void fetchDir(currentPath, pickerFetchHolder).then((d) => {
    // Drop any prior non-keyed siblings (error row, empty placeholder)
    // before reconciling.
    for (const child of [...list.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }

    if (d.error !== undefined) {
      if (d.error === "stale") {
        return;
      }
      // Wipe keyed children + show error row.
      reconcile(list, [], { key: () => "", mount: () => el("div") });
      list.appendChild(
        errorRow(d.error, () => {
          loadDir();
        }),
      );
      return;
    }

    const sorted = sortEntries(d.files);
    const entries: DirEntry[] = [];
    if (currentPath !== ".") {
      entries.push({ kind: "up" });
    }
    for (const f of sorted) {
      entries.push({ kind: "file", name: f.name, isDir: f.isDir });
    }

    reconcile(list, entries, {
      key: (e: DirEntry) => (e.kind === "up" ? "__up__" : `file:${e.name}`),
      mount: (e: DirEntry) => (e.kind === "up" ? upRow() : entryRow(e.name, e.isDir)),
      update: (row, e: DirEntry) => {
        if (e.kind === "file") {
          // Sync checkbox state to the live `selected` set.
          const check = row.querySelector<HTMLInputElement>(`.${FB_CHECK}`);
          if (check !== null) {
            check.checked = selected.has(e.name);
          }
        }
      },
    });

    if (sorted.length === 0 && currentPath === ".") {
      list.appendChild(
        el("div", { className: FB_ROW }, el("span", { className: FB_META }, "Empty")),
      );
    }
  });
}

function upRow(): HTMLDivElement {
  const icon = el("span", { className: "fb-icon" }, iconEl(FILE_ICONS["folder"] ?? ""));

  const nameSpan = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, "..");
  nameSpan.addEventListener("click", () => {
    currentPath = parentPath(currentPath);
    selected.clear();
    loadDir();
    syncAttachBtn();
  });

  return el("div", { className: FB_ROW }, icon, nameSpan) as HTMLDivElement;
}

function entryRow(name: string, isDir: boolean): HTMLDivElement {
  // Checkbox for multi-select.
  const check = el("input", {
    type: "checkbox",
    className: FB_CHECK,
    checked: selected.has(name),
    "aria-label": `Select ${name}`,
  }) as HTMLInputElement;
  check.addEventListener("change", () => {
    if (check.checked) {
      selected.add(name);
    } else {
      selected.delete(name);
    }
    syncAttachBtn();
  });

  const icon = el(
    "span",
    { className: "fb-icon" },
    iconEl(isDir ? (FILE_ICONS["folder"] ?? "") : fileIcon(name, false)),
  );

  const label = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, name);
  label.addEventListener("click", () => {
    if (isDir) {
      currentPath = joinPath(currentPath, name);
      selected.clear();
      loadDir();
      syncAttachBtn();
    } else {
      // Toggle checkbox on name click for files.
      check.checked = !check.checked;
      if (check.checked) {
        selected.add(name);
      } else {
        selected.delete(name);
      }
      syncAttachBtn();
    }
  });

  return el("div", { className: FB_ROW }, check, icon, label) as HTMLDivElement;
}
