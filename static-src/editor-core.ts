// ---------------------------------------------------------------------------
// Editor core: init, mode switches, plan handoff.
// Types, state, and pure predicates live in editor-types.ts.
// Openers live in editor-openers.ts; UI helpers in editor-ui.ts.
//
// Extracted modules:
//   editor-types.ts    — shared types, state container, predicates
//   editor-modes.ts    — restoreUI dispatcher
//   editor-conflict.ts — conflict-mode rendering and AI merge suggestions
//   editor-diff.ts     — diff source helpers
//   editor-ui.ts       — rendering helpers (gutter, highlight, mode UI)
//   editor-openers.ts  — file open, load, and fetch logic
// ---------------------------------------------------------------------------

import { effect } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { confirm as confirmDialog } from "./confirm.js";
import { parseConflicts } from "./conflict.js";
import { saveFile as saveFileAction } from "./actions/editor.js";
import { isPending, registerCleanup } from "./actions/index.js";
import { renderConflictOverlay } from "./editor-conflict.js";
import { showEditMode, updateGutter, renderReadSurface, renderEditModeUI } from "./editor-ui.js";
import { restoreUI } from "./editor-modes.js";
import { fetchGitDiffSources, openFileGitDiff } from "./editor-openers.js";
import {
  fileStates,
  getActiveFilePath,
  activeDirty,
  unsavedDiffSource,
  gitDiffSource,
} from "./editor-types.js";
import type { FileState } from "./editor-types.js";
import { markGitDirty } from "./git.js";
import { relToWorkspace } from "./workspace.js";
import { iconEl } from "./icon-el.js";
import { ICON_GIT_COMMIT } from "./icons.js";
import { isViewableImage } from "./file-extensions.js";
import { onGitStatusChange, statusForPath } from "./git-status-store.js";

// --- Re-exports for backward compatibility ---
// Consumers that import from editor-core.ts continue to work.

export { routeForPath } from "./editor-types.js";

export function initEditor(): void {
  $.editorEditBtn.addEventListener("click", startEditing);
  $.editorCancelBtn.addEventListener("click", confirmStopEditing);
  $.editorSaveBtn.addEventListener("click", saveFile);
  $.editorDiffBtn.addEventListener("click", toggleDiffMode);
  // No pending accept/reject/partial/discuss buttons, and no bus listeners
  // closing pending tabs: the whole staged-write review surface is gone. KAS
  // reviews a turn at once, so there is no per-file verdict to give from the
  // editor and no `pending:` tab to close when one lands.
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

  // The glyph is injected rather than drawn in static/index.html, because a
  // concept with an icons.ts entry may not be redrawn there. One drawing, so
  // drift is unrepresentable and menu-icons.test.ts needs no pair for it.
  $.editorGitDiffBtn.replaceChildren(iconEl(ICON_GIT_COMMIT));
  $.editorGitDiffBtn.addEventListener("click", toggleGitDiffMode);

  // Sole owner of the git-diff button's visibility and its toggle attributes.
  // TWO triggers because it has two kinds of input: this effect for the active
  // path, that file's error and its mode (all three signals), and the imperative
  // git-status subscription armed below for a letter arriving after the file is
  // open.
  // `statusForPath` reads a plain Map, so it is deliberately NOT tracked here —
  // which is exactly why the second trigger has to exist.
  effect(() => {
    // Arming HERE rather than from `activateFile` is the one departure from the
    // brief, and it is forced by direction: the writer belongs in this module
    // (one writer) and `editor-openers.ts` importing it back would close a
    // cycle. The trigger is the same — `activateFile` is what sets the active
    // path — so the store's first-subscriber walk of every worktree is still
    // paid on the first file activation and never at boot.
    if (getActiveFilePath() !== "") {
      armGitStatusWatch();
    }
    paintGitDiffBtn();
  });
}

/** Whether the git-status store is being watched for this surface yet. */
let gitStatusWatched = false;

function armGitStatusWatch(): void {
  if (gitStatusWatched) {
    return;
  }
  gitStatusWatched = true;
  registerCleanup(onGitStatusChange(paintGitDiffBtn));
}

