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

import { effect } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { confirm as confirmDialog } from "./confirm.js";
import { openEditorView } from "./tabs.js";
import { parseConflicts } from "./conflict.js";
import { saveFile as saveFileAction } from "./actions/editor.js";
import { bindLoadingState, isPending } from "./actions/index.js";
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
  activeDirty,
  isPendingPath,
  parsePendingPath,
  makePendingPath,
  unsavedDiffSource,
  gitDiffSource,
} from "./editor-types.js";
import type { FileState } from "./editor-types.js";

// --- Re-exports for backward compatibility ---
// Consumers that import from editor-core.ts continue to work.

export { isPendingPath, parsePendingPath, routeForPath } from "./editor-types.js";
export type { FileState } from "./editor-types.js";

export function initEditor(): void {
  $.editorEditBtn.addEventListener("click", startEditing);
  $.editorCancelBtn.addEventListener("click", confirmStopEditing);
  $.editorSaveBtn.addEventListener("click", saveFile);
  $.editorDiffBtn.addEventListener("click", toggleDiffMode);
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
  let conflictReparseQueued = false;
  $.editorContent.addEventListener("input", () => {
    const state = fileStates.get(getActiveFilePath());
    if (state === undefined) {
      return;
    }
    state.current.value = $.editorContent.value;
    updateGutter(state.current.value);
    if (state.mode.value.kind === "conflict" && !conflictReparseQueued) {
      // Debounce the O(lines) re-parse + overlay rebuild to one animation
      // frame: running it synchronously on EVERY keystroke janked typing
      // in large conflicted files on slow devices. The frame callback
      // re-resolves the active state (tab may have switched meanwhile).
      conflictReparseQueued = true;
      requestAnimationFrame(() => {
        conflictReparseQueued = false;
        const st = fileStates.get(getActiveFilePath());
        if (st?.mode.value.kind !== "conflict") {
          return;
        }
        st.mode.value = {
          kind: "conflict",
          conflict: parseConflicts(st.current.value),
          editing: true,
        };
        renderConflictOverlay(st);
      });
    }
  });
  $.editorContent.addEventListener("scroll", () => {
    $.editorGutter.scrollTop = $.editorContent.scrollTop;
  });

  // Sole owner of the save button's disabled state: disabled when the
  // active file is clean OR a save is in flight; enabled exactly when the
  // active file is dirty and no save is running. `activeDirty` re-tracks on
  // edits and tab switches; `isPending` is signal-backed, so this one effect
  // replaces the former bindLoadingState(save_file) plus every scattered
  // imperative `disabled = current === original` write.
  effect(() => {
    $.editorSaveBtn.disabled = !activeDirty.value || isPending("editor.save_file");
  });
}

export function restoreEditorTabs(paths: string[]): void {
  for (const p of paths) {
    if (!fileStates.has(p)) {
      fileStates.set(p, freshState(p));
    }
    // Open WITHOUT activating (B8): activation runs activateFile, which
    // fetches the file — restoring N saved editor tabs active fanned out
    // N pre-auth fetches at boot. The saved active tab (if it's an editor
    // tab) is activated exactly once by restoreTabState(), which fetches
    // just that file; the rest load lazily on first click.
    openEditorView(
      p,
      () => {
        activateFile(p);
      },
      () => {
        closeEditorFile(p);
      },
      { activate: false },
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
  if (state.current.value === state.original.value) {
    return;
  }
  state.mode.value = {
    kind: "diff",
    diffSource: unsavedDiffSource(state.original.value, state.current.value),
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
  $.editorContent.value = state.current.value;
  showEditMode();
  updateGutter(state.current.value);
  $.editorContent.focus();
  $.editorEditBtn.classList.add("hidden");
  $.editorDiffBtn.classList.add("hidden");
  $.editorCancelBtn.classList.remove("hidden");
  $.editorSaveBtn.classList.remove("hidden");
}

function confirmStopEditing(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  if (state.current.value !== state.original.value) {
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
    state.current.value = state.original.value;
    return;
  }
  state.current.value = state.original.value;
  $.editorConflictOverlay.classList.add("hidden");
  if (state.returnToGitDiff !== null) {
    const { ref, repo } = state.returnToGitDiff;
    state.returnToGitDiff = null;
    state.mode.value = {
      kind: "diff",
      diffSource: gitDiffSource(ref, "", state.current.value),
    };
    void fetchGitDiffSources(state, repo, ref);
    return;
  }
  state.mode.value = { kind: "edit", editing: false };
  renderHighlight(state.original.value, state.path);
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
        state.original.value = content;
        // Don't overwrite state.current — user may have edited during save.
        // The save-button effect re-derives `disabled` from `activeDirty`
        // (dirty flips to false once original === current, unless the user
        // edited during the save), so no manual write here.
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
