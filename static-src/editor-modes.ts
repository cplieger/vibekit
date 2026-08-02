// ---------------------------------------------------------------------------
// Editor modes: restoreUI dispatcher extracted to break circular imports
// between editor-conflict.ts, editor-ui.ts, and editor-diff.ts.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { renderDiffModeUI } from "./editor-diff.js";
import { renderConflictModeUI } from "./editor-conflict.js";
import { renderEditModeUI, showReadMode } from "./editor-ui.js";
import type { FileState } from "./editor-types.js";
import { isPendingPath } from "./editor-types.js";

// --- restoreUI (dispatches to mode-specific renderers) ---

export function restoreUI(state: FileState): void {
  const pending = isPendingPath(state.path);
  $.editorPendingAcceptBtn.classList.toggle("hidden", !pending);
  $.editorPendingRejectBtn.classList.toggle("hidden", !pending);
  $.editorPendingDiscussBtn.classList.toggle("hidden", !pending);
  $.editorPendingApplyPartialBtn.classList.add("hidden");

  if (state.error !== "") {
    $.editorCode.textContent = "";
    $.editorGutter.textContent = "";
    $.editorGutter.classList.add("hidden");
    $.editorError.textContent = state.error;
    $.editorError.classList.remove("hidden");
    $.editorEditBtn.disabled = true;
    $.editorEditBtn.classList.toggle("hidden", pending);
    $.editorCancelBtn.classList.add("hidden");
    $.editorSaveBtn.classList.add("hidden");
    $.editorDiffBtn.classList.add("hidden");
    // A resolved/expired pending change still routes here with an error
    // ("Change already resolved"). Hide the Accept/Reject/Discuss actions the
    // top of restoreUI un-hid for pending paths, so the user can't dispatch a
    // resolve for a tool_call_id the server has already dropped.
    $.editorPendingAcceptBtn.classList.add("hidden");
    $.editorPendingRejectBtn.classList.add("hidden");
    $.editorPendingDiscussBtn.classList.add("hidden");
    $.editorPendingApplyPartialBtn.classList.add("hidden");
    showReadMode();
    return;
  }
  $.editorGutter.classList.remove("hidden");
  $.editorError.classList.add("hidden");

  if (pending) {
    $.editorEditBtn.classList.add("hidden");
    $.editorCancelBtn.classList.add("hidden");
    $.editorSaveBtn.classList.add("hidden");
    $.editorDiffBtn.classList.add("hidden");
    renderDiffModeUI(state);
    return;
  }

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
