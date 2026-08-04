// ---------------------------------------------------------------------------
// Editor modes: restoreUI dispatcher extracted to break circular imports
// between editor-conflict.ts, editor-ui.ts, and editor-diff.ts.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { renderDiffModeUI } from "./editor-diff.js";
import { renderConflictModeUI } from "./editor-conflict.js";
import { renderEditModeUI, showReadMode } from "./editor-ui.js";
import type { FileState } from "./editor-types.js";

// --- restoreUI (dispatches to mode-specific renderers) ---

export function restoreUI(state: FileState): void {
  if (state.error !== "") {
    $.editorCode.textContent = "";
    $.editorGutter.textContent = "";
    $.editorGutter.classList.add("hidden");
    $.editorError.textContent = state.error;
    $.editorError.classList.remove("hidden");
    $.editorEditBtn.disabled = true;
    $.editorEditBtn.classList.add("hidden");
    $.editorCancelBtn.classList.add("hidden");
    $.editorSaveBtn.classList.add("hidden");
    $.editorDiffBtn.classList.add("hidden");
    showReadMode();
    return;
  }
  $.editorGutter.classList.remove("hidden");
  $.editorError.classList.add("hidden");

  switch (state.mode.value.kind) {
    case "diff":
      renderDiffModeUI(state);
      return;
    case "conflict":
      renderConflictModeUI(state);
      return;
    case "edit":
      renderEditModeUI(state);
      return;
  }
}
