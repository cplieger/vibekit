// ---------------------------------------------------------------------------
// Find in files: the file browser's recursive content search.
//
// Server-side by necessity, not preference. The client holds one directory's
// listing, so there is nothing here to search recursively, and the server owns
// the confinement (the granted-roots allow-list plus one kernel-confined root
// per mount) that a walk has to run inside. Wire contract:
// internal/filebrowse/search.go.
//
// TWO SURFACES, ONE VOCABULARY. The bar deliberately mirrors find-in-chat's:
// the same `Aa` latched match-case toggle spelled with the same `aria-pressed`,
// the same typing debounce, the same second-press escape hatch back to the
// browser's native find. Ctrl-F means "find in what I am looking at" in both,
// and a reader should not have to learn two boxes.
//
// IT SAYS WHAT IT DID NOT READ. A repo holds far more files than the caps allow,
// so the scan stops routinely; a bare "no matches" over a stopped scan tells the
// reader the text is nowhere when most of the tree was never opened. The note
// states the file count and whether the scan stopped, in the same shape the
// History page's cross-chat note uses.
//
// It writes into its OWN results list rather than #fb-list. Four things in
// files.ts assume every row in that list is an entry of one directory (the git
// badge repaint, the selection highlight sweep, the sorted-name index behind
// shift-select, and the stagger index), and a hit row would break each.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { $, byId } from "./dom.js";
import { apiGet } from "./api-client.js";
import { openAtLine } from "./navigate.js";
import { reconcile } from "./reconcile.js";
import { fileIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { caseParam, createSearchShell, searchField, wireSearchKeys } from "./search-shell.js";
import type { SearchShell } from "./search-shell.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";

// --- Wire types ------------------------------------------------------------
// Hand-declared beside the feature, the chat-search-types.ts precedent: one
// endpoint, one record, no codegen registration.

/** One matching line; mirrors filebrowse.FileMatch. */
export interface FileSearchMatch {
  /** Container-absolute path, the namespace every /api/file* route speaks. */
  path: string;
  excerpt: string;
  line: number;
}

/** Mirrors filebrowse.FileSearchResult. */
export interface FileSearchResult {
  matches: FileSearchMatch[];
  /** How many files the scan took up. */
  scanned: number;
  /** True when the answer is incomplete because the scan stopped at one of its
   *  caps, so files were left unread. The UI must say so rather than let a
   *  short result imply the text is nowhere else. */
  truncated: boolean;
}

/** The glob convention, stated where the user meets it.
 *
 *  It is a real convention rather than raw glob semantics, so it has to be
 *  written down somewhere a reader will see: a `*` does not cross a `/`, which
 *  is why a pattern with no slash is matched against the file NAME. */
const GLOB_HINT =
  "One or more patterns, comma separated. " +
  "A pattern without a slash matches the file name at any depth (*.go); " +
  "one with a slash matches the path under the folder searched (src/*.go). " +
  "Exclude also skips a whole folder (node_modules).";

export interface FilesSearchCtx {
  /** The folder the browser is showing, which is the search ROOT. */
  getSearchPath: () => string;
  /** Bring the file browser into view.
   *
   *  Ctrl-F in an EDITOR tab means find-in-files, and this surface lives inside
   *  the (hidden) files view, so the tab has to be activated first. Injected
   *  rather than imported: the opener is files.ts's (showFilesView takes that
   *  module's loader and reset), so reaching for it here would be a cycle.
   *
   *  It must SHOW, never toggle. Two of this module's three callers run with the
   *  files tab already active, where a toggle closed the very view the bar is
   *  about to render into. `tabs.ts` showFilesView is the verb that cannot. */
  activateBrowser: () => void;
}

let ctx: FilesSearchCtx | null = null;
let barEl: HTMLElement | null = null;
let resultsEl: HTMLElement | null = null;
let includeEl: HTMLInputElement | null = null;
let excludeEl: HTMLInputElement | null = null;
/** The box's shell: the field, the `Aa` toggle, the note, the debounce and the
 *  supersession guard. Its abort signal is its OWN — sharing the browser's would
 *  make a search and a directory load cancel each other. */
let shell: SearchShell | null = null;
let lastMatches: FileSearchMatch[] = [];
/** Unsubscribe for the tab-change teardown, so a rebuilt module does not stack a
 *  second subscriber on the bus. Mirrors find-in-chat.ts and editor-find.ts. */
let unsubTab: (() => void) | null = null;

// --- Pure helpers (exported for tests) -------------------------------------

/** The search URL for one query. `case=1` only when asked, because the server
 *  reads an absent parameter as insensitive; the transcript search sends the
 *  same flag the same way, so the two boxes cannot disagree about the toggle. */
export function searchURL(
  path: string,
  query: string,
  opts: { caseSensitive?: boolean; include?: string; exclude?: string } = {},
): string {
  const q = new URLSearchParams({ path, q: query });
  const flag = caseParam(opts.caseSensitive === true);
  if (flag !== "") {
    q.set("case", flag);
  }
  if ((opts.include ?? "") !== "") {
    q.set("include", opts.include ?? "");
  }
  if ((opts.exclude ?? "") !== "") {
    q.set("exclude", opts.exclude ?? "");
  }
  return `/api/files/search?${q.toString()}`;
}

/** The note under the search box. Truncation is stated in every branch that has
 *  it, including the empty one: an empty result over a stopped scan would
 *  otherwise read as "the text is nowhere". */
export function searchNote(res: FileSearchResult): string {
  const files = res.scanned === 1 ? "1 file" : `${String(res.scanned)} files`;
  const tail = res.truncated ? " The scan stopped at its limit, so more were not read." : "";
  if (res.matches.length === 0) {
    return `No matches in ${files}.${tail}`;
  }
  const n = res.matches.length;
  const label = n === 1 ? "1 match" : `${String(n)} matches`;
  return `${label} in ${files}.${tail}`;
}

/** A hit's path as the reader should see it: relative to the folder searched
 *  when it sits under it, absolute otherwise (a root search spans mounts, where
 *  there is no one folder to be relative to). */
export function hitLabel(searchPath: string, abs: string): string {
  if (searchPath === "" || searchPath === ".") {
    return abs;
  }
  const root = `/${searchPath.replace(/\/+$/, "")}/`;
  return abs.startsWith(root) ? abs.slice(root.length) : abs;
}

/** Reconcile key for a hit row. Two hits differ by path AND line, and a colon
 *  is a legal filename character, so the composite goes through keyenc rather
 *  than a template literal. */
export function hitKey(m: FileSearchMatch): string {
  return join("hit", m.path, String(m.line));
}

// --- DOM -------------------------------------------------------------------

function globField(id: string, placeholder: string, label: string): HTMLInputElement {
  return searchField({
    id,
    className: "fb-search-field",
    label,
    placeholder,
    title: GLOB_HINT,
  });
}

function ensureBuilt(): void {
  if (barEl !== null) {
    return;
  }
  includeEl = globField("fb-search-include", "Include (*.go)", "Include patterns");
  excludeEl = globField("fb-search-exclude", "Exclude (node_modules)", "Exclude patterns");
  const globRow = el("div", { className: "fb-search-row fb-search-globs" }, includeEl, excludeEl);

  // The GLOB ROW is this surface's alone — a transcript has no paths to include
  // or exclude — so it arrives through `compose` rather than becoming a shell
  // feature. Everything above it is the shell's: the field's attributes, the
  // debounce, the supersession guard, the `Aa` toggle's aria-pressed idiom and
  // the note.
  const built = createSearchShell<FileSearchResult>({
    id: "fb-search",
    regionClass: "fb-search hidden",
    inputClass: "fb-search-field",
    buttonClass: "fb-search-btn",
    caseClass: "fb-search-case",
    noteClass: "fb-search-note",
    label: "Find in files",
    placeholder: "Find in files\u2026",
    inputTitle: "Find in files. Press Ctrl+F again to use the browser's find.",
    matchCase: true,
    note: true,
    closeButton: true,
    compose: ({ input, caseButton, closeButton, note }) => [
      el("div", { className: "fb-search-row" }, input, caseButton, closeButton),
      globRow,
      note,
    ],
    query: async (query, qctx) => {
      const trimmed = query.trim();
      if (trimmed === "" || ctx === null) {
        return null;
      }
      return apiGet<FileSearchResult>(
        searchURL(ctx.getSearchPath(), trimmed, {
          caseSensitive: qctx.caseSensitive,
          include: includeEl?.value.trim() ?? "",
          exclude: excludeEl?.value.trim() ?? "",
        }),
        qctx.signal,
      );
    },
    render: (res, query) => {
      const searchPath = ctx?.getSearchPath() ?? "";
      if (query.trim() === "") {
        lastMatches = [];
        renderResults(searchPath);
        built.setNote("");
        return;
      }
      if (res === null) {
        built.setNote("Search failed. Check your connection.");
        return;
      }
      lastMatches = res.matches;
      renderResults(searchPath);
      built.setNote(searchNote(res));
    },
    onDismiss: () => {
      closeFilesSearch();
    },
    onSubmit: () => {
      built.run();
    },
  });
  shell = built;

  const results = el("div", {
    id: "fb-search-results",
    className: "fb-list fb-search-results hidden",
    role: "list",
  });

  // The glob fields feed the same query, so they schedule the same run and carry
  // the same key contract. Sharing wireSearchKeys is what keeps Escape meaning
  // the same thing in all three fields.
  for (const target of [includeEl, excludeEl]) {
    target.addEventListener("input", () => {
      built.schedule();
    });
    wireSearchKeys(target, {
      onDismiss: () => {
        closeFilesSearch();
      },
      onSubmit: () => {
        built.run();
      },
    });
  }

  $.fbList.insertAdjacentElement("beforebegin", built.region);
  $.fbList.insertAdjacentElement("afterend", results);
  barEl = built.region;
  resultsEl = results;

  // LEAVING the browser drops the search; ARRIVING at it never does, and the
  // asymmetry is what makes the subscription safe to add at all. `openFilesSearch`
  // activates the files tab before it opens the bar, and the tab store announces
  // that switch from a BATCHED effect — so a subscriber that closed on every
  // change would fire after the open had already landed and shut the bar the user
  // just asked for. Keying on the DESTINATION kind sidesteps the ordering
  // entirely: that emit carries `files`.
  //
  // Without the close half, this bar was the one search surface that survived a
  // tab switch (find-in-chat.ts and editor-find.ts have closed on this event for
  // as long as they have existed), so the browser kept a stale hit list and a
  // stale query in place of its directory listing until someone dismissed it by
  // hand.
  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, (e) => {
    if (e.kind === "files") {
      return;
    }
    resetFilesSearch();
  });
}

