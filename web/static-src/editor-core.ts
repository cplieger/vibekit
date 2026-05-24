// ---------------------------------------------------------------------------
// Editor core: init, mode switches, plan handoff.
// Types, state, and pure predicates live in editor-types.ts.
// Openers live in editor-openers.ts; UI helpers in editor-ui.ts.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { confirm as confirmDialog } from "./confirm.js";
import { openEditorView } from "./tabs.js";
import { parseConflicts } from "./conflict.js";
import { saveFile as saveFileAction, sendPlan as sendPlanAction } from "./actions/editor.js";
import { onBus, BUS_PENDING_RESOLVED, BUS_PENDING_CLEARED } from "./bus.js";
import {
  resolveActivePending, applyActivePendingPartial, openDiscussPromptForActive,
} from "./editor-pending.js";
import { renderConflictOverlay } from "./editor-conflict.js";
import {
  showReadMode, showEditMode, updateGutter, renderHighlight,
  renderEditModeUI,
} from "./editor-ui.js";
import { restoreUI } from "./editor-modes.js";
import {
  activateFile, closeEditorFile, fetchGitDiffSources,
} from "./editor-openers.js";
import {
  fileStates, getActiveFilePathInternal,
  freshState, isPendingPath, parsePendingPath,
  planDraftChatID, unsavedDiffSource, gitDiffSource,
} from "./editor-types.js";
import type { FileState } from "./editor-types.js";

// --- Re-exports for backward compatibility ---
// Consumers that import from editor-core.ts continue to work.

export {
  fileStates, getActiveFilePathInternal, setActiveFilePath,
  freshState, isPendingPath, parsePendingPath, isPlanDraftPath,
  routeForPath, pendingDiffSource, gitDiffSource, getCachedDiff,
  getActiveFilePath, getOpenFilePaths, getDirtyEditorPaths,
} from "./editor-types.js";
export type { FileMode, FileState } from "./editor-types.js";

// Re-export closeEditorFile from editor-openers (editor-pending.ts imports it from here)
export { closeEditorFile } from "./editor-openers.js";

export function initEditor(): void {
  $.editorEditBtn.addEventListener("click", startEditing);
  $.editorCancelBtn.addEventListener("click", confirmStopEditing);
  $.editorSaveBtn.addEventListener("click", saveFile);
  $.editorDiffBtn.addEventListener("click", toggleDiffMode);
  $.editorSendPlanBtn.addEventListener("click", () => { void sendActivePlan(); });
  $.editorPendingAcceptBtn.addEventListener("click", () => { void resolveActivePending("accept"); });
  $.editorPendingRejectBtn.addEventListener("click", () => { void resolveActivePending("reject"); });
  $.editorPendingApplyPartialBtn.addEventListener("click", () => { void applyActivePendingPartial(); });
  $.editorPendingDiscussBtn.addEventListener("click", () => { openDiscussPromptForActive(); });

  onBus(BUS_PENDING_RESOLVED, (p) => {
    const targetPath = `pending:${p.chatID}:${p.toolCallID}`;
    if (fileStates.has(targetPath)) closeEditorFile(targetPath);
  });
  onBus(BUS_PENDING_CLEARED, (p) => {
    for (const path of [...fileStates.keys()]) {
      if (!isPendingPath(path)) continue;
      const { chatID } = parsePendingPath(path);
      if (chatID === p.chatID) closeEditorFile(path);
    }
  });
  $.editorContent.addEventListener("input", () => {
    const state = fileStates.get(getActiveFilePathInternal());
    if (state === undefined) return;
    state.current = $.editorContent.value;
    $.editorSaveBtn.disabled = state.current === state.original;
    updateGutter(state.current);
    if (state.mode.kind === "conflict") {
      state.mode = { kind: "conflict", conflict: parseConflicts(state.current), editing: true };
      renderConflictOverlay(state);
    }
  });
  $.editorContent.addEventListener("scroll", () => {
    $.editorGutter.scrollTop = $.editorContent.scrollTop;
  });
}

