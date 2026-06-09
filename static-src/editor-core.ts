// ---------------------------------------------------------------------------
// Editor core: init, mode switches, plan handoff.
// Types, state, and pure predicates live in editor-types.ts.
// Openers live in editor-openers.ts; UI helpers in editor-ui.ts.
//
// Extracted modules:
//   editor-types.ts    — shared types, state container, predicates
//   editor-modes.ts    — restoreUI dispatcher
//   editor-conflict.ts — conflict-mode rendering and AI merge suggestions
//   editor-pending.ts  — supervised pending-change resolution
//   editor-diff.ts     — diff source helpers
//   editor-ui.ts       — rendering helpers (gutter, highlight, mode UI)
//   editor-openers.ts  — file open, load, and fetch logic
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { confirm as confirmDialog } from "./confirm.js";
import { openEditorView } from "./tabs.js";
import { parseConflicts } from "./conflict.js";
import { saveFile as saveFileAction, sendPlan as sendPlanAction } from "./actions/editor.js";
import { bindLoadingState } from "./actions/index.js";
import { onBus, BUS_PENDING_RESOLVED, BUS_PENDING_CLEARED } from "./bus.js";
import {
  resolveActivePending,
  applyActivePendingPartial,
  openDiscussPromptForActive,
} from "./editor-pending.js";
import { renderConflictOverlay } from "./editor-conflict.js";
import {
  showReadMode,
  showEditMode,
  updateGutter,
  renderHighlight,
  renderEditModeUI,
} from "./editor-ui.js";
import { restoreUI } from "./editor-modes.js";
import { activateFile, closeEditorFile, fetchGitDiffSources } from "./editor-openers.js";
import {
  fileStates,
  getActiveFilePath,
  freshState,
  isPendingPath,
  parsePendingPath,
  makePendingPath,
  planDraftChatID,
  unsavedDiffSource,
  gitDiffSource,
} from "./editor-types.js";
import type { FileState } from "./editor-types.js";

// --- Re-exports for backward compatibility ---
// Consumers that import from editor-core.ts continue to work.

export {
  isPendingPath,
  parsePendingPath,
  isPlanDraftPath,
  routeForPath,
  getDirtyEditorPaths,
} from "./editor-types.js";
export type { FileState } from "./editor-types.js";

export function initEditor(): void {
  $.editorEditBtn.addEventListener("click", startEditing);
  $.editorCancelBtn.addEventListener("click", confirmStopEditing);
  $.editorSaveBtn.addEventListener("click", saveFile);
  $.editorDiffBtn.addEventListener("click", toggleDiffMode);
  $.editorSendPlanBtn.addEventListener("click", () => {
    void sendActivePlan();
  });

  // Registry-driven loading state: button auto-disables while the
  // dispatch is in flight. preserveDisabled keeps the
  // content-unchanged disable from the input listener intact, since
  // bindLoadingState OR's the original disabled value.
  bindLoadingState("editor.save_file", $.editorSaveBtn, { preserveDisabled: true });
  bindLoadingState("editor.send_plan", $.editorSendPlanBtn);
  $.editorPendingAcceptBtn.addEventListener("click", () => {
    void resolveActivePending("accept");
  });
  $.editorPendingRejectBtn.addEventListener("click", () => {
    void resolveActivePending("reject");
  });
  $.editorPendingApplyPartialBtn.addEventListener("click", () => {
    void applyActivePendingPartial();
  });
  bindLoadingState("editor.resolve_partial", $.editorPendingApplyPartialBtn);
  $.editorPendingDiscussBtn.addEventListener("click", () => {
    openDiscussPromptForActive();
  });

  onBus(BUS_PENDING_RESOLVED, (p) => {
    const targetPath = makePendingPath(p.chatID, p.toolCallID);
    if (fileStates.has(targetPath)) {
      closeEditorFile(targetPath);
    }
  });
  onBus(BUS_PENDING_CLEARED, (p) => {
    for (const path of [...fileStates.keys()]) {
      if (!isPendingPath(path)) {
        continue;
      }
      const { chatID } = parsePendingPath(path);
      if (chatID === p.chatID) {
        closeEditorFile(path);
      }
    }
  });
  $.editorContent.addEventListener("input", () => {
    const state = fileStates.get(getActiveFilePath());
    if (state === undefined) {
      return;
    }
    state.current = $.editorContent.value;
    $.editorSaveBtn.disabled = state.current === state.original;
    updateGutter(state.current);
    if (state.mode.value.kind === "conflict") {
      state.mode.value = {
        kind: "conflict",
        conflict: parseConflicts(state.current),
        editing: true,
      };
      renderConflictOverlay(state);
    }
  });
  $.editorContent.addEventListener("scroll", () => {
    $.editorGutter.scrollTop = $.editorContent.scrollTop;
  });
}