function hitRow(m: FileSearchMatch, label: string): HTMLElement {
  const row = el(
    "div",
    {
      className: "fb-row fb-search-hit",
      role: "listitem",
      tabindex: "0",
      "data-path": m.path,
      "data-line": String(m.line),
    },
    el("span", { className: "fb-icon" }, iconEl(fileIcon(m.path, false))),
    el("span", { className: "fb-name fb-name-link" }, label),
    el("span", { className: "fb-search-lineno" }, `:${String(m.line)}`),
    el("span", { className: "fb-search-excerpt" }, m.excerpt),
  );
  const open = (): void => {
    openAtLine(m.path, m.line);
  };
  row.addEventListener("click", open);
  row.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      open();
    }
  });
  return row;
}

function renderResults(searchPath: string): void {
  if (resultsEl === null) {
    return;
  }
  reconcile(resultsEl, lastMatches, {
    key: hitKey,
    mount: (m: FileSearchMatch) => hitRow(m, hitLabel(searchPath, m.path)),
    // Nothing on a hit row changes in place: a re-run produces a new hit set,
    // and a row whose path and line are unchanged shows the same line.
    update: () => undefined,
  });
}

// --- Lifecycle -------------------------------------------------------------

/** Open state is the bar's own class, not a second boolean.
 *
 *  Unlike the transcript's box this is NOT a popup: its results render into a
 *  sibling element OUTSIDE the panel, so the primitive's outside-click dismissal
 *  would close the bar on the first click of a result row. Placement decides
 *  dismissal, which is why search-shell.ts owns neither. */
