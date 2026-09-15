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
// is the shared search grammar (textsearch/copy.ts) over the reply's tally, with
// this surface's nouns, so it reads like the History page's cross-chat note.
//
// It writes into its OWN results list rather than #fb-list. Four things in
// files.ts assume every row in that list is an entry of one directory (the git
// badge repaint, the selection highlight sweep, the sorted-name index behind
// shift-select, and the stagger index), and a hit row would break each.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { $, byId } from "./dom.js";
import { apiGetTyped } from "./api-client.js";
import { openAtLine } from "./navigate.js";
import { reconcile } from "./reconcile.js";
import { fileIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { caseParam, createSearchShell, searchField, wireSearchKeys } from "./search-shell.js";
import type { SearchShell } from "./search-shell.js";
import { FB_ROOT } from "./files-shared.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { getActiveTabId, getActiveTabKind } from "./tabs.js";
import { classify, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns } from "./textsearch/copy.js";
import type { FileMatch, FileSearchResult } from "./wire/types.gen.js";
import { decodeFileSearchResult } from "./wire/decoders.gen.js";

/** A match is a row (a line, or a name); the scan reads files. */
const NOUNS: Nouns = {
  match: { one: "match", many: "matches" },
  scanned: { one: "file", many: "files" },
};

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
  /** Bring the file browser into view. Injected rather than imported, because
   *  files.ts owns the opener and importing it here would be a cycle. It must SHOW
   *  and never toggle: every door here can run with the files tab already active,
   *  where a toggle closes the view the bar is about to render into. */
  activateBrowser: () => void;
  /** Navigate the browser to a folder a NAME hit named, and close the search.
   *  Injected rather than imported: the graph runs files -> files-search. */
  openFolder: (path: string) => void;
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
let lastMatches: FileMatch[] = [];
/** Unsubscribe for the tab-change teardown, so a rebuilt module does not stack a
 *  second subscriber on the bus. Mirrors find-in-chat.ts and editor-find.ts. */
let unsubTab: (() => void) | null = null;
/** The id of the files tab the open bar belongs to, `""` when no bar is open.
 *
 *  `""` is a VALUE, not a missing field: the teardown never fires for it, so a bar
 *  opened while nothing is active (`getActiveTabId()` answers `""` on an empty
 *  strip) is not torn down by the first activation that follows. */
let searchOwnerID = "";

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

/** A hit's path as the reader should see it: relative to the folder searched
 *  when it sits under it, absolute otherwise (a root search spans mounts, where
 *  there is no one folder to be relative to). */
export function hitLabel(searchPath: string, abs: string): string {
  // A hit's `path` is container-absolute (`FileMatch.Path`: "the same namespace
  // every other /api/file* route speaks"), and so is the folder that was
  // searched, so the prefix is the search path itself. It used to be spelled
  // `/${searchPath}` because the browser's own paths were rootless — the one
  // place that mismatch was visible, since a label that failed to strip simply
  // showed the whole path.
  if (searchPath === "" || searchPath === FB_ROOT) {
    return abs;
  }
  const root = `${searchPath.replace(/\/+$/, "")}/`;
  return abs.startsWith(root) ? abs.slice(root.length) : abs;
}

/** Reconcile key for a hit row. Two hits differ by path AND line, and a colon
 *  is a legal filename character, so the composite goes through keyenc rather
 *  than a template literal. */
export function hitKey(m: FileMatch): string {
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
      // An empty root means no browser is bound, so there is no folder to search.
      if (trimmed === "" || ctx === null || ctx.getSearchPath() === "") {
        return null;
      }
      return apiGetTyped(
        searchURL(ctx.getSearchPath(), trimmed, {
          caseSensitive: qctx.caseSensitive,
          include: includeEl?.value.trim() ?? "",
          exclude: excludeEl?.value.trim() ?? "",
        }),
        decodeFileSearchResult,
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
        built.setNote(emptyNote({ kind: "failed" }, NOUNS));
        return;
      }
      lastMatches = res.matches;
      renderResults(searchPath);
      if (res.matches.length === 0) {
        // A stopped scan must be stated: an empty result over one would
        // otherwise read as "the text is nowhere".
        built.setNote(
          emptyNote(
            classify({
              matched: res.matched,
              shown: 0,
              scanned: res.scanned,
              truncated: res.truncated,
            }),
            NOUNS,
          ),
        );
        return;
      }
      built.setNote(scanNote(res, res.matches.length, NOUNS));
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

  // Keyed on tab IDENTITY rather than the destination KIND: a switch from files tab A
  // to B carries `kind: "files"`, so B would inherit A's query and hit list.
  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, (e) => {
    if (searchOwnerID !== "" && e.to !== searchOwnerID) {
      resetFilesSearch();
    }
  });
}

