// ---------------------------------------------------------------------------
// Editor: Diff-mode rendering.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { renderDiffPane } from "./diff-pane.js";
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
    // The file's own path is the language hint. Without it this pane — the
    // depth-2 view a chat's changed-file link opens — rendered unhighlighted
    // while the inline peek that sent the reader here was meant to be coloured.
    lang: state.path,
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

  // Each button owns its own diff KIND, so a fromGit diff is exited by
  // #editor-git-diff-btn (which entered it) and this one stays hidden. Offering
  // both would make "enter with B, exit with A" spellable, and the add is not
  // redundant: renderEditModeUI un-hides this button whenever the buffer is
  // dirty, so a dirty file entering a git diff arrives here with it visible.
  // Nothing here writes #editor-git-diff-btn — editor-core.ts is its one writer.
  if (src.fromGit) {
    $.editorDiffBtn.classList.add("hidden");
  } else {
    $.editorDiffBtn.classList.remove("hidden");
    $.editorDiffBtn.setAttribute("data-tooltip", "Exit diff view");
    $.editorDiffBtn.setAttribute("aria-label", "Exit diff view");
  }
  // Editing needs the file's own text, which is what `loaded` reports. A card's
  // diff carries its pair without one, so the control arrives with the buffer and
  // stays away for a file that cannot be read — where an enabled Edit would open
  // an empty box over real content and a save would write it.
  $.editorEditBtn.classList.toggle("hidden", !state.loaded);
  $.editorEditBtn.disabled = !state.loaded;
  $.editorCancelBtn.classList.add("hidden");
  $.editorSaveBtn.classList.add("hidden");
}