function isOpen(): boolean {
  return barEl !== null && !barEl.classList.contains("hidden");
}

export function initFilesSearch(c: FilesSearchCtx): void {
  ctx = c;
}

/** Open (or refocus) the search bar.
 *
 *  Reachable from the toolbar button as well as the hotkey, and that is not
 *  optional: find-in-chat records the same rule, because a feature whose only
 *  door is Ctrl-F is undiscoverable on a desktop and unreachable on a tablet
 *  with no keyboard. */
export function openFilesSearch(): void {
  ctx?.activateBrowser();
  ensureBuilt();
  if (barEl === null || shell === null || resultsEl === null) {
    return;
  }
  barEl.classList.remove("hidden");
  resultsEl.classList.remove("hidden");
  $.fbList.classList.add("hidden");
  // The app toolbar's one contextual Find button is the only visible trigger;
  // the duplicate button in this bottom bar is gone.
  $.findBtn.setAttribute("aria-pressed", "true");
  shell.focus();
  shell.run();
}

export function closeFilesSearch(): void {
  if (!isOpen() || barEl === null || resultsEl === null) {
    return;
  }
  shell?.cancel();
  barEl.classList.add("hidden");
  resultsEl.classList.add("hidden");
  resultsEl.replaceChildren();
  lastMatches = [];
  $.fbList.classList.remove("hidden");
  $.findBtn.setAttribute("aria-pressed", "false");
  shell?.setNote("");
}

