// ---------------------------------------------------------------------------
// Editor: Conflict-mode rendering and AI merge suggestion handling.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { parseConflicts, resolveHunk, type ConflictFile, type ConflictHunk, type Resolution } from "./conflict.js";
import { suggestResolution } from "./actions/editor.js";
import type { FileState } from "./editor-types.js";
import { getActiveFilePath } from "./editor-types.js";
import { rebuildGutter, renderEditModeUI, showEditMode } from "./editor-ui.js";
import { registerCleanup } from "./actions/cleanup.js";

registerCleanup(() => suggestResolution.cancel());

/** Generation counter: incremented on each requestSuggestion dispatch
 *  so superseded dispatches can detect they were cancelled. */
let suggestionGen = 0;

/** Abort any in-flight suggestion request (called on tab close). */
export function abortSuggestion(): void {
  suggestResolution.cancel();
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
  const header = document.createElement("div");
  header.className = "conflict-status";
  header.textContent = `${String(conflict.hunks.length)} unresolved conflict${conflict.hunks.length === 1 ? "" : "s"}`;
  overlay.appendChild(header);
  for (let i = 0; i < conflict.hunks.length; i++) {
    const hunk = conflict.hunks[i]!;
    const row = document.createElement("div");
    row.className = "conflict-hunk-row";
    const title = document.createElement("span");
    title.className = "conflict-hunk-title";
    title.textContent = `Line ${String(hunk.startLine + 1)}: ${hunk.ourLabel || "HEAD"} vs ${hunk.theirLabel || "incoming"}`;
    row.appendChild(title);
    const suggestion = state.suggestions.get(hunk.startLine);
    if (suggestion !== undefined && suggestion.preview !== null) {
      const pill = document.createElement("span");
      pill.className = "conflict-suggest-pill";
      pill.textContent = "AI suggestion";
      row.appendChild(pill);
      row.appendChild(resolveBtn("Accept", () => acceptSuggestion(state, i)));
      row.appendChild(resolveBtn("Reject", () => rejectSuggestion(state, hunk.startLine)));
      overlay.appendChild(row);
      const preview = document.createElement("pre");
      preview.className = "conflict-suggest-preview";
      preview.textContent = suggestion.preview;
      overlay.appendChild(preview);
      continue;
    }
    row.appendChild(resolveBtn("Ours", () => applyResolution(state, i, "ours")));
    row.appendChild(resolveBtn("Theirs", () => applyResolution(state, i, "theirs")));
    row.appendChild(resolveBtn("Both", () => applyResolution(state, i, "both")));
    const suggestBtn = document.createElement("button");
    suggestBtn.className = "conflict-btn conflict-btn-suggest";
    suggestBtn.textContent = suggestion?.loading === true ? "Suggesting..." : "Suggest";
    suggestBtn.title = "Propose a merged version using the utility AI bridge";
    suggestBtn.disabled = suggestion?.loading === true;
    suggestBtn.addEventListener("click", () => { void requestSuggestion(state, i); });
    row.appendChild(suggestBtn);
    if (suggestion !== undefined && suggestion.error !== "") {
      const err = document.createElement("span");
      err.className = "conflict-suggest-error";
      err.textContent = suggestion.error;
      row.appendChild(err);
    }
    overlay.appendChild(row);
  }
}

function resolveBtn(label: string, onClick: () => void): HTMLButtonElement {
  const b = document.createElement("button");
  b.className = "conflict-btn";
  b.textContent = label;
  b.addEventListener("click", onClick);
  return b;
}

function applyResolution(state: FileState, hunkIndex: number, resolution: Resolution): void {
  if (state.mode.kind !== "conflict") return;
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
  if (state.mode.kind !== "conflict") return;
  const hunk = state.mode.conflict.hunks[hunkIndex];
  if (hunk === undefined) return;
  const existing = state.suggestions.get(hunk.startLine);
  if (existing?.loading === true || existing?.preview !== undefined && existing.preview !== null) return;
  suggestResolution.cancel();
  const myDispatchId = ++suggestionGen;
  state.suggestions.set(hunk.startLine, { loading: true, preview: null, error: "" });
  renderConflictOverlay(state);
  const context = buildHunkContext(state.mode.conflict, hunk);
  const body = {
    ours: hunk.oursLines.join("\n"),
    theirs: hunk.theirsLines.join("\n"),
    context,
  };
  const resp = await suggestResolution.dispatch(body);
  if (myDispatchId !== suggestionGen) return; // superseded by another dispatch; silently bail
  if (state.mode.kind !== "conflict") return;
  if (getActiveFilePath() !== state.path) return; // stale file switch
  const current = state.mode.conflict.hunks[hunkIndex];
  if (current?.startLine !== hunk.startLine) return;
  if (resp === null || typeof resp.output !== "string") {
    state.suggestions.set(hunk.startLine, { loading: false, preview: null, error: resp?.error ?? "generation failed" });
    renderConflictOverlay(state);
    return;
  }
  state.suggestions.set(hunk.startLine, { loading: false, preview: resp.output, error: "" });
  renderConflictOverlay(state);
}

function acceptSuggestion(state: FileState, hunkIndex: number): void {
  if (state.mode.kind !== "conflict") return;
  const hunk = state.mode.conflict.hunks[hunkIndex];
  if (hunk === undefined) return;
  const suggestion = state.suggestions.get(hunk.startLine);
  if (suggestion === undefined || suggestion.preview === null) return;
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
  const after = file.lines.slice(hunk.endLine + 1, Math.min(file.lines.length, hunk.endLine + 1 + ctxLines));
  if (before.length === 0 && after.length === 0) return "";
  return [...before, "/* ...conflict hunk... */", ...after].join("\n");
}
