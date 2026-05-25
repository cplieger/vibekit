// ---------------------------------------------------------------------------
// Editor UI: rendering helpers (read/edit mode, gutter, highlight, restoreUI).
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { highlight } from "./highlight.js";
import { getActiveId } from "./store.js";
import { fetchAgentLinesAction } from "./actions/editor.js";
import { scrollToEditorLine, flashEditorLine } from "./editor-scroll.js";
import type { FileState } from "./editor-types.js";
import {
  getActiveFilePath, isPlanDraftPath, fileStates,
} from "./editor-types.js";

// --- Pending line jump state (shared with openers) ---

export const pendingLines = new Map<string, number>();

// --- Agent line tracking ---

interface LineRange { start_line: number; end_line: number }
const agentLineCache = new Map<string, LineRange[]>();
const agentLineSetCache = new Map<string, Set<number>>();

function getAgentLines(path: string): Set<number> {
  const cached = agentLineSetCache.get(path);
  if (cached !== undefined) return cached;
  const ranges = agentLineCache.get(path);
  if (ranges === undefined) return new Set();
  const lines = new Set<number>();
  for (const r of ranges) {
    for (let i = r.start_line; i <= r.end_line; i++) lines.add(i);
  }
  agentLineSetCache.set(path, lines);
  return lines;
}

export async function fetchAgentLines(path: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") return;
  fetchAgentLinesAction.cancel();
  const data = await fetchAgentLinesAction.dispatch({ chatID, path });
  if (data === null) return;
  if (getActiveFilePath() !== path) return;
  agentLineCache.set(path, data.changes ?? []);
  agentLineSetCache.delete(path);
  // Rebuild gutter to reflect newly-fetched agent lines if file is displayed.
  const state = fileStates.get(path);
  if (state !== undefined && state.loaded) {
    rebuildGutter(state.current);
  }
}

/** Clear cached agent-line data for a path. Called when a file is
 *  closed so the per-file Maps don't grow unbounded over a session. */
export function clearAgentLineCache(path: string): void {
  agentLineCache.delete(path);
  agentLineSetCache.delete(path);
}

// --- Show/hide mode helpers ---

export function showReadMode(): void {
  $.editorHighlight.classList.remove("hidden");
  $.editorContent.classList.add("hidden");
  $.editorDiffPane.classList.add("hidden");
}

export function showEditMode(): void {
  $.editorHighlight.classList.add("hidden");
  $.editorContent.classList.remove("hidden");
  $.editorDiffPane.classList.add("hidden");
}

export function showDiffMode(): void {
  $.editorHighlight.classList.add("hidden");
  $.editorContent.classList.add("hidden");
  $.editorDiffPane.classList.remove("hidden");
}

// --- Gutter ---

export function updateGutter(content: string): void {
  const lineCount = content.split("\n").length;
  const gutter = $.editorGutter;
  const currentCount = gutter.children.length;
  const agentLines = getAgentLines(getActiveFilePath());

  if (currentCount > lineCount) {
    for (let i = currentCount; i > lineCount; i--) {
      gutter.lastChild?.remove();
    }
  } else if (currentCount < lineCount) {
    for (let i = currentCount + 1; i <= lineCount; i++) {
      const line = document.createElement("div");
      line.className = "gutter-line";
      line.textContent = String(i);
      if (agentLines.has(i)) line.classList.add("gutter-agent-modified");
      gutter.appendChild(line);
    }
  }
}

export function rebuildGutter(content: string): void {
  const lineCount = content.split("\n").length;
  const gutter = $.editorGutter;
  gutter.replaceChildren();
  const agentLines = getAgentLines(getActiveFilePath());
  for (let i = 1; i <= lineCount; i++) {
    const line = document.createElement("div");
    line.className = "gutter-line";
    line.textContent = String(i);
    if (agentLines.has(i)) line.classList.add("gutter-agent-modified");
    gutter.appendChild(line);
  }
}

// --- Highlight rendering ---

export function renderHighlight(content: string, path: string): void {
  $.editorCode.innerHTML = highlight(content, path);
  rebuildGutter(content);
}

// --- Edit-mode UI ---

export function renderEditModeUI(state: FileState): void {
  $.editorEditBtn.disabled = false;
  const isPlanDraft = isPlanDraftPath(state.path);
  const isModified = state.current !== state.original;
  $.editorDiffBtn.classList.toggle("hidden", isPlanDraft || !isModified);
  $.editorDiffBtn.setAttribute("data-tooltip", "View diff vs saved");
  $.editorDiffBtn.setAttribute("aria-label", "View diff vs saved");
  const editing = state.mode.kind === "edit" && state.mode.editing;
  if (editing) {
    $.editorContent.value = state.current;
    showEditMode();
    rebuildGutter(state.current);
    $.editorEditBtn.classList.add("hidden");
    $.editorCancelBtn.classList.remove("hidden");
    $.editorSaveBtn.classList.remove("hidden");
    $.editorSaveBtn.disabled = state.current === state.original;
  } else {
    renderHighlight(state.current, state.path);
    showReadMode();
    $.editorEditBtn.classList.remove("hidden");
    $.editorCancelBtn.classList.add("hidden");
    $.editorSaveBtn.classList.add("hidden");
  }
}

// --- Line jump ---

export function applyPendingLine(path: string): void {
  const line = pendingLines.get(path);
  if (line === undefined || line <= 0) return;
  pendingLines.delete(path);
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      scrollToEditorLine(line);
      flashEditorLine(line);
    });
  });
}