/** The row for one hit.
 *
 *  LINE decides the SHAPE: a content hit (line >= 1) keeps the `:N` + excerpt
 *  row, and a name hit (line 0) is icon + label only, because a name has neither
 *  a line number nor a matching line to quote. KIND decides only where the row
 *  GOES, and the switch is total over the generated enum: a kind this bundle does
 *  not know fails the reply at the decoder rather than reaching a row nothing can
 *  open. */
function hitRow(m: FileMatch, label: string): HTMLElement {
  const isDir = m.kind === "dir";
  const isName = m.line === 0;
  const row = el(
    "div",
    {
      className: "fb-row fb-search-hit",
      role: "listitem",
      tabindex: "0",
      "data-path": m.path,
      "data-line": String(m.line),
      "data-kind": m.kind,
    },
    el("span", { className: "fb-icon" }, iconEl(fileIcon(m.path, isDir))),
    el("span", { className: "fb-name fb-name-link" }, label),
    ...(isName
      ? []
      : [
          el("span", { className: "fb-search-lineno" }, `:${String(m.line)}`),
          el("span", { className: "fb-search-excerpt" }, m.excerpt),
        ]),
  );
  const open = (): void => {
    switch (m.kind) {
      case "dir":
        ctx?.openFolder(m.path);
        return;
      case "name":
        openAtLine(m.path);
        return;
      case "content":
        openAtLine(m.path, m.line);
        return;
    }
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
    mount: (m: FileMatch) => hitRow(m, hitLabel(searchPath, m.path)),
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
  // Read AFTER activateBrowser, so the id recorded is the tab the bar opens over.
  // Every production door arrives with an active files tab, so this read is it.
  searchOwnerID = getActiveTabId();
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
  searchOwnerID = "";
  shell?.cancel();
  barEl.classList.add("hidden");
  resultsEl.classList.add("hidden");
  resultsEl.replaceChildren();
  lastMatches = [];
  $.fbList.classList.remove("hidden");
  $.findBtn.setAttribute("aria-pressed", "false");
  shell?.setNote("");
}

/** Drop the search entirely: close it AND forget what was typed, so the next look at
 *  this browser is a directory listing rather than someone's old query. The GLOBS go
 *  with the query — they are part of the search the reader composed, and a stale
 *  `Exclude: node_modules` silently narrows a later one. */
export function resetFilesSearch(): void {
  closeFilesSearch();
  // Unconditional, because `closeFilesSearch` early-returns on an already-closed
  // bar: a closed bar must not be reachable by a later switch's teardown.
  searchOwnerID = "";
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

/** The first printable keystroke on a bound files tab opens the search bar and lands
 *  in it. No `preventDefault`, which is what makes the character arrive: the field is
 *  focused during this keydown, so the keypress/input that follow target it and dead
 *  keys and IME composition survive.
 *
 *  A bare `?` never reaches here, correctly: keys.ts calls `stopImmediatePropagation`
 *  for it so the shortcuts sheet opens, and this is one of those sibling listeners. */
export function handleFilesTypeAhead(e: KeyboardEvent): void {
  // The tab store answers which view is on screen; F2 and find-dispatch.ts already
  // ask it this way, and two mechanisms for one question drift.
  if (getActiveTabKind() !== "files") {
    return;
  }
  // With the bar open the field has focus, so the character lands there already.
  if (isOpen()) {
    return;
  }
  if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing) {
    return;
  }
  // `length === 1` excludes Enter, Escape, Tab, the arrows and the F-keys without
  // enumerating them; Space is a focused control's activation key.
  if (e.key.length !== 1 || e.key === " ") {
    return;
  }
  const active = document.activeElement;
  if (
    active instanceof HTMLInputElement ||
    active instanceof HTMLTextAreaElement ||
    active instanceof HTMLSelectElement ||
    (active instanceof HTMLElement &&
      (active.isContentEditable || active.closest("#shell-panel, dialog[open], .wt-root") !== null))
  ) {
    return;
  }
  // A bare keystroke can land in the window between the activation and the lazy
  // bind, where an unbound browser's search root answers "".
  if ((ctx?.getSearchPath() ?? "") === "") {
    return;
  }
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
