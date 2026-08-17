// ---------------------------------------------------------------------------
// Find in the open file's BUFFER.
//
// The gap this closes was actively harmful rather than merely missing: Ctrl-F on
// a file tab routed to find-in-FILES, which activates the browser view — so the
// chord that every editor in the world binds to "search this document" switched
// you AWAY from the document you were editing. A missing affordance is a gap; one
// that navigates you off the page is a trap.
//
// NO NETWORK. The buffer is already in memory as `FileState.current`, so this
// searches the string the editor is holding, not the file on disk. That is also
// the only honest thing to search: an unsaved edit is what the reader is looking
// at, and a server-side grep would answer about the saved version.
//
// SCOPED TO THE SOURCE SURFACES, and that is a decision rather than an
// oversight. `editor-scroll.ts` derives a line's position from
// `getComputedStyle($.editorCode).lineHeight` and a fixed line height, which is
// true for the highlighted source pre and for the textarea that shares its
// metrics — and false for rendered markdown (prose reflows, the gutter is
// hidden), for an image, and for the diff pane. In those three the browser's own
// find is the better tool anyway (all three are DOM text), so this one declines
// the key and native find opens. A find that reported "3 of 17" and could not
// take you to any of them would be a control that does nothing.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { byId } from "./dom.js";
import { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { fileStates, getActiveFilePath, rendersMarkdown } from "./editor-types.js";
import { flashEditorLine, scrollToEditorLine } from "./editor-scroll.js";
import { createSearchShell, searchIconButton } from "./search-shell.js";
import type { SearchShell } from "./search-shell.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";

/** One match: the 1-based line it sits on, and its offset within the buffer so
 *  two matches on one line stay distinct positions. */
export interface BufferMatch {
  line: number;
  offset: number;
}

/**
 * Every occurrence of `needle` in `text`, with the 1-based line each sits on.
 *
 * Pure and exported for tests. Counts newlines up to each hit with a running
 * cursor rather than splitting the buffer into lines: a large file's split
 * allocates the whole document a second time, and the running count is the same
 * answer.
 */
export function findInBuffer(text: string, needle: string, caseSensitive: boolean): BufferMatch[] {
  if (needle === "") {
    return [];
  }
  const hay = caseSensitive ? text : text.toLowerCase();
  const want = caseSensitive ? needle : needle.toLowerCase();
  const out: BufferMatch[] = [];
  let line = 1;
  let scanned = 0;
  let idx = hay.indexOf(want);
  while (idx >= 0) {
    for (let i = scanned; i < idx; i++) {
      if (text[i] === "\n") {
        line++;
      }
    }
    scanned = idx;
    out.push({ line, offset: idx });
    // Advance past this hit's FIRST character, not past the whole match, so
    // overlapping occurrences ("aa" in "aaa") are both found — the same
    // convention a text editor's find uses.
    idx = hay.indexOf(want, idx + 1);
  }
  return out;
}

/** The counter, in the transcript search's wording so the two cursors read
 *  alike. */
export function formatBufferCount(total: number, current: number, query: string): string {
  if (query === "") {
    return "";
  }
  if (total === 0) {
    return "No matches";
  }
  return `${String(current + 1)} of ${String(total)}`;
}

let barEl: HTMLElement | null = null;
let shell: SearchShell | null = null;
let countEl: HTMLElement | null = null;
let matches: BufferMatch[] = [];
let current = -1;
/** The query `matches` was produced for, so a step can tell "move the cursor"
 *  from "the box says something else now". */
let searched = "";
let unsubTab: (() => void) | null = null;

/** The active file's buffer, or null when there is no text surface to search.
 *
 *  `rendersMarkdown` and the mode kind are BOTH checked: a `.md` file in read
 *  mode shows reflowed prose, but the same file in EDIT mode is a textarea over
 *  the source, where a line number means what editor-scroll thinks it means. */
function searchableBuffer(): string | null {
  const path = getActiveFilePath();
  if (path === "") {
    return null;
  }
  const state = fileStates.get(path);
  if (state?.loaded !== true) {
    return null;
  }
  const mode = state.mode.value;
  if (mode.kind === "diff" || mode.kind === "image") {
    return null;
  }
  if (mode.kind === "edit" && !mode.editing && rendersMarkdown(path)) {
    return null;
  }
  return state.current.value;
}

function isOpen(): boolean {
  return barEl !== null && !barEl.classList.contains("hidden");
}

function updateCounter(query: string): void {
  if (countEl === null) {
    return;
  }
  countEl.textContent = formatBufferCount(matches.length, current, query);
  barEl?.classList.toggle("editor-find-no-results", query !== "" && matches.length === 0);
}

/** Take the cursor to the current match. Two frames of wait because the read
 *  surface must be laid out before its geometry can be read — the same reason
 *  editor-ui.ts's applyPendingLine defers. */
function reveal(): void {
  const hit = matches[current];
  if (hit === undefined) {
    return;
  }
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      scrollToEditorLine(hit.line);
      flashEditorLine(hit.line);
    });
  });
}

