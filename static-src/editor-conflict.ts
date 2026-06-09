// ---------------------------------------------------------------------------
// Editor: Conflict-mode rendering and AI merge suggestion handling.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { el } from "@cplieger/reactive";
import {
  parseConflicts,
  resolveHunk,
  type ConflictFile,
  type ConflictHunk,
  type Resolution,
} from "./conflict.js";
import { suggestResolution } from "./actions/editor.js";
import type { FileState } from "./editor-types.js";
import { getActiveFilePath, fileStates } from "./editor-types.js";
import { rebuildGutter, renderEditModeUI, showEditMode } from "./editor-ui.js";
import { registerCleanup } from "./actions/index.js";

registerCleanup(() => {
  suggestResolution.cancel();
});

/** Per-file generation counter so superseded dispatches can detect
 *  they were cancelled WITHOUT invalidating other files' in-flight
 *  suggestions. Closing file A's tab bumps only A's counter; file B's
 *  in-flight suggestion uses B's counter and resolves correctly. */
const suggestionGenByPath = new Map<string, number>();

function bumpSuggestionGen(path: string): number {
  const next = (suggestionGenByPath.get(path) ?? 0) + 1;
  suggestionGenByPath.set(path, next);
  return next;
}

function currentSuggestionGen(path: string): number {
  return suggestionGenByPath.get(path) ?? 0;
}

/** Clean up per-file generation tracking when a file is closed. Called
 *  from closeEditorFile so the suggestionGenByPath Map doesn't grow
 *  unbounded over a long session with many files opened/closed. */
export function clearSuggestionState(path: string): void {
  suggestionGenByPath.delete(path);
}

/** Abort any in-flight suggestion request. If path is provided, reset
 *  loading state on THAT file; otherwise default to the active file.
 *  Called on tab close (with path) and on broader teardown (without).
 *
 *  Per-file generation: bumps only the target path's counter so other
 *  files' in-flight suggestions remain valid. */
export function abortSuggestion(path?: string): void {
  // Reset any entries with loading: true so the UI doesn't show stale spinners.
  const targetPath = path ?? getActiveFilePath();
  const state = fileStates.get(targetPath);
  if (state !== undefined) {
    for (const [key, entry] of state.suggestions) {
      if (entry.loading) {
        state.suggestions.set(key, { loading: false, preview: null, error: "cancelled" });
      }
    }
  }
  // Bump only this path's counter so this file's in-flight requests
  // discard their results on resolution.
  bumpSuggestionGen(targetPath);
}

export function renderConflictModeUI(state: FileState): void {
  if (state.mode.kind !== "conflict") {
    state.mode = { kind: "conflict", conflict: parseConflicts(state.current), editing: true };
  }
  $.editorContent.value = state.current;
  showEditMode();
  rebuildGutter(state.current);
  $.editorEditBtn.classList.add("hidden");
  $.editorCancelBtn.classList.remove("hidden");
  $.editorSaveBtn.classList.remove("hidden");
  $.editorSaveBtn.disabled = state.current === state.original;
  $.editorDiffBtn.classList.add("hidden");
  renderConflictOverlay(state);
}

export function renderConflictOverlay(state: FileState): void {
  const overlay = $.editorConflictOverlay;
  overlay.replaceChildren();
  if (state.mode.kind !== "conflict" || state.mode.conflict.hunks.length === 0) {
    overlay.classList.add("hidden");
    return;
  }
  const conflict = state.mode.conflict;
  overlay.classList.remove("hidden");
  overlay.appendChild(
    el(
      "div",
      { className: "conflict-status" },
      `${String(conflict.hunks.length)} unresolved conflict${conflict.hunks.length === 1 ? "" : "s"}`,
    ),
  );
  for (let i = 0; i < conflict.hunks.length; i++) {
    const hunk = conflict.hunks[i]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const row = el("div", { className: "conflict-hunk-row" });
    row.appendChild(
      el(
        "span",
        { className: "conflict-hunk-title" },
        `Line ${String(hunk.startLine + 1)}: ${hunk.ourLabel || "HEAD"} vs ${hunk.theirLabel || "incoming"}`,
      ),
    );
    const suggestion = state.suggestions.get(hunk.startLine);
    if (suggestion !== undefined && suggestion.preview !== null) {
      row.appendChild(el("span", { className: "conflict-suggest-pill" }, "AI suggestion"));
      row.appendChild(
        resolveBtn("Accept", () => {
          acceptSuggestion(state, i);
        }),
      );
      row.appendChild(
        resolveBtn("Reject", () => {
          rejectSuggestion(state, hunk.startLine);
        }),
      );
      overlay.appendChild(row);
      overlay.appendChild(el("pre", { className: "conflict-suggest-preview" }, suggestion.preview));
      continue;
    }
    row.appendChild(
      resolveBtn("Ours", () => {
        applyResolution(state, i, "ours");
      }),
    );
    row.appendChild(
      resolveBtn("Theirs", () => {
        applyResolution(state, i, "theirs");
      }),
    );
    row.appendChild(
      resolveBtn("Both", () => {
        applyResolution(state, i, "both");
      }),
    );
    const suggestBtn = el(
      "button",
      {
        className: "conflict-btn conflict-btn-suggest",
        title: "Propose a merged version using the utility AI bridge",
        disabled: suggestion?.loading === true,
      },
      suggestion?.loading === true ? "Suggesting..." : "Suggest",
    );
    suggestBtn.addEventListener("click", () => {
      void requestSuggestion(state, i);
    });
    row.appendChild(suggestBtn);
    if (suggestion !== undefined && suggestion.error !== "") {
      row.appendChild(el("span", { className: "conflict-suggest-error" }, suggestion.error));
    }
    overlay.appendChild(row);
  }
}