export function restoreEditorTabs(paths: string[]): void {
  for (const p of paths) {
    if (!fileStates.has(p)) fileStates.set(p, freshState(p));
    openEditorView(p, () => activateFile(p), () => closeEditorFile(p));
  }
}

// --- Mode switches ---

function toggleDiffMode(): void {
  const state = fileStates.get(getActiveFilePathInternal());
  if (state === undefined) return;
  if (state.mode.kind === "diff") {
    state.mode = { kind: "edit", editing: false };
    state.pendingHunkCount = null;
    renderEditModeUI(state);
    return;
  }
  if (state.current === state.original) return;
  state.mode = {
    kind: "diff",
    diffSource: unsavedDiffSource(state.original, state.current),
  };
  state.pendingHunkCount = null;
  restoreUI(state);
}

function startEditing(): void {
  const state = fileStates.get(getActiveFilePathInternal());
  if (state === undefined) return;
  if (state.mode.kind === "diff") {
    state.returnToGitDiff = state.mode.diffSource.fromGit === true
      ? { ref: state.mode.diffSource.oldLabel } : null;
    state.pendingHunkCount = null;
  }
  state.mode = { kind: "edit", editing: true };
  $.editorContent.value = state.current;
  showEditMode();
  updateGutter(state.current);
  $.editorContent.focus();
  $.editorEditBtn.classList.add("hidden");
  $.editorCancelBtn.classList.remove("hidden");
  $.editorSaveBtn.classList.remove("hidden");
  $.editorSaveBtn.disabled = state.current === state.original;
}

function confirmStopEditing(): void {
  const state = fileStates.get(getActiveFilePathInternal());
  if (state === undefined) return;
  if (state.current !== state.original) {
    void (async () => {
      const ok = await confirmDialog("Discard unsaved changes?", "Discard", "destructive");
      if (ok) stopEditing(state);
    })();
  } else {
    stopEditing(state);
  }
}

function stopEditing(state: FileState): void {
  state.current = state.original;
  $.editorConflictOverlay.classList.add("hidden");
  if (state.returnToGitDiff !== null) {
    const { ref } = state.returnToGitDiff;
    state.returnToGitDiff = null;
    state.mode = {
      kind: "diff",
      diffSource: gitDiffSource(ref, "", state.current),
    };
    state.pendingHunkCount = null;
    void fetchGitDiffSources(state, "", ref);
    return;
  }
  state.mode = { kind: "edit", editing: false };
  renderHighlight(state.original, state.path);
  showReadMode();
  $.editorEditBtn.classList.remove("hidden");
  $.editorCancelBtn.classList.add("hidden");
  $.editorSaveBtn.classList.add("hidden");
  $.editorDiffBtn.classList.add("hidden");
}

function saveFile(): void {
  const state = fileStates.get(getActiveFilePathInternal());
  if (state === undefined) return;
  const content = $.editorContent.value;
  $.editorSaveBtn.disabled = true;
  void saveFileAction.dispatch({ path: state.path, content }).then((d) => {
    if (d === null || d.error !== undefined) {
      $.editorError.textContent = d?.error ?? "Save failed";
      $.editorError.classList.remove("hidden");
      $.editorSaveBtn.disabled = false;
      return;
    }
    state.original = content;
    state.current = content;
    $.editorSaveBtn.disabled = true;
    $.editorError.classList.add("hidden");
    if (state.mode.kind === "conflict" && state.mode.conflict.hunks.length === 0) {
      state.mode = { kind: "edit", editing: false };
      renderEditModeUI(state);
    }
    if (state.returnToGitDiff !== null) {
      const { ref } = state.returnToGitDiff;
      state.returnToGitDiff = null;
      state.mode = {
        kind: "diff",
        diffSource: gitDiffSource(ref, "", content),
      };
      state.pendingHunkCount = null;
      void fetchGitDiffSources(state, "", ref);
    }
  });
}

// --- Plan handoff ---

async function sendActivePlan(): Promise<void> {
  const state = fileStates.get(getActiveFilePathInternal());
  if (state === undefined) return;
  const chatID = planDraftChatID(state.path);
  if (chatID === "") return;
  state.original = state.current;
  await sendPlanAction.dispatch({ chatID, content: state.current });
}
