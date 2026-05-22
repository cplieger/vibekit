// ---------------------------------------------------------------------------
// Editor openers: file open, load, and fetch logic.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { openEditorView } from "./tabs.js";
import * as uiState from "./ui-state.js";
import { pushRoute } from "./router.js";
import { parseConflicts } from "./conflict.js";
import { abortSuggestion } from "./editor-conflict.js";
import { apiGet } from "./api-client.js";
import type { FileMode, FileState } from "./editor-types.js";
import {
  fileStates, getActiveFilePathInternal, setActiveFilePath,
  isPendingPath, routeForPath, freshState,
  pendingDiffSource, gitDiffSource, registerCloseFile,
} from "./editor-types.js";
import {
  showReadMode, applyPendingLine, fetchAgentLines, pendingLines,
} from "./editor-ui.js";
import { restoreUI } from "./editor-modes.js";

// --- Active-load cancellation ---

/** Aborted on every activateFile call to cancel stale in-flight loads. */
let activeLoadController: AbortController | null = null;

// --- Public openers ---

export function openFile(path: string, line?: number): void {
  const opts: OpenOpts = { mode: { kind: "edit", editing: false } };
  if (line !== undefined) opts.line = line;
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
        oldContent, newContent,
        oldLabel: opts.oldLabel ?? "before",
        newLabel: opts.newLabel ?? "after",
        fromGit: false,
      },
    },
  });
}

export function openFileGitDiff(path: string, ref = "HEAD", repo = ""): void {
  open(path, {
    mode: {
      kind: "diff",
      diffSource: gitDiffSource(ref, "", ""),
    },
    repo, ref,
  });
}

export function openPlanDraftPath(chatID: string): void {
  const path = "plan-draft:" + chatID;
  fileStates.delete(path);
  open(path, { mode: { kind: "edit", editing: false } });
}

export function openPendingDiff(chatID: string, toolCallID: string): void {
  if (chatID === "" || toolCallID === "") return;
  const path = `pending:${chatID}:${toolCallID}`;
  fileStates.delete(path);
  open(path, {
    mode: {
      kind: "diff",
      diffSource: pendingDiffSource("", ""),
    },
  });
}

interface OpenOpts {
  mode: FileMode;
  line?: number;
  repo?: string;
  ref?: string;
}

function open(path: string, opts: OpenOpts): void {
  saveCurrentState();
  let state = fileStates.get(path);
  if (state === undefined) {
    state = freshState(path);
    fileStates.set(path, state);
    persistOpenFiles();
  }
  state.mode = opts.mode;
  state.pendingHunkCount = null;
  state.cachedDiff = null;
  if (opts.line !== undefined && opts.line > 0) pendingLines.set(path, opts.line);
  openEditorView(path, () => activateFile(path), () => closeEditorFile(path));
  const line = opts.line;
  pushRoute(line !== undefined && line > 0
    ? { kind: "file", path, line }
    : { kind: "file", path });

  if (opts.mode.kind === "diff" && opts.mode.diffSource.fromGit === true) {
    void fetchGitDiffSources(state, opts.repo ?? "", opts.ref ?? "HEAD");
  }
}

export async function fetchGitDiffSources(state: FileState, repo: string, ref: string): Promise<void> {
  const repoParam = repo !== "" ? `&repo=${encodeURIComponent(repo)}` : "";
  const [oldD, newD] = await Promise.all([
    apiGet<{ content?: string }>(
      `/api/git/show?path=${encodeURIComponent(state.path)}&ref=${encodeURIComponent(ref)}${repoParam}`),
    apiGet<{ content?: string; error?: string }>(
      `/api/file?path=${encodeURIComponent(state.path)}`),
  ]);
  if (state.mode.kind !== "diff") return;
  if (!fileStates.has(state.path)) return;
  state.mode = {
    kind: "diff",
    diffSource: {
      ...state.mode.diffSource,
      oldContent: oldD?.content ?? "",
      newContent: newD?.content ?? "",
    },
  };
  state.pendingHunkCount = null;
  state.cachedDiff = null;
  if (!state.loaded) {
    state.original = state.mode.diffSource.newContent;
    state.current = state.mode.diffSource.newContent;
  }
  state.loaded = true;
  state.error = newD?.error ?? "";
  if (getActiveFilePathInternal() === state.path) restoreUI(state);
}

