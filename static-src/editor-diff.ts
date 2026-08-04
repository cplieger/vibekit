// ---------------------------------------------------------------------------
// Editor: Diff-mode rendering.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { renderDiffPane } from "./diff-pane.js";
import { getActiveId } from "./store.js";
import type { FileState } from "./editor-types.js";
import { getCachedDiff } from "./editor-types.js";
import { renderEditModeUI, showDiffMode } from "./editor-ui.js";

export function renderDiffModeUI(state: FileState): void {
  const m = state.mode.value;
  if (m.kind !== "diff") {
    state.mode.value = { kind: "edit", editing: false };
    renderEditModeUI(state);
    return;
  }
  const src = m.diffSource;
  $.editorDiffPane.replaceChildren();
  const diff = getCachedDiff(state);
  const paneOpts: Parameters<typeof renderDiffPane>[1] = {
    oldLabel: src.oldLabel,
    newLabel: src.newLabel,
    lineNumbers: true,
    syncScroll: true,
    onAskAbout: (hunkText: string) => {
      const chatID = getActiveId();
      if (chatID === "") {
        return;
      }
      const prompt = `Explain this diff:\n\n\`\`\`diff\n${hunkText}\n\`\`\``;
      void import("./chat-commands.js")
        .then(({ sendPromptTo }) => {
          void sendPromptTo(chatID, prompt);
        })
        .catch(() => {
          /* noop */
        });
    },
  };
  // The "Ignore whitespace" toggle: diff.ts supports a whitespace-insensitive
  // compare and diff-pane re-diffs + re-renders in place from these source texts.
  // It used to be suppressed for a supervised diff, whose per-hunk accept/reject
  // indices had to line up with the un-normalized diff a partial merge walked —
  // there are no per-hunk decisions now, so it applies everywhere.
  paneOpts.source = { oldText: src.oldContent, newText: src.newContent };
  // No per-hunk accept/reject. KAS's decision wire is PER FILE, and the IDE ships
  // only `supervisedDiff.discussHunk` beside it — there is no per-hunk verdict to
  // send. The replacement is ordinary editing: approve the turn, then edit what
  // you partly disagree with.
  const pane = renderDiffPane(diff, paneOpts);
  $.editorDiffPane.appendChild(pane);
  // showDiffMode inline
  showDiffMode();

  $.editorDiffBtn.classList.remove("hidden");
  $.editorDiffBtn.setAttribute("data-tooltip", "Exit diff view");
  $.editorDiffBtn.setAttribute("aria-label", "Exit diff view");
  $.editorEditBtn.classList.remove("hidden");
  $.editorEditBtn.disabled = false;
  $.editorCancelBtn.classList.add("hidden");
  $.editorSaveBtn.classList.add("hidden");
}
