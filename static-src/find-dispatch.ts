// ---------------------------------------------------------------------------
// "Find" belongs to the ACTIVE TAB — and so does the toolbar button that opens
// it.
//
// The chord had two meanings already: open a find, and on a second press hand
// the key back to the browser's native find. Every tab kind's answer is now one
// table (DESTINATION below) rather than a switch with a default, because the
// default was where the last dead door hid: it answered "the transcript" for
// every kind it did not name, so `/settings` and `/run/{id}` kept a visible
// magnifier whose click reached find-in-chat, met its hidden-view guard and did
// nothing at all.
//
// THE BUTTON COMES THROUGH HERE TOO, and that is what the table is for. It used
// to call openChatFind directly, so on a files or editor tab it ran
// find-in-chat's context guard, found the chat view hidden, and returned.
// Routing it here makes the button and the chord the same decision, which is also
// why they cannot drift: `toggleFindForActiveTab`, `handleFindKey` and
// `findAvailableForActiveTab` all read one table.
//
// THE FOUR PAGE DESTINATIONS ARE POPUPS, LIKE THE TRANSCRIPT'S. They used to be
// permanent in-flow boxes (docs, History) or hand-authored fields inside a panel
// toolbar (the two git tabs), so this dispatcher had a `focus` verb for them and
// no way to close one. They share search-popup.ts now, so every destination
// answers the same three questions — open, toggle, is the caret in it — and the
// dispatcher stopped needing a second vocabulary for half of them.
//
// Keyed on the tab, not the view. The tab store already knows which tab is
// active and what kind it is (getActiveTabKind), so reading it here is reading
// the answer rather than inferring it from which view element happens to be
// unhidden. It reads the TabSpec's kind rather than the route's, because an
// editor tab's kind is "editor" while its route's is "file" — a binding keyed on
// the route would be a second vocabulary for one question.
//
// Neither escape hatch lives here. Each destination owns its own second-press
// fall-through, so this function never calls preventDefault: whether a press is
// consumed is the receiving find's judgement, and it is the a11y justification
// for overriding the key at all. Destinations may DECLINE — the editor over a
// diff pane, an image or rendered markdown, where a line number means nothing and
// the browser's own find is the better tool; and the git view's Sources tab,
// which lists forge accounts rather than a filterable inventory. Both then fall
// through to the transcript's handler, which declines in turn because the chat
// view is hidden, so native find opens. A `none` destination does not even offer
// the chord to that handler: the answer is already known, and routing through it
// would imply the page had an opinion.
//
// A module rather than an inline listener in app.ts so the routing itself is
// testable; app.ts still owns the registration, and nothing but app.ts and the
// test imports this.
// ---------------------------------------------------------------------------

import { getActiveTabKind } from "./tabs.js";
import type { TabKind } from "./tabs.js";
import { handleFindHotkey, toggleChatFind } from "./find-in-chat.js";
import { handleFindInFilesHotkey, toggleFilesSearch } from "./files-search.js";
import { handleEditorFindHotkey, toggleEditorFind, editorFindAvailable } from "./editor-find.js";
import { pageFind } from "./find-registry.js";
import type { FindKind } from "./find-registry.js";

/** Where a tab kind's find lives. */
type FindDestination =
  /** The transcript popup (find-in-chat.ts). */
  | "transcript"
  /** The in-flow buffer bar (editor-find.ts), which declines per surface. */
  | "editor"
  /** The in-flow recursive search panel (files-search.ts). */
  | "files"
  /** A registered page popup (search-popup.ts, via find-registry.ts). */
  | "page"
  /** Nothing to search or filter on this page at all. */
  | "none";

/**
 * Every tab kind's answer, in one table.
 *
 * A `Record<TabKind, …>` rather than a switch with a default, and that is the
 * fix for a dead door this module was written to remove and then left half open:
 * the default branch answered "the transcript" for every kind it did not name, so
 * `/settings` and `/run/{id}` — which have no search of any kind — kept a visible
 * magnifier whose click reached find-in-chat, met its hidden-view guard, and did
 * nothing. Exhaustiveness is now the compiler's job: a new tab kind is a build
 * error here rather than a button that silently does nothing on it.
 *
 * The `none` members are a decision, not a gap. Settings has four panels and its
 * own deep-link-to-one-control mechanism (`settings-highlight.ts`, which records
 * why it refused a search box: the ids the panels already carry ARE the index). A
 * run view is one run's node tree, read top to bottom.
 */
const DESTINATION: Readonly<Record<TabKind, FindDestination>> = {
  chat: "transcript",
  plan: "transcript",
  editor: "editor",
  files: "files",
  docs: "page",
  history: "page",
  git: "page",
  run: "none",
  settings: "none",
};

