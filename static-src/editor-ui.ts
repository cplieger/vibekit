// ---------------------------------------------------------------------------
// Editor UI: rendering helpers (read/edit mode, gutter, highlight, restoreUI).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { reconcile } from "./reconcile.js";
import { $ } from "./dom.js";
import { highlight } from "./highlight.js";
import { getActiveId } from "./store.js";
import { fetchAgentLines as fetchAgentLinesAction } from "./actions/editor.js";
import { scrollToEditorLine, flashEditorLine } from "./editor-scroll.js";
import { renderMarkdownDoc } from "./editor-markdown.js";
import type { FileState } from "./editor-types.js";
import { getActiveFilePath, fileStates, rendersMarkdown } from "./editor-types.js";

// --- Pending line jump state (shared with openers) ---

export const pendingLines = new Map<string, number>();

// --- Agent line tracking ---

interface LineRange {
  start_line: number;
  end_line: number;
}
const agentLineCache = new Map<string, LineRange[]>();
const agentLineSetCache = new Map<string, Set<number>>();

function getAgentLines(path: string): Set<number> {
  const cached = agentLineSetCache.get(path);
  if (cached !== undefined) {
    return cached;
  }
  const ranges = agentLineCache.get(path);
  if (ranges === undefined) {
    return new Set();
  }
  const lines = new Set<number>();
  for (const r of ranges) {
    for (let i = r.start_line; i <= r.end_line; i++) {
      lines.add(i);
    }
  }
  agentLineSetCache.set(path, lines);
  return lines;
}

export async function fetchAgentLines(path: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  fetchAgentLinesAction.cancel();
  const data = await fetchAgentLinesAction.dispatch({ chatID, path });
  if (data === null) {
    return;
  }
  if (getActiveFilePath() !== path) {
    return;
  }
  agentLineCache.set(path, data.changes);
  agentLineSetCache.delete(path);
  // Refresh gutter to reflect newly-fetched agent lines if file is displayed.
  const state = fileStates.get(path);
  if (state?.loaded) {
    updateGutter(state.current.value);
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
  $.editorMarkdown.classList.add("hidden");
  $.editorGutter.classList.remove("hidden");
}

export function showEditMode(): void {
  $.editorHighlight.classList.add("hidden");
  $.editorContent.classList.remove("hidden");
  $.editorDiffPane.classList.add("hidden");
  $.editorMarkdown.classList.add("hidden");
  $.editorGutter.classList.remove("hidden");
}

export function showDiffMode(): void {
  $.editorHighlight.classList.add("hidden");
  $.editorContent.classList.add("hidden");
  $.editorDiffPane.classList.remove("hidden");
  $.editorMarkdown.classList.add("hidden");
}

/** The rendered-markdown read surface. The gutter goes with it: those line
 *  numbers count SOURCE lines, and beside rendered prose they would number
 *  something that is no longer on screen. */
function showMarkdownMode(): void {
  $.editorHighlight.classList.add("hidden");
  $.editorContent.classList.add("hidden");
  $.editorDiffPane.classList.add("hidden");
  $.editorMarkdown.classList.remove("hidden");
  $.editorGutter.classList.add("hidden");
}

// --- Gutter ---

export function updateGutter(content: string, agentLines?: ReadonlySet<number>): void {
  const lineCount = content.split("\n").length;
  // Production callers omit `agentLines` and get the per-file cache; tests
  // inject an explicit set so the reconcile behaviour is observable without
  // seeding module-private caches.
  const marks = agentLines ?? getAgentLines(getActiveFilePath());
  const lines = Array.from({ length: lineCount }, (_, i) => i + 1);
  // Keyed reconcile by line number is the sole owner of the gutter DOM. It
  // handles add/remove on line-count change (a file switch reconciles cleanly
  // — surplus rows are removed, missing rows mounted) AND refreshes the
  // agent-modified class on EXISTING rows: `update` toggles the class
  // unconditionally, so a switch to a file with different (or no) agent lines
  // clears stale highlights without any manual replaceChildren(). Dropping the
  // clear also preserves row identity + scrollTop across switches.
  reconcile($.editorGutter, lines, {
    key: (n) => String(n),
    mount: (n) => {
      const line = el("div", { className: "gutter-line" }, String(n));
      if (marks.has(n)) {
        line.classList.add("gutter-agent-modified");
      }
      return line;
    },
    update: (lineEl, n) => {
      lineEl.classList.toggle("gutter-agent-modified", marks.has(n));
    },
  });
}

// --- Highlight rendering ---

function renderHighlight(content: string, path: string): void {
  $.editorCode.innerHTML = highlight(content, path);
  updateGutter(content);
}

/** Paint the READ surface of a loaded file: rendered markdown for a document,
 *  syntax-highlighted source for everything else.
 *
 *  One funnel because there is more than one read-paint site — this module's
 *  mode renderer and editor-core's Cancel, which resets to the saved text
 *  without going through restoreUI. A markdown branch added to only one of them
 *  leaves the rendered view missing after Cancel. */
export function renderReadSurface(state: FileState): void {
  if (rendersMarkdown(state.path)) {
    renderMarkdownDoc($.editorMarkdown, state.current.value);
    showMarkdownMode();
    return;
  }
  renderHighlight(state.current.value, state.path);
  showReadMode();
}

// --- Edit-mode UI ---

export function renderEditModeUI(state: FileState): void {
  $.editorEditBtn.disabled = false;
  const isModified = state.current.value !== state.original.value;
  $.editorDiffBtn.classList.toggle("hidden", !isModified);
  $.editorDiffBtn.setAttribute("data-tooltip", "View diff vs saved");
  $.editorDiffBtn.setAttribute("aria-label", "View diff vs saved");
  const m = state.mode.value;
  const editing = m.kind === "edit" && m.editing;
  if (editing) {
    $.editorContent.value = state.current.value;
    showEditMode();
    updateGutter(state.current.value);
    $.editorEditBtn.classList.add("hidden");
    $.editorCancelBtn.classList.remove("hidden");
    $.editorSaveBtn.classList.remove("hidden");
  } else {
    renderReadSurface(state);
    $.editorEditBtn.classList.remove("hidden");
    $.editorCancelBtn.classList.add("hidden");
    $.editorSaveBtn.classList.add("hidden");
  }
}

// --- Line jump ---

export function applyPendingLine(path: string): void {
  const line = pendingLines.get(path);
  if (line === undefined || line <= 0) {
    return;
  }
  pendingLines.delete(path);
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      scrollToEditorLine(line);
      flashEditorLine(line);
    });
  });
}
