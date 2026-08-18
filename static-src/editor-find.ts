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
import { FindEngine } from "./find-engine.js";
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
/** The DOM cursor, for a `dom` search. Non-null only while one is showing, so it
 *  doubles as "which engine produced what is on screen" — the two cursors are
 *  never both live, because one surface is visible at a time. */
let domFind: FindEngine | null = null;
/** The query `matches` was produced for, so a step can tell "move the cursor"
 *  from "the box says something else now". */
let searched = "";
let unsubTab: (() => void) | null = null;

/** What one run produced. Discriminated, because the two engines count different
 *  things: a buffer match is a line, a DOM match is a node. */
type FindResult = { engine: "buffer"; hits: BufferMatch[] } | { engine: "dom"; total: number };

/** How many matches are on screen, whichever engine found them. */
function total(): number {
  return domFind !== null ? domFind.total : matches.length;
}

/** Where the cursor sits, whichever engine holds it. */
function cursor(): number {
  return domFind !== null ? domFind.currentIndex : current;
}

/** Drop the previous run's highlight. Called at the top of every run and on
 *  every close, because a `<mark>` left in a diff pane is welded there for the
 *  rest of the session — the same rule find-in-chat.ts records for the
 *  transcript. */
function clearDomFind(): void {
  domFind?.clear();
  domFind = null;
}

/** Which engine the active editor surface needs, or null when it has no text to
 *  search at all.
 *
 *  TWO ANSWERS, not one, and that split is what let the diff pane and rendered
 *  markdown join. This used to return "searchable or not", and it said NOT for
 *  three surfaces on the reasoning that `editor-scroll.ts` derives a line's
 *  position from a fixed line height — true for the highlighted source pre and
 *  for the textarea that shares its metrics, false for reflowed prose and for a
 *  two-pane diff. That is an argument against LINE arithmetic, and it was read as
 *  an argument against searching, so the chord silently changed meaning on one
 *  tab depending on its mode: the app's bar in edit mode, the browser's find in
 *  diff mode.
 *
 *    - `buffer` — the source pre or the textarea. A match is a LINE, so the bar
 *      counts matches in `FileState.current` and jumps the gutter to one.
 *    - `dom` — the diff pane or rendered markdown. A match is a NODE, so the
 *      shared find-engine wraps it in a `<mark>` and scrolls it into view. No
 *      line number is involved, so nothing depends on the geometry that does not
 *      hold there.
 *    - `null` — an image. It has no text, and no engine can invent one; the chord
 *      falls through and the browser's find opens.
 *
 *  Reads only the active-path and mode SIGNALS, never the buffer, so an effect
 *  over it re-runs on a file switch or a mode swap and not on every keystroke.
 *  That is what `editorFindAvailable` needs. */
type SurfaceEngine = "buffer" | "dom";

function surfaceEngine(): SurfaceEngine | null {
  const path = getActiveFilePath();
  if (path === "") {
    return null;
  }
  const state = fileStates.get(path);
  if (state === undefined) {
    return null;
  }
  const mode = state.mode.value;
  if (mode.kind === "image") {
    return null;
  }
  if (mode.kind === "diff") {
    return "dom";
  }
  // A `.md` file in READ mode is reflowed prose; the same file in EDIT mode is a
  // textarea over the source, where a line number means what editor-scroll thinks
  // it means.
  if (mode.kind === "edit" && !mode.editing && rendersMarkdown(path)) {
    return "dom";
  }
  return "buffer";
}

/** The rendered root a `dom` search walks, or null when it is not on screen yet.
 *
 *  Selected by which surface is unhidden rather than by re-deriving the mode:
 *  `editor-modes.ts` owns that toggle, and asking the DOM which one it revealed
 *  cannot disagree with it. */
function domRoot(): HTMLElement | null {
  for (const id of ["editor-diff-pane", "editor-markdown"]) {
    const host = document.getElementById(id);
    if (host !== null && !host.classList.contains("hidden")) {
      return host;
    }
  }
  return null;
}

/** Whether the toolbar's search button has a destination on this editor tab.
 *
 *  Deliberately does NOT wait for the file to load, which is the one difference
 *  from the two searchable-content readers below. Gating it on `loaded` too would
 *  collapse and re-expand the toolbar for the duration of every file read — a bar
 *  that flinches each time you open a file, to answer a question nobody asked. The
 *  mode already says whether the surface has text, and that is decided when the
 *  file is opened.
 *
 *  So `FileState.loaded` being a plain field rather than a signal is not a gap
 *  here: nothing in this answer depends on it. */