/** Drop the search entirely: close it AND forget what was typed.
 *
 *  Two callers, one meaning — the next time this browser is looked at, it is a
 *  directory listing rather than someone's old query. `resetFileBrowser` calls it
 *  on tab close (its own DOM clear is there for the same reason: rows kept while
 *  hidden replay their entry animation in unison on the next open), and the
 *  tab-change subscriber calls it on leaving.
 *
 *  The GLOBS are cleared with the query. They are part of the search the reader
 *  composed, not a standing preference, and an `Exclude: node_modules` still
 *  sitting in the bar an hour later silently narrows a search nobody asked it to
 *  narrow. */
export function resetFilesSearch(): void {
  closeFilesSearch();
  if (shell !== null) {
    shell.input.value = "";
  }
  if (includeEl !== null) {
    includeEl.value = "";
  }
  if (excludeEl !== null) {
    excludeEl.value = "";
  }
}

/** @internal Test seam: whether the bar is open. */
export function _isFilesSearchOpen(): boolean {
  return isOpen();
}

/** Ctrl-F / Cmd-F for a files or editor tab, dispatched from app.ts.
 *
 *  Carries its own second-press escape hatch, exactly as handleFindHotkey does:
 *  a repeat press while our field already has focus falls through with no
 *  preventDefault, so the browser's native find stays reachable. That hatch is
 *  the a11y justification for overriding the key at all, so each destination
 *  owns one rather than the dispatcher guessing. */
export function handleFindInFilesHotkey(e: KeyboardEvent): void {
  if (e.key.toLowerCase() !== "f" || !(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) {
    return;
  }
  if (isOpen() && shell !== null && document.activeElement === shell.input) {
    return;
  }
  e.preventDefault();
  openFilesSearch();
}

/** Toggle the file search. What the toolbar button and the dispatcher's button
 *  route mean — the same shape find-in-chat's toggle now has. */
export function toggleFilesSearch(): void {
  if (isOpen()) {
    closeFilesSearch();
    return;
  }
  openFilesSearch();
}

/** @internal Test seam: the lazily-built search bar, once it exists. */
export function _filesSearchBar(): HTMLElement | null {
  return document.getElementById("fb-search");
}

/** @internal Test seam: the results list, once it exists. */
export function _filesSearchResults(): HTMLElement {
  return byId<HTMLDivElement>("fb-search-results");
}