function step(dir: 1 | -1): void {
  if (shell === null) {
    return;
  }
  // A fast type-then-Enter must SEARCH first and land on the first match, which
  // is what native find does; only a step against the query already searched
  // moves the cursor. find-in-chat's step carries the same check for the same
  // reason, and without it Enter after typing is a no-op.
  if (searched !== shell.value) {
    shell.run();
    return;
  }
  if (matches.length === 0) {
    return;
  }
  current = (current + dir + matches.length) % matches.length;
  updateCounter(shell.value);
  reveal();
}

function ensureBuilt(): void {
  if (barEl !== null) {
    return;
  }
  const count = el("span", {
    id: "editor-find-count",
    className: "editor-find-count",
    role: "status",
    "aria-live": "polite",
    "aria-atomic": "true",
  });
  countEl = count;

  const prevBtn = searchIconButton(
    "editor-find-btn",
    "Previous match",
    "Previous (Shift+Enter)",
    ICON_CHEVRON_UP,
    () => {
      step(-1);
    },
  );
  const nextBtn = searchIconButton(
    "editor-find-btn",
    "Next match",
    "Next (Enter)",
    ICON_CHEVRON_DOWN,
    () => {
      step(1);
    },
  );

  const built = createSearchShell<BufferMatch[]>({
    id: "editor-find",
    regionClass: "editor-find hidden",
    inputClass: "editor-find-input",
    buttonClass: "editor-find-btn",
    caseClass: "editor-find-case",
    label: "Find in file",
    placeholder: "Find in file\u2026",
    inputTitle: "Find in this file. Press Ctrl+F again to use the browser's find.",
    matchCase: true,
    closeButton: true,
    compose: ({ input, caseButton, closeButton }) => [
      input,
      count,
      el("div", { className: "editor-find-nav" }, caseButton, prevBtn, nextBtn, closeButton),
    ],
    // SYNCHRONOUS: there is no network here, so the count paints in the same tick
    // as the keystroke that asked for it rather than a microtask later.
    query: (query, qctx) => {
      const buffer = searchableBuffer();
      return buffer === null ? [] : findInBuffer(buffer, query, qctx.caseSensitive);
    },
    render: (found, query) => {
      matches = found ?? [];
      searched = query;
      current = matches.length > 0 ? 0 : -1;
      updateCounter(query);
      reveal();
    },
    onDismiss: () => {
      closeEditorFind();
    },
    onSubmit: (shift) => {
      step(shift ? -1 : 1);
    },
  });
  shell = built;

  // The bar sits between the conflict overlay and the scroller, in normal flow:
  // `.editor-body` is the scroller, so a docked bar shrinks it instead of
  // covering the first lines of the file. That is the same reasoning 19-files.css
  // records for the file browser's in-flow bar.
  const page = byId("editor-conflict-overlay").parentElement;
  page?.insertBefore(built.region, byId("editor-conflict-overlay").nextSibling);
  barEl = built.region;

  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, () => {
    closeEditorFind();
  });
}

/** Open (or refocus) the in-file find. No-op when the active surface has no
 *  searchable buffer, so the caller's `preventDefault` can be conditioned on it
 *  and native find stays reachable where this one declines. */
export function openEditorFind(): boolean {
  if (searchableBuffer() === null) {
    return false;
  }
  ensureBuilt();
  if (barEl === null || shell === null) {
    return false;
  }
  barEl.classList.remove("hidden");
  shell.focus();
  shell.run();
  return true;
}

export function closeEditorFind(): void {
  if (!isOpen() || barEl === null) {
    return;
  }
  shell?.cancel();
  barEl.classList.add("hidden");
  matches = [];
  current = -1;
  searched = "";
  updateCounter("");
}

export function toggleEditorFind(): void {
  if (isOpen()) {
    closeEditorFind();
    return;
  }
  openEditorFind();
}

/** Ctrl-F / Cmd-F over an editor tab.
 *
 *  Carries the same second-press escape hatch every destination owns: a repeat
 *  press while our field has focus falls through with no preventDefault. And it
 *  only claims the key when there IS a buffer to search — over a diff pane, an
 *  image or rendered markdown the browser's find is the better tool and gets the
 *  chord. */
export function handleEditorFindHotkey(e: KeyboardEvent): boolean {
  if (e.key.toLowerCase() !== "f" || !(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) {
    return false;
  }
  if (isOpen() && shell !== null && document.activeElement === shell.input) {
    return true;
  }
  if (searchableBuffer() === null) {
    return false;
  }
  e.preventDefault();
  openEditorFind();
  return true;
}

/** @internal Test seam. */
export function _isEditorFindOpen(): boolean {
  return isOpen();
}
