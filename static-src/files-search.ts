// ---------------------------------------------------------------------------
// Find in files: the file browser's recursive content search.
//
// Server-side by necessity, not preference. The client holds one directory's
// listing, so there is nothing here to search recursively, and the server owns
// the confinement (the granted-roots allow-list plus one kernel-confined root
// per mount) that a walk has to run inside. Wire contract:
// internal/filehandler/filehandler_search.go.
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
import { apiGet, CancellableSlot } from "./api-client.js";
import { openAtLine } from "./navigate.js";
import { reconcile } from "./reconcile.js";
import { fileIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";

// --- Wire types ------------------------------------------------------------
// Hand-declared beside the feature, the chat-search-types.ts precedent: one
// endpoint, one record, no codegen registration.

/** One matching line; mirrors filehandler.FileMatch. */
export interface FileSearchMatch {
  /** Container-absolute path, the namespace every /api/file* route speaks. */
  path: string;
  excerpt: string;
  line: number;
}

/** Mirrors filehandler.FileSearchResult. */
export interface FileSearchResult {
  matches: FileSearchMatch[];
  /** How many files the scan took up. */
  scanned: number;
  /** True when the answer is incomplete because the scan stopped at one of its
   *  caps, so files were left unread. The UI must say so rather than let a
   *  short result imply the text is nowhere else. */
  truncated: boolean;
}

/** Matches find-in-chat's TYPE_DEBOUNCE_MS: small enough to feel instant, large
 *  enough to coalesce a burst of keystrokes. */
const TYPE_DEBOUNCE_MS = 90;

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
   *  rather than imported: the opener is files.ts's (toggleFilesView takes that
   *  module's loader and reset), so reaching for it here would be a cycle. */
  activateBrowser: () => void;
}

let ctx: FilesSearchCtx | null = null;
let barEl: HTMLElement | null = null;
let resultsEl: HTMLElement | null = null;
let inputEl: HTMLInputElement | null = null;
let includeEl: HTMLInputElement | null = null;
let excludeEl: HTMLInputElement | null = null;
let noteEl: HTMLElement | null = null;
let caseBtn: HTMLButtonElement | null = null;
/** Persists across open/close: a preference about how the reader searches, not
 *  state belonging to one query. Same reasoning as find-in-chat's. */
let caseSensitive = false;
let isOpen = false;
let typeTimer: ReturnType<typeof setTimeout> | undefined;
/** Its OWN abort slot. Sharing the browser's would make a search and a
 *  directory load cancel each other. */
const searchSlot = new CancellableSlot();
let lastMatches: FileSearchMatch[] = [];

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
  if (opts.caseSensitive === true) {
    q.set("case", "1");
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

function field(id: string, placeholder: string, label: string, title: string): HTMLInputElement {
  return el("input", {
    id,
    type: "text",
    className: "fb-search-field",
    placeholder,
    "aria-label": label,
    title,
    autocomplete: "off",
    autocapitalize: "off",
    spellcheck: "false",
  }) as HTMLInputElement;
}

function barButton(label: string, glyph: string, onClick: () => void): HTMLButtonElement {
  const btn = el(
    "button",
    { type: "button", className: "fb-search-btn", "aria-label": label, title: label },
    glyph,
  ) as HTMLButtonElement;
  btn.addEventListener("click", onClick);
  return btn;
}

function ensureBuilt(): void {
  if (barEl !== null) {
    return;
  }
  const query = field(
    "fb-search-input",
    "Find in files\u2026",
    "Find in files",
    "Find in files. Press Ctrl+F again to use the browser's find.",
  );
  query.setAttribute("enterkeyhint", "search");
  inputEl = query;

  const caseToggle = barButton("Match case", "Aa", () => {
    caseSensitive = !caseSensitive;
    caseBtn?.setAttribute("aria-pressed", caseSensitive ? "true" : "false");
    runSearch();
  });
  caseToggle.classList.add("fb-search-case");
  caseToggle.setAttribute("aria-pressed", caseSensitive ? "true" : "false");
  caseBtn = caseToggle;

  const closeBtn = barButton("Close find", "\u00d7", () => {
    closeFilesSearch();
  });

  includeEl = field("fb-search-include", "Include (*.go)", "Include patterns", GLOB_HINT);
  excludeEl = field("fb-search-exclude", "Exclude (node_modules)", "Exclude patterns", GLOB_HINT);

  const note = el("div", {
    id: "fb-search-note",
    className: "fb-search-note text-muted",
    role: "status",
    "aria-live": "polite",
    "aria-atomic": "true",
  });
  noteEl = note;

  const bar = el(
    "div",
    {
      id: "fb-search",
      className: "fb-search hidden",
      role: "search",
      "aria-label": "Find in files",
    },
    el("div", { className: "fb-search-row" }, query, caseToggle, closeBtn),
    el("div", { className: "fb-search-row fb-search-globs" }, includeEl, excludeEl),
    note,
  );

  const results = el("div", {
    id: "fb-search-results",
    className: "fb-list fb-search-results hidden",
    role: "list",
  });

  for (const target of [query, includeEl, excludeEl]) {
    target.addEventListener("input", () => {
      scheduleSearch();
    });
    target.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeFilesSearch();
      } else if (e.key === "Enter") {
        e.preventDefault();
        runSearch();
      }
    });
  }

  $.fbList.insertAdjacentElement("beforebegin", bar);
  $.fbList.insertAdjacentElement("afterend", results);
  barEl = bar;
  resultsEl = results;
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

