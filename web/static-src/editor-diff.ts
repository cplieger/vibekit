// ---------------------------------------------------------------------------
// Editor: Diff-mode rendering.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { renderDiffPane } from "./diff-pane.js";
import { getActiveId } from "./store.js";
import type { FileState } from "./editor-types.js";
import { isPendingPath, getCachedDiff } from "./editor-types.js";
import { renderEditModeUI, showDiffMode } from "./editor-ui.js";

export function renderDiffModeUI(state: FileState): void {
  if (state.mode.kind !== "diff") {
    state.mode = { kind: "edit", editing: false };
    renderEditModeUI(state);
    return;
  }
  const src = state.mode.diffSource;
  $.editorDiffPane.replaceChildren();
  const diff = getCachedDiff(state);
  const pending = isPendingPath(state.path);
  const paneOpts: Parameters<typeof renderDiffPane>[1] = {
    oldLabel: src.oldLabel, newLabel: src.newLabel, lineNumbers: true, syncScroll: true,
    onAskAbout: (hunkText: string) => {
      if (pending) {
        void import("./editor-pending.js").then((m) => m.openDiscussPrompt(state.path, hunkText));
        return;
      }
      const chatID = getActiveId();
      if (chatID === "") return;
      const prompt = `Explain this diff:\n\n\`\`\`diff\n${hunkText}\n\`\`\``;
      void import("./chat-commands.js").then(({ sendPromptTo }) => {
        void sendPromptTo(chatID, prompt);
      });
    },
  };
  if (pending) {
    paneOpts.onAcceptHunk = (hunkIndex: number) => {
      state.pendingHunkDecisions.set(hunkIndex, "accept");
      void import("./editor-pending.js").then((m) => m.refreshPendingToolbar(state));
    };
    paneOpts.onRejectHunk = (hunkIndex: number) => {
      state.pendingHunkDecisions.set(hunkIndex, "reject");
      void import("./editor-pending.js").then((m) => m.refreshPendingToolbar(state));
    };
  }
  const pane = renderDiffPane(diff, paneOpts);
  $.editorDiffPane.appendChild(pane);
  // showDiffMode inline
  showDiffMode();

  if (!isPendingPath(state.path)) {
    $.editorDiffBtn.classList.remove("hidden");
    $.editorDiffBtn.setAttribute("data-tooltip", "Exit diff view");
    $.editorDiffBtn.setAttribute("aria-label", "Exit diff view");
    $.editorEditBtn.classList.remove("hidden");
    $.editorEditBtn.disabled = false;
  }
  $.editorCancelBtn.classList.add("hidden");
  $.editorSaveBtn.classList.add("hidden");
  if (pending) void import("./editor-pending.js").then((m) => m.refreshPendingToolbar(state));
}