/** Search or filter, for the destinations that own the answer themselves.
 *
 *  The three built-in finds all SEARCH: each reaches past what is on screen — the
 *  transcript's enumeration is server-side over the whole conversation, the file
 *  browser's is a recursive grep over the tree, and the editor's scans a buffer of
 *  which the viewport shows a fraction. A page destination is asked instead
 *  (`PageFind.kind`), because a sub-tabbed page can be one on one tab and the
 *  other on the next. `none` still carries a value so the type stays total; it is
 *  never rendered, because nothing is offered. */
const BUILTIN_KIND: Readonly<Record<FindDestination, FindKind>> = {
  transcript: "search",
  editor: "search",
  files: "search",
  page: "filter",
  none: "filter",
};

/** The active tab's destination. No tab open at all reads as the transcript,
 *  which is what the app shows then. */
function destination(): FindDestination {
  const kind = getActiveTabKind();
  return kind === null ? "transcript" : DESTINATION[kind];
}

/** Route Ctrl-F / Cmd-F to the find that belongs to the active tab. Registered
 *  on document in the capture phase, so the browser's native find can be
 *  pre-empted before it opens. */
export function handleFindKey(e: KeyboardEvent): void {
  switch (destination()) {
    case "editor":
      // The editor's find declines over a non-source surface, and the fall
      // through to find-in-chat then declines too (the chat view is hidden), so
      // native find gets the key.
      if (handleEditorFindHotkey(e)) {
        return;
      }
      handleFindHotkey(e);
      return;
    case "files":
      handleFindInFilesHotkey(e);
      return;
    case "page":
      if (openRegistered(e)) {
        return;
      }
      handleFindHotkey(e);
      return;
    case "none":
      // Nothing here to search, so the chord is the browser's. Not even offered
      // to the transcript's handler: it would decline anyway (the chat view is
      // hidden), and routing through it would imply this page has an opinion.
      return;
    default:
      handleFindHotkey(e);
  }
}

/** Toggle the find that belongs to the active tab. What `#find-btn` means. */
export function toggleFindForActiveTab(): void {
  switch (destination()) {
    case "editor":
      toggleEditorFind();
      return;
    case "files":
      toggleFilesSearch();
      return;
    case "page":
      pageFind(getActiveTabKind() ?? "")?.toggle();
      return;
    case "none":
      return;
    default:
      toggleChatFind();
  }
}

/** What the toolbar's magnifier should paint for the active tab.
 *
 *  ONE lookup answering both halves, because both come from the same table read
 *  and a caller that asked twice could paint a funnel on a page whose box is a
 *  search. `kind` is meaningful only where `available` is true; on a page with no
 *  find at all the control is not drawn, so its glyph is not a question.
 *
 *  The button used to be painted unconditionally with a fixed magnifier, so on an
 *  editor tab showing a diff, an image or rendered markdown — and on `/settings`,
 *  `/run/{id}` and the git view's Sources tab — it was a control that did nothing,
 *  and on `/docs` and the git panels it promised a search over a box that only
 *  filters. Read inside a `@cplieger/reactive` effect (app.ts), so the signals the
 *  answer depends on — the editor's mode, the git sub-tab — re-run it themselves. */
export function findAffordanceForActiveTab(): { available: boolean; kind: FindKind } {
  // Read the registry FIRST, whatever the destination. `pageFind` is what
  // subscribes a caller's effect to registration, and reading it only inside the
  // `page` branch left that effect with no dependency on the registry on any
  // boot where the active tab was a chat — so a page registering a moment later
  // could not repaint the button it had already been painted absent on.
  const find = pageFind(getActiveTabKind() ?? "");
  const dest = destination();
  if (dest === "page") {
    if (find === undefined) {
      return { available: false, kind: BUILTIN_KIND.page };
    }
    return { available: find.available?.() ?? true, kind: find.kind() };
  }
  const available = dest === "editor" ? editorFindAvailable() : dest !== "none";
  return { available, kind: BUILTIN_KIND[dest] };
}

/** The shared chord guard. Each destination re-checks it, so the dispatcher's
 *  own pre-checks below use the same test rather than a second spelling. */
function isFindChord(e: KeyboardEvent): boolean {
  return e.key.toLowerCase() === "f" && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey;
}

/** Open a registered page popup, consuming the chord only when it accepted.
 *
 *  A second press from INSIDE the open box is not consumed: that is the escape
 *  hatch to the browser's own find, the same one every other destination keeps,
 *  and it is why overriding the chord is defensible at all.
 *
 *  Synchronous by necessity: `preventDefault` cannot be called from a promise
 *  callback, because by then the browser has already opened its own find. The
 *  registry (find-registry.ts) is how a lazily-loaded page becomes reachable
 *  synchronously without this module importing it. */
function openRegistered(e: KeyboardEvent): boolean {
  if (!isFindChord(e)) {
    return false;
  }
  const find = pageFind(getActiveTabKind() ?? "");
  if (find === undefined || find.focused()) {
    return false;
  }
  const opened = find.open();
  if (opened) {
    e.preventDefault();
  }
  return opened;
}