function setNote(text: string): void {
  if (noteEl !== null) {
    noteEl.textContent = text;
  }
}

function scheduleSearch(): void {
  if (typeTimer !== undefined) {
    clearTimeout(typeTimer);
  }
  typeTimer = setTimeout(() => {
    typeTimer = undefined;
    runSearch();
  }, TYPE_DEBOUNCE_MS);
}

function runSearch(): void {
  if (inputEl === null || ctx === null) {
    return;
  }
  const query = inputEl.value.trim();
  const searchPath = ctx.getSearchPath();
  if (query === "") {
    // Cancel rather than fire an empty query: the server answers an empty scan
    // for it, and a stale in-flight response must not repaint the cleared list.
    searchSlot.abort();
    lastMatches = [];
    renderResults(searchPath);
    setNote("");
    return;
  }
  const signal = searchSlot.start();
  const url = searchURL(searchPath, query, {
    caseSensitive,
    include: includeEl?.value.trim() ?? "",
    exclude: excludeEl?.value.trim() ?? "",
  });
  void apiGet<FileSearchResult>(url, signal).then((res) => {
    // Superseded by newer typing, or the bar closed while this was in flight.
    if (signal.aborted || !isOpen || inputEl?.value.trim() !== query) {
      return;
    }
    if (res === null) {
      setNote("Search failed. Check your connection.");
      return;
    }
    lastMatches = res.matches;
    renderResults(searchPath);
    setNote(searchNote(res));
  });
}

// --- Lifecycle -------------------------------------------------------------

export function initFilesSearch(c: FilesSearchCtx): void {
  ctx = c;
  $.fbSearchBtn.addEventListener("click", () => {
    if (isOpen) {
      closeFilesSearch();
    } else {
      openFilesSearch();
    }
  });
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
  if (barEl === null || inputEl === null || resultsEl === null) {
    return;
  }
  isOpen = true;
  barEl.classList.remove("hidden");
  resultsEl.classList.remove("hidden");
  $.fbList.classList.add("hidden");
  $.fbSearchBtn.setAttribute("aria-pressed", "true");
  inputEl.focus();
  inputEl.select();
  runSearch();
}

export function closeFilesSearch(): void {
  if (!isOpen || barEl === null || resultsEl === null) {
    return;
  }
  if (typeTimer !== undefined) {
    clearTimeout(typeTimer);
    typeTimer = undefined;
  }
  searchSlot.abort();
  isOpen = false;
  barEl.classList.add("hidden");
  resultsEl.classList.add("hidden");
  resultsEl.replaceChildren();
  lastMatches = [];
  $.fbList.classList.remove("hidden");
  $.fbSearchBtn.setAttribute("aria-pressed", "false");
  setNote("");
}

/** Drop the search when the browser resets (tab close). Mirrors
 *  resetFileBrowser's own DOM clear: rows kept while hidden would replay their
 *  entry animation in unison on the next open. */
export function resetFilesSearch(): void {
  closeFilesSearch();
  if (inputEl !== null) {
    inputEl.value = "";
  }
}

/** @internal Test seam: whether the bar is open. */
export function _isFilesSearchOpen(): boolean {
  return isOpen;
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
  if (isOpen && inputEl !== null && document.activeElement === inputEl) {
    return;
  }
  e.preventDefault();
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