export function editorFindAvailable(): boolean {
  return surfaceEngine() !== null;
}

/** The active file's buffer, for a `buffer` search.
 *
 *  Requires the bytes: opening a find over a buffer that has not arrived would
 *  report "No matches" about a file nobody has read yet. */
function searchableBuffer(): string | null {
  if (surfaceEngine() !== "buffer") {
    return null;
  }
  const state = fileStates.get(getActiveFilePath());
  if (state?.loaded !== true) {
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
  countEl.textContent = formatBufferCount(total(), cursor(), query);
  barEl?.classList.toggle("editor-find-no-results", query !== "" && total() === 0);
}

/** Take the cursor to the current match.
 *
 *  Two ways, because a match is two different things. Over source it is a LINE, so
 *  editor-scroll places it and the gutter flashes it — after two frames, because
 *  the read surface must be laid out before its geometry can be read (the same
 *  reason editor-ui.ts's applyPendingLine defers). Over a diff pane or rendered
 *  prose it is a NODE, so the browser scrolls the `<mark>` itself and no geometry
 *  is computed at all. */
function reveal(): void {
  if (domFind !== null) {
    revealMark();
    return;
  }
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

function revealMark(): void {
  const mark = domFind?.currentMark() ?? null;
  // happy-dom implements no scrolling, so the call is guarded rather than
  // assumed — the same shape find-in-chat.ts uses.
  const scrollFn = (mark as { scrollIntoView?: (o?: ScrollIntoViewOptions) => void } | null)
    ?.scrollIntoView;
  if (typeof scrollFn === "function" && mark !== null) {
    scrollFn.call(mark, { block: "center", inline: "nearest" });
  }
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
  if (total() === 0) {
    return;
  }
  if (domFind !== null) {
    if (dir === 1) {
      domFind.next();
    } else {
      domFind.prev();
    }
  } else {
    current = (current + dir + matches.length) % matches.length;
  }
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

  const built = createSearchShell<FindResult>({
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
      // Every run starts from a clean surface: the previous run's marks go before
      // the next one wraps any, which also covers a mode swap under an open bar.
      clearDomFind();
      matches = [];
      if (surfaceEngine() === "dom") {
        const root = domRoot();
        if (root === null) {
          return { engine: "dom", total: 0 };
        }
        const found = new FindEngine(root);
        const count = found.search(query, qctx.caseSensitive);
        domFind = found;
        return { engine: "dom", total: count };
      }
      const buffer = searchableBuffer();
      return {
        engine: "buffer",
        hits: buffer === null ? [] : findInBuffer(buffer, query, qctx.caseSensitive),
      };
    },
    render: (found, query) => {
      searched = query;
      if (found !== null && found.engine === "buffer") {
        matches = found.hits;
        current = matches.length > 0 ? 0 : -1;
      }
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

  // A tab switch closes the bar and FORGETS the query, the same rule
  // find-in-chat.ts records. It matters more here than there: `openEditorFind`
  // runs on open and the buffer under the box is a different FILE, so a retained
  // query searched the next file for a string typed against the previous one and
  // reported a count for it.
  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, () => {
    closeEditorFind();
    if (shell !== null) {
      shell.input.value = "";
    }
  });
}

/** Open (or refocus) the in-file find. No-op when the active surface has no text
 *  at all — an image — so the caller's `preventDefault` can be conditioned on it
 *  and native find stays reachable where this one declines.
 *
 *  Gated on the SURFACE, not on the bytes: a `dom` search reads what is rendered,
 *  so it has nothing to wait for, and a `buffer` search over an unloaded file
 *  reports "No matches" for one frame rather than refusing to open. Refusing was
 *  the old behaviour and it cost more than it bought — the chord did nothing at
 *  all while a file loaded. */
export function openEditorFind(): boolean {
  if (surfaceEngine() === null) {
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
  // The marks go with the box. A `<mark>` left behind is welded into the diff
  // pane for the rest of the session, and the next render of that pane would
  // reconcile around it.
  clearDomFind();
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
  if (surfaceEngine() === null) {
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