/** Paint #editor-git-diff-btn. The ONE writer — see the effect above.
 *
 *  Shown when the active file is a text file with no error, being READ or in a
 *  git diff, and differing from the ref. The last clause carries an OR: once the
 *  reader is looking at the diff the control must not vanish if the file gets
 *  committed underneath them, because it is also the way out.
 *
 *  Two clauses that are deliberately NOT here. `state.loaded` is absent because
 *  this control fetches both of its own sides (`openFileGitDiff` routes into
 *  `fetchGitDiffSources`), so it is complete before the buffer arrives and
 *  withholding it during the read would withdraw a working affordance; the
 *  window is bounded by one `/api/file` round trip and a failed one lands on the
 *  error clause above. And `statusForPath` is read here rather than tracked
 *  because it is a plain Map — which is exactly why the second, imperative
 *  trigger exists. */
function paintGitDiffBtn(): void {
  const btn = $.editorGitDiffBtn;
  const path = getActiveFilePath();
  const state = path === "" ? undefined : fileStates.get(path);
  const m = state?.mode.value;
  const inGitDiff = m?.kind === "diff" && m.diffSource.fromGit;
  // `state.error !== ""` is what hides it for a BINARY file, and that is
  // verified rather than assumed: /api/file answers 415 for one, apiGet
  // collapses every non-2xx to null, and `loadFile`'s null branch sets the
  // error. So a git-dirty .zip reaches the error state and needs no clause of
  // its own here. An image never loads at all, hence the extension test.
  const show =
    // `state?.error.value === ""` carries the existence check too: an absent
    // state yields undefined, which is not "".
    state?.error.value === "" &&
    !isViewableImage(path) &&
    // `!m.editing` puts this control in the set `startEditing` withdraws. Edit
    // mode is left through Cancel, which confirms a discard, or through Save; a
    // third sideways exit replaces the textarea with a two-pane diff on one
    // click, which reads as losing the edit even though the buffer survives
    // (`open` captures it into `current` and `startEditing` restores it).
    ((m?.kind === "edit" && !m.editing) || inGitDiff) &&
    (statusForPath(path) !== "" || inGitDiff);
  btn.classList.toggle("hidden", !show);
  btn.setAttribute("aria-pressed", inGitDiff ? "true" : "false");
  // The accessible NAME is stable across both states and the state travels on
  // aria-pressed alone; the tooltip is the one surface with no state channel
  // beside it, so it carries state plus action. Written unconditionally so this
  // effect owns the attribute and nothing else can flip it.
  btn.setAttribute("aria-label", "View diff vs HEAD");
  btn.setAttribute(
    "data-tooltip",
    inGitDiff ? "Diff vs HEAD. Exit diff view" : "View diff vs HEAD",
  );
}

/** Enter or leave the diff against HEAD. A different question from
 *  `toggleDiffMode`'s buffer-vs-saved, so it is a different control — but the
 *  EXIT is the same path, so the two cannot diverge. */
function toggleGitDiffMode(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  const m = state.mode.value;
  if (m.kind === "diff" && m.diffSource.fromGit) {
    state.mode.value = { kind: "edit", editing: false };
    renderEditModeUI(state);
    return;
  }
  openFileGitDiff(state.path, "HEAD");
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
  // `current` was just reset to `original` above, so the read surface paints the
  // saved text — as markdown for a document, as source otherwise.
  renderReadSurface(state);
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
  const args: Parameters<typeof saveFileAction.dispatch>[0] = { path: state.path, content };
  if (state.loadedHash !== "") {
    args.expectedHash = state.loadedHash;
  }
  void saveFileAction.dispatch(args, {
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
          // A refused stale write carries the file's current bytes. Show the
          // difference rather than the words: the user's own text stays in the
          // buffer (it is the only copy), `original` becomes what is on disk, so
          // the existing unsaved-diff view answers "what changed under me" and
          // the next save carries the new hash.
          if (d.content !== undefined) {
            state.original.value = d.content;
            state.loadedHash = d.content_hash ?? "";
            state.mode.value = {
              kind: "diff",
              diffSource: unsavedDiffSource(d.content, content),
            };
            renderEditModeUI(state);
          }
        }
        return;
      }
      state.original.value = content;
      // Don't overwrite state.current — user may have edited during save.
      // The save-button effect re-derives `disabled` from `activeDirty`
      // (dirty flips to false once original === current, unless the user
      // edited during the save), so no manual write here.
      $.editorError.classList.add("hidden");
      // A save is a write to the worktree, so the badge, the file-browser
      // decorations and the docs page are stale from here. The agent's own writes
      // announce themselves through `tool_call_update`; the user's own editor is
      // the second writer and had no announcement at all, which left every one of
      // those surfaces stale indefinitely. Scoped to the one file, so it costs the
      // owning repository's two git subprocesses.
      markGitDirty([relToWorkspace(state.path)]);
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
  });
}