export function activateFile(path: string): void {
  saveCurrentState();
  activeLoadController?.abort();
  activeLoadController = new AbortController();
  setActiveFilePath(path);
  const state = fileStates.get(path);
  if (state === undefined) return;
  $.editorFilename.textContent = routeForPath(path).displayPath;
  $.editorError.classList.add("hidden");
  $.editorHighlight.parentElement?.scrollTo(0, 0);

  void fetchAgentLines(path, activeLoadController.signal);

  if (state.mode.kind === "diff" && state.mode.diffSource.fromGit === true && !state.loaded) {
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
  const activeFilePath = getActiveFilePathInternal();
  if (activeFilePath === "") return;
  const state = fileStates.get(activeFilePath);
  if (state !== undefined && state.loaded && (state.mode.kind === "edit" && state.mode.editing || state.mode.kind === "conflict")) {
    state.current = $.editorContent.value;
  }
}

async function loadFile(state: FileState, signal?: AbortSignal): Promise<void> {
  $.editorCode.textContent = "Loading...";
  showReadMode();
  $.editorEditBtn.disabled = true;

  if (isPendingPath(state.path)) {
    await loadPendingDiff(state, signal);
    return;
  }

  const d = await apiGet<{ content?: string; error?: string }>(routeForPath(state.path).readURL, signal);
  if (signal?.aborted === true) return;
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
  state.original = d.content ?? "";
  state.current = state.original;
  state.loaded = true;
  state.error = "";
  const parsed = parseConflicts(state.current);
  if (parsed.hunks.length > 0 && state.mode.kind === "edit") {
    state.mode = { kind: "conflict", conflict: parsed, editing: true };
  }
  restoreUI(state);
  applyPendingLine(state.path);
}

async function loadPendingDiff(state: FileState, signal?: AbortSignal): Promise<void> {
  const url = routeForPath(state.path).readURL;
  const d = await apiGet<{
    path?: string; kind?: string;
    old_text?: string; new_text?: string; truncated?: boolean;
  }>(url, signal);
  if (signal?.aborted === true) return;
  if (d === null) {
    state.error = "Change already resolved";
    state.loaded = true;
    state.original = "";
    state.current = "";
    state.mode = {
      kind: "diff",
      diffSource: pendingDiffSource("", ""),
    };
    state.pendingHunkCount = null;
    state.cachedDiff = null;
    restoreUI(state);
    return;
  }
  const oldText = d.old_text ?? "";
  const newText = d.new_text ?? "";
  state.original = newText;
  state.current = newText;
  state.loaded = true;
  state.error = "";
  state.mode = {
    kind: "diff",
    diffSource: pendingDiffSource(oldText, newText),
  };
  state.pendingHunkCount = null;
  state.cachedDiff = null;
  restoreUI(state);
  applyPendingLine(state.path);
}

function persistOpenFiles(): void {
  uiState.save({ editor_files: [...fileStates.keys()] });
}

export function closeEditorFile(path: string): void {
  const state = fileStates.get(path);
  if (state !== undefined && state.mode.kind === "conflict") abortSuggestion();
  fileStates.delete(path);
  pendingLines.delete(path);
  const activeFilePath = getActiveFilePathInternal();
  if (activeFilePath === path) setActiveFilePath("");
  persistOpenFiles();
}

// Register with editor-types so modules that can't import editor-openers
// (due to cycle risk) can still close files via the late-bound reference.
registerCloseFile(closeEditorFile);