function resolveBtn(label: string, onClick: () => void): HTMLElement {
  const b = el("button", { className: "conflict-btn" }, label);
  b.addEventListener("click", onClick);
  return b;
}

function applyResolution(state: FileState, hunkIndex: number, resolution: Resolution): void {
  if (state.mode.kind !== "conflict") {
    return;
  }
  const newContent = resolveHunk(state.mode.conflict, hunkIndex, resolution);
  state.current = newContent;
  const parsed = parseConflicts(newContent);
  state.suggestions.clear();
  $.editorContent.value = newContent;
  $.editorSaveBtn.disabled = state.current === state.original;
  rebuildGutter(newContent);
  if (parsed.hunks.length === 0) {
    state.mode = { kind: "edit", editing: false };
    renderEditModeUI(state);
    return;
  }
  state.mode = { kind: "conflict", conflict: parsed, editing: true };
  renderConflictOverlay(state);
}

async function requestSuggestion(state: FileState, hunkIndex: number): Promise<void> {
  if (state.mode.kind !== "conflict") {
    return;
  }
  const hunk = state.mode.conflict.hunks[hunkIndex];
  if (hunk === undefined) {
    return;
  }
  const existing = state.suggestions.get(hunk.startLine);
  if (
    existing?.loading === true ||
    (existing?.preview !== undefined && existing.preview !== null)
  ) {
    return;
  }
  // Per-file generation: bump only THIS file's counter. Other files'
  // in-flight suggestions remain valid.
  // Note: we no longer call suggestResolution.cancel() globally — that
  // would abort in-flight suggestions for OTHER files. The per-file
  // gen check below discards stale results from this file only.
  const myDispatchId = bumpSuggestionGen(state.path);
  state.suggestions.set(hunk.startLine, { loading: true, preview: null, error: "" });
  renderConflictOverlay(state);
  const context = buildHunkContext(state.mode.conflict, hunk);
  const body = {
    ours: hunk.oursLines.join("\n"),
    theirs: hunk.theirsLines.join("\n"),
    context,
  };
  const resp = await suggestResolution.dispatch(body);
  // Stale-dispatch guard: if abortSuggestion(state.path) or another
  // requestSuggestion on this file ran while we were awaiting, the
  // path's gen has incremented. Bail silently.
  if (myDispatchId !== currentSuggestionGen(state.path)) {
    return;
  }
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check
  if (state.mode.kind !== "conflict") {
    return;
  }
  if (getActiveFilePath() !== state.path) {
    return;
  } // stale file switch
  const current = state.mode.conflict.hunks[hunkIndex];
  if (current?.startLine !== hunk.startLine) {
    return;
  }
  if (resp === null || typeof resp.output !== "string") {
    state.suggestions.set(hunk.startLine, {
      loading: false,
      preview: null,
      error: resp?.error ?? "generation failed",
    });
    renderConflictOverlay(state);
    return;
  }
  state.suggestions.set(hunk.startLine, { loading: false, preview: resp.output, error: "" });
  renderConflictOverlay(state);
}

function acceptSuggestion(state: FileState, hunkIndex: number): void {
  if (state.mode.kind !== "conflict") {
    return;
  }
  const hunk = state.mode.conflict.hunks[hunkIndex];
  if (hunk === undefined) {
    return;
  }
  const suggestion = state.suggestions.get(hunk.startLine);
  if (suggestion?.preview == null) {
    return;
  }
  const previewLines = suggestion.preview === "" ? [] : suggestion.preview.split("\n");
  const out = [
    ...state.mode.conflict.lines.slice(0, hunk.startLine),
    ...previewLines,
    ...state.mode.conflict.lines.slice(hunk.endLine + 1),
  ];
  const newContent = out.join("\n") + (state.mode.conflict.trailingNewline ? "\n" : "");
  state.current = newContent;
  const parsed = parseConflicts(newContent);
  state.suggestions.clear();
  $.editorContent.value = newContent;
  $.editorSaveBtn.disabled = state.current === state.original;
  rebuildGutter(newContent);
  if (parsed.hunks.length === 0) {
    state.mode = { kind: "edit", editing: false };
    renderEditModeUI(state);
    return;
  }
  state.mode = { kind: "conflict", conflict: parsed, editing: true };
  renderConflictOverlay(state);
}

function rejectSuggestion(state: FileState, startLine: number): void {
  state.suggestions.delete(startLine);
  renderConflictOverlay(state);
}

function buildHunkContext(file: ConflictFile, hunk: ConflictHunk): string {
  const ctxLines = 10;
  const before = file.lines.slice(Math.max(0, hunk.startLine - ctxLines), hunk.startLine);
  const after = file.lines.slice(
    hunk.endLine + 1,
    Math.min(file.lines.length, hunk.endLine + 1 + ctxLines),
  );
  if (before.length === 0 && after.length === 0) {
    return "";
  }
  return [...before, "/* ...conflict hunk... */", ...after].join("\n");
}