export function restoreEditorTabs(paths: string[]): void {
  for (const p of paths) {
    if (!fileStates.has(p)) {
      fileStates.set(p, freshState(p));
    }
    openEditorView(
      p,
      () => {
        activateFile(p);
      },
      () => {
        closeEditorFile(p);
      },
    );
  }
}

// --- Mode switches ---

function toggleDiffMode(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  if (state.mode.value.kind === "diff") {
    state.mode.value = { kind: "edit", editing: false };
    renderEditModeUI(state);
    return;
  }
  if (state.current === state.original) {
    return;
  }
  state.mode.value = {
    kind: "diff",
    diffSource: unsavedDiffSource(state.original, state.current),
  };
  restoreUI(state);
}

function startEditing(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  const m = state.mode.value;
  if (m.kind === "diff") {
    state.returnToGitDiff = m.diffSource.fromGit
      ? { ref: m.diffSource.oldLabel, repo: state.repo }
      : null;
  }
  state.mode.value = { kind: "edit", editing: true };
  $.editorContent.value = state.current;
  showEditMode();
  updateGutter(state.current);
  $.editorContent.focus();
  $.editorEditBtn.classList.add("hidden");
  $.editorDiffBtn.classList.add("hidden");
  $.editorCancelBtn.classList.remove("hidden");
  $.editorSaveBtn.classList.remove("hidden");
  $.editorSaveBtn.disabled = state.current === state.original;
}

function confirmStopEditing(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  if (state.current !== state.original) {
    void (async () => {
      const ok = await confirmDialog("Discard unsaved changes?", "Discard", "destructive");
      if (ok) {
        stopEditing(state);
      }
    })();
  } else {
    stopEditing(state);
  }
}

function stopEditing(state: FileState): void {
  // Guard: if user switched tabs during the confirm dialog, reset silently.
  if (getActiveFilePath() !== state.path) {
    state.current = state.original;
    return;
  }
  state.current = state.original;
  $.editorConflictOverlay.classList.add("hidden");
  if (state.returnToGitDiff !== null) {
    const { ref, repo } = state.returnToGitDiff;
    state.returnToGitDiff = null;
    state.mode.value = {
      kind: "diff",
      diffSource: gitDiffSource(ref, "", state.current),
    };
    void fetchGitDiffSources(state, repo, ref);
    return;
  }
  state.mode.value = { kind: "edit", editing: false };
  renderHighlight(state.original, state.path);
  showReadMode();
  $.editorEditBtn.classList.remove("hidden");
  $.editorCancelBtn.classList.add("hidden");
  $.editorSaveBtn.classList.add("hidden");
  $.editorDiffBtn.classList.add("hidden");
}

function saveFile(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  const content = $.editorContent.value;
  void saveFileAction.dispatch(
    { path: state.path, content },
    {
      onError: (e) => {
        if (getActiveFilePath() === state.path) {
          $.editorError.textContent = e.message || "Save failed";
          $.editorError.classList.remove("hidden");
        }
      },
      onSuccess: (d) => {
        if (d.error !== undefined) {
          if (getActiveFilePath() === state.path) {
            $.editorError.textContent = d.error;
            $.editorError.classList.remove("hidden");
          }
          return;
        }
        state.original = content;
        // Don't overwrite state.current — user may have edited during save.
        // Re-derive button state from live values; guard against file switch.
        if (getActiveFilePath() === state.path) {
          $.editorSaveBtn.disabled = state.current === state.original;
        }
        $.editorError.classList.add("hidden");
        if (getActiveFilePath() === state.path) {
          const m = state.mode.value;
          if (m.kind === "conflict" && m.conflict.hunks.length === 0) {
            state.mode.value = { kind: "edit", editing: false };
            renderEditModeUI(state);
          }
          if (state.returnToGitDiff !== null) {
            const { ref, repo } = state.returnToGitDiff;
            state.returnToGitDiff = null;
            state.mode.value = {
              kind: "diff",
              diffSource: gitDiffSource(ref, "", content),
            };
            void fetchGitDiffSources(state, repo, ref);
          }
        }
      },
    },
  );
}

// --- Plan handoff ---

async function sendActivePlan(): Promise<void> {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  const chatID = planDraftChatID(state.path);
  if (chatID === "") {
    return;
  }
  const content = state.current; // capture before await
  const result = await sendPlanAction.dispatch({ chatID, content });
  if (result === null) {
    if (getActiveFilePath() === state.path) {
      $.editorError.textContent = "Couldn't send plan";
      $.editorError.classList.remove("hidden");
    }
    return;
  }
  state.original = content;
}
