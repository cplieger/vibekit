// ---------------------------------------------------------------------------
// Find in Chat (Ctrl-F / Cmd-F): an in-chat message search overlay.
//
// Scoped to the ACTIVE chat's rendered messages (`#messages`).
//
// THE DOM_MESSAGE_CAP CLAIM THIS COMMENT USED TO MAKE WAS FALSE. It said the
// list is "DOM-capped at 50 nodes (see scroll.ts DOM_MESSAGE_CAP)"; no such
// constant has ever existed and scroll.ts never trims the DOM. The 50 was
// store-load.ts's PAGE SIZE — pagination, not eviction — and the wrong
// provenance propagated out of here into a design document before it was caught.
//
// The real blind spots are three, and they are why the enumeration moves
// server-side rather than being patched in this walker: non-resident pages;
// resident rows whose `content-visibility: auto` makes checkVisibility report
// false while rendering is skipped; and hidden or collapsed subtrees, which
// progressive collapse adds a third time.
//
// Native-find override policy (researched against 2024-2026 a11y/UX guidance):
//   - Overriding Ctrl-F is only acceptable because we provide an EQUIVALENT
//     in-page find: highlight, an aria-live "N of M" counter, next/prev
//     stepping, and scroll-into-view. Like native find, it never restructures
//     page content — it only wraps matches in <mark> and cleanly unwraps on
//     close.
//   - The override is NARROW: it only fires when the chat view is the active
//     context. Over the editor, shell, settings, git, or files views the
//     browser's native find is left untouched.
//   - Escape hatch: a SECOND Ctrl-F while our find field already has focus
//     falls through to the browser's native find (no preventDefault).
//   - No focus trap: Tab moves through the widget and back out to the page.
//     Escape closes and restores focus to wherever it was before opening.
//
// The keydown listener is registered from app.ts (the composition root) via
// handleFindHotkey — this module is a leaf (imports dom/scroll/reactive; nothing
// imports it except app.ts and the test), so there is no import cycle.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createPopup } from "@cplieger/ui-primitives/popup";
import type { PopupController } from "@cplieger/ui-primitives/popup";
import { $, byId } from "./dom.js";
import { setUserScrolledUp } from "./scroll.js";
import { runServerSearch, resetServerSearch } from "./chat-search.js";
import { getActiveId } from "./store.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { createSearchShell, searchIconButton } from "./search-shell.js";
import type { SearchShell } from "./search-shell.js";
import type { SearchHit } from "./chat-search.js";

const HIT_CLASS = "chat-find-hit";
const CURRENT_CLASS = "chat-find-hit-current";

const TEXT_NODE = 3;
const ELEMENT_NODE = 1;

// Debounce for live re-runs while the transcript changes (streaming): large
// enough to coalesce a burst of streamed chunks. The TYPING debounce is
// search-shell.ts's SEARCH_DEBOUNCE_MS — it was authored here and in
// files-search.ts with the same value and a comment in each saying so, which is
// what a shared constant is for.
const RERUN_DEBOUNCE_MS = 150;

// ---------------------------------------------------------------------------
// Pure helpers (exported for tests)
// ---------------------------------------------------------------------------

/** Format the match counter. 1-based for humans; "" when the query is empty,
 *  "No matches" when a non-empty query finds nothing. */
export function formatCount(total: number, current: number, query: string): string {
  if (query === "") {
    return "";
  }
  if (total === 0) {
    return "No matches";
  }
  return `${current + 1} of ${total}`;
}

/** True when `elem` (and thus its descendant text) should be searched. Prunes
 *  script/style, already-wrapped hits, structurally-hidden subtrees (hidden
 *  attr, .hidden class, aria-hidden, closed <details>), the live-streaming
 *  bubble (its markdown writer owns those nodes), and — in a real browser —
 *  anything hidden by CSS via Element.checkVisibility(). The structural checks
 *  work under happy-dom too (checkVisibility is optional and skipped there). */
function isSearchableElement(elem: Element): boolean {
  const tag = elem.tagName;
  if (tag === "SCRIPT" || tag === "STYLE" || tag === "MARK") {
    return false;
  }
  if (elem.hasAttribute("hidden") || elem.getAttribute("aria-hidden") === "true") {
    return false;
  }
  if (elem.classList.contains("hidden")) {
    return false;
  }
  // .streaming is set on the live assistant bubble AND live reasoning block.
  if (elem.classList.contains("streaming")) {
    return false;
  }
  if (tag === "DETAILS" && !(elem as HTMLDetailsElement).open) {
    return false;
  }
  const cv = (elem as { checkVisibility?: (opts?: unknown) => boolean }).checkVisibility;
  if (typeof cv === "function") {
    return cv.call(elem, {
      contentVisibilityAuto: true,
      visibilityProperty: true,
      opacityProperty: false,
    });
  }
  return true;
}

/** Wrap each occurrence of `needle` (length `needleLen`) in `node` with a
 *  `<mark>`, preserving original casing. Appends created marks to `out`. Only
 *  text nodes are touched — element nodes (and their listeners) are never
 *  disturbed.
 *
 *  `needle` arrives already folded when the search is case-INSENSITIVE, so the
 *  haystack is folded to match; a case-SENSITIVE search compares both verbatim.
 *  `needleLen` is passed separately because the slice below has to come out of
 *  the ORIGINAL text either way. */
function wrapMatchesInNode(
  node: Text,
  needleLen: number,
  needle: string,
  caseSensitive: boolean,
  out: HTMLElement[],
): void {
  const text = node.nodeValue ?? "";
  if (text === "") {
    return;
  }
  const hay = caseSensitive ? text : text.toLowerCase();
  let idx = hay.indexOf(needle);
  if (idx < 0) {
    return;
  }
  const frag = document.createDocumentFragment();
  let last = 0;
  while (idx >= 0) {
    if (idx > last) {
      frag.appendChild(document.createTextNode(text.slice(last, idx)));
    }
    const hit = el("mark", { className: HIT_CLASS }, text.slice(idx, idx + needleLen));
    frag.appendChild(hit);
    out.push(hit);
    last = idx + needleLen;
    idx = hay.indexOf(needle, last);
  }
  if (last < text.length) {
    frag.appendChild(document.createTextNode(text.slice(last)));
  }
  node.parentNode?.replaceChild(frag, node);
}

/** Replace a `<mark>` with a plain text node of its content and merge adjacent
 *  text nodes so the DOM returns to its pre-highlight shape. */
function unwrapMark(mark: HTMLElement): void {
  const parent = mark.parentNode;
  if (parent === null) {
    return;
  }
  parent.replaceChild(document.createTextNode(mark.textContent), mark);
  parent.normalize();
}

// ---------------------------------------------------------------------------
// FindEngine: owns match discovery, highlighting, and step state for one root.
// DOM-only (no scroll / overlay concerns) so it is unit-testable under happy-dom.
// ---------------------------------------------------------------------------

export class FindEngine {
  private readonly root: HTMLElement;
  private marks: HTMLElement[] = [];
  private current = -1;
  private lastQuery = "";

  constructor(root: HTMLElement) {
    this.root = root;
  }

  get total(): number {
    return this.marks.length;
  }

  get currentIndex(): number {
    return this.current;
  }

  get query(): string {
    return this.lastQuery;
  }

  /** Re-highlight `query` across the root. Clears any prior highlight first.
   *  Resets the current match to the first (index 0), or -1 when there are
   *  none. Returns the total match count. */
  search(query: string, caseSensitive = false): number {
    this.clear();
    this.lastQuery = query;
    if (query === "") {
      return 0;
    }
    const needle = caseSensitive ? query : query.toLowerCase();
    const marks: HTMLElement[] = [];
    for (const node of this.collectTextNodes()) {
      wrapMatchesInNode(node, query.length, needle, caseSensitive, marks);
    }
    this.marks = marks;
    this.current = marks.length > 0 ? 0 : -1;
    this.applyCurrentClass();
    return marks.length;
  }

  /** Remove all highlight marks and restore the original text nodes. */
  clear(): void {
    for (const mark of this.marks) {
      unwrapMark(mark);
    }
    // Defensive sweep in case an external DOM change stranded marks we no
    // longer track (e.g. a reconcile pass replaced a message element).
    for (const mark of [...this.root.querySelectorAll<HTMLElement>(`mark.${HIT_CLASS}`)]) {
      unwrapMark(mark);
    }
    this.marks = [];
    this.current = -1;
    this.lastQuery = "";
  }

  next(): void {
    if (this.marks.length === 0) {
      return;
    }
    this.current = (this.current + 1) % this.marks.length;
    this.applyCurrentClass();
  }

  prev(): void {
    if (this.marks.length === 0) {
      return;
    }
    this.current = (this.current - 1 + this.marks.length) % this.marks.length;
    this.applyCurrentClass();
  }

  /** Best-effort restore of the current index (used after a live re-run so the
   *  highlight doesn't jump back to match 1 on every streamed chunk). Clamped
   *  to the valid range; no-op when out of range. */
  setCurrent(index: number): void {
    if (index < 0 || index >= this.marks.length) {
      return;
    }
    this.current = index;
    this.applyCurrentClass();
  }

  currentMark(): HTMLElement | null {
    return this.marks[this.current] ?? null;
  }

  private applyCurrentClass(): void {
    for (let i = 0; i < this.marks.length; i++) {
      this.marks[i]?.classList.toggle(CURRENT_CLASS, i === this.current);
    }
  }

  private collectTextNodes(): Text[] {
    const out: Text[] = [];
    const visit = (node: Node): void => {
      if (node.nodeType === TEXT_NODE) {
        if ((node.nodeValue ?? "").length > 0) {
          out.push(node as Text);
        }
        return;
      }
      if (node.nodeType !== ELEMENT_NODE) {
        return;
      }
      if (!isSearchableElement(node as Element)) {
        return;
      }
      for (const child of node.childNodes) {
        visit(child);
      }
    };
    for (const child of this.root.childNodes) {
      visit(child);
    }
    return out;
  }
}

// ---------------------------------------------------------------------------
// Overlay controller (module singleton) — wires the FindEngine to the live
// #messages DOM, the search bar UI, keyboard handling, and the Ctrl-F hotkey.
// ---------------------------------------------------------------------------

let overlayEl: HTMLElement | null = null;
let shell: SearchShell | null = null;
let popup: PopupController | null = null;
let countEl: HTMLElement | null = null;
let engine: FindEngine | null = null;
let lastFocus: HTMLElement | null = null;
let rerunTimer: ReturnType<typeof setTimeout> | undefined;
let observer: MutationObserver | null = null;
/** Unsubscribe for the tab-change teardown, so a rebuilt module does not stack
 *  a second subscriber on the bus. */
let unsubTab: (() => void) | null = null;

/** Open state lives on the popup and NOWHERE ELSE.
 *
 *  It used to be a module boolean, and that is what made a tab switch leave the
 *  feature half-alive: hiding the view left the flag true, the MutationObserver
 *  connected, the <mark> elements welded into the transcript and every fold the
 *  search had opened still open, so returning to the chat re-revealed a search
 *  mid-flight. One source of truth means every close path — the ×, Escape
 *  anywhere in the document, an outside click, the trigger, a tab switch — runs
 *  the same teardown, because they all run through hide(). */
function isOpen(): boolean {
  return popup?.isOpen === true;
}

function prefersReducedMotion(): boolean {
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/** The bar's nav buttons: SVG glyphs, so `align-items: center` centres the ink
 *  rather than a line box. See search-shell.ts's searchIconButton for why a text
 *  `×` or `↑` cannot be centred by any authored value. */
function navButton(
  label: string,
  hint: string,
  icon: string,
  onClick: () => void,
): HTMLButtonElement {
  return searchIconButton("chat-find-btn", label, hint, icon, onClick);
}

function ensureBuilt(): void {
  if (overlayEl !== null) {
    return;
  }
  engine = new FindEngine($.messages);

  const count = el("span", {
    id: "chat-find-count",
    className: "chat-find-count",
    role: "status",
    "aria-live": "polite",
    "aria-atomic": "true",
  });
  countEl = count;

  const prevBtn = navButton("Previous match", "Previous (Shift+Enter)", ICON_CHEVRON_UP, () => {
    step(-1);
  });
  const nextBtn = navButton("Next match", "Next (Enter)", ICON_CHEVRON_DOWN, () => {
    step(1);
  });

  // The COUNTER and the prev/next pair are this surface's alone — a cursor has a
  // position in a document, which a ranked list does not — so they arrive
  // through `compose` as ordinary controls rather than becoming shell features.
  const built = createSearchShell<SearchHit[]>({
    id: "chat-find",
    regionClass: "chat-find uip-popup",
    inputClass: "chat-find-input",
    buttonClass: "chat-find-btn",
    caseClass: "chat-find-case",
    label: "Find in conversation",
    placeholder: "Find in chat\u2026",
    inputTitle: "Find in chat. Press Ctrl+F again to use the browser's find.",
    matchCase: true,
    closeButton: true,
    compose: ({ input, caseButton, closeButton }) => [
      input,
      count,
      el("div", { className: "chat-find-nav" }, caseButton, prevBtn, nextBtn, closeButton),
    ],
    query: async (query, ctx) => {
      if (engine === null) {
        return null;
      }
      // The LOCAL pass runs first and synchronously, so typing stays responsive
      // on what is already resident. The server pre-pass below is what makes the
      // count honest: the DOM walker prunes hidden and collapsed subtrees, so a
      // folded turn's hit is invisible to it until the reveal lands.
      applyEngine(() => {
        engine?.search(query, ctx.caseSensitive);
      });
      updateCounter(query);
      revealCurrent();
      return runServerSearch(getActiveId(), query, ctx.caseSensitive);
    },
    render: (hits, query) => {
      if (hits === null || hits.length === 0) {
        return;
      }
      // Re-run over the now-revealed DOM so the marks and the count cover the
      // turns the reveal opened.
      applyEngine(() => {
        engine?.search(query, shell?.caseSensitive ?? false);
      });
      updateCounter(query);
      revealCurrent();
    },
    onDismiss: () => {
      closeChatFind();
    },
    onSubmit: (shift) => {
      step(shift ? -1 : 1);
    },
  });
  shell = built;

  built.input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      step(1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      step(-1);
    }
  });

  // Anchor over the transcript (messages-wrap-outer is position:relative).
  //
  // HIDDEN BEFORE THE FIRST OPEN, and this is not cosmetic. The primitive only
  // writes `[hidden]` at the END of a leave, so a freshly built panel is visible
  // to the layout — and this one is `position: absolute` at `z-index: 60` over
  // the transcript with `opacity: 0`, so without this it would swallow every
  // click in its rectangle before search had ever been opened. pill-expand.ts
  // normalizes the same way for the same reason.
  built.region.hidden = true;
  byId("messages-wrap-outer").appendChild(built.region);
  overlayEl = built.region;

  // The reveal lifecycle is the primitive's: outside-click dismissal,
  // document-level Escape, the trigger's ARIA, single-open coordination, and the
  // is-open / is-leaving pair that lets BOTH legs animate. It drives the
  // `[hidden]` attribute rather than the `.hidden` class, which is what removes
  // the `display: none !important` the close could never animate out of.
  //
  // `isolateEscape: false` so the app's global Escape coordinator still sees the
  // key, the same contract pill-expand.ts keeps.
  //
  // The trigger is looked up defensively: the popup only needs it to write ARIA
  // and to exempt it from outside-click, and a fixture without the toolbar must
  // still be able to open the box.
  popup = createPopup(built.region, {
    trigger: document.getElementById("find-btn"),
    group: "app-search",
    isolateEscape: false,
    haspopup: "dialog",
    onOpen: () => {
      startObserving();
      // aria-pressed, not aria-expanded: find is a TOGGLE, not a disclosure of
      // this button's own content, and `.active` in this app means "this
      // singleton tab is active" (tabs.ts syncSidebarButtons owns that class).
      // 70-selection.css already styles `.icon-btn[aria-pressed="true"]`, so the
      // visual is the app's one selected treatment with no local rule.
      //
      // The primitive writes aria-expanded on the same element and that is left
      // in place: it is the truthful description of a revealable panel, no rule
      // matches it, and removing it would fight the primitive on every open.
      document.getElementById("find-btn")?.setAttribute("aria-pressed", "true");
      built.focus();
      built.run();
    },
    onClose: () => {
      teardown();
      document.getElementById("find-btn")?.setAttribute("aria-pressed", "false");
      const target = lastFocus;
      lastFocus = null;
      if (target?.isConnected === true) {
        target.focus();
      }
    },
  });

  // A tab switch CLOSES the search rather than hiding it. Subscribed here, once,
  // at build time; the unsubscribe exists so a rebuilt module cannot stack two.
  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, () => {
    closeChatFind();
  });
}

/** Everything the open state owns, released in one place.
 *
 *  Called from the popup's onClose, so it runs on EVERY close path. The mark
 *  unwrap and the server-search reset are the two that cannot be skipped: marks
 *  left behind are welded into the transcript for the rest of the session, and a
 *  skipped reset leaves the turns the search opened open, permanently
 *  rearranging a transcript as a side effect of having searched it. */
function teardown(): void {
  stopObserving();
  shell?.cancel();
  if (rerunTimer !== undefined) {
    clearTimeout(rerunTimer);
    rerunTimer = undefined;
  }
  applyEngine(() => {
    engine?.clear();
  });
  resetServerSearch(getActiveId());
  updateCounter("");
}

/** Run a DOM-mutating engine op with the transcript observer disconnected, so
 *  our own <mark> writes and class toggles don't re-trigger the live-re-run
 *  observer (MutationObserver callbacks are async, so a boolean guard would
 *  race; disconnect/reconnect is deterministic). */
function applyEngine(fn: () => void): void {
  const wasObserving = observer !== null;
  if (wasObserving) {
    stopObserving();
  }
  try {
    fn();
  } finally {
    if (wasObserving && isOpen()) {
      startObserving();
    }
  }
}

function step(dir: 1 | -1): void {
  if (engine === null || shell === null) {
    return;
  }
  // If the query changed since the last search (fast type-then-Enter), search
  // first — that lands on the first match, matching native find's behaviour.
  if (engine.query !== shell.value) {
    shell.run();
    return;
  }
  applyEngine(() => {
    if (dir === 1) {
      engine?.next();
    } else {
      engine?.prev();
    }
  });
  updateCounter(shell.value);
  revealCurrent();
}

/** Paint the counter for `query`.
 *
 *  The query is a PARAMETER rather than a read of the box, so a count can never
 *  describe a search other than the one that produced it. */
function updateCounter(query: string): void {
  if (countEl === null || engine === null) {
    return;
  }
  countEl.textContent = formatCount(engine.total, engine.currentIndex, query);
  const noResults = query !== "" && engine.total === 0;
  overlayEl?.classList.toggle("chat-find-no-results", noResults);
}

function revealCurrent(): void {
  if (engine === null) {
    return;
  }
  const mark = engine.currentMark();
  if (mark === null) {
    return;
  }
  // Freeze the auto-scroll controller so a streaming turn doesn't yank the
  // view back to the bottom while the user reads a match.
  setUserScrolledUp(true);
  const scrollFn = (mark as { scrollIntoView?: (o?: ScrollIntoViewOptions) => void })
    .scrollIntoView;
  if (typeof scrollFn === "function") {
    scrollFn.call(mark, {
      block: "center",
      inline: "nearest",
      behavior: prefersReducedMotion() ? "auto" : "smooth",
    });
  }
}

function startObserving(): void {
  if (observer !== null) {
    return;
  }
  observer = new MutationObserver(() => {
    scheduleRerun();
  });
  observer.observe($.messages, { childList: true, subtree: true, characterData: true });
}

function stopObserving(): void {
  observer?.disconnect();
  observer = null;
}

/** The transcript changed (streaming, a new turn, a chat switch). Re-run the
 *  search so the counter stays honest, preserving the current index and NOT
 *  scrolling (the user isn't stepping). */
function scheduleRerun(): void {
  if (rerunTimer !== undefined) {
    clearTimeout(rerunTimer);
  }
  rerunTimer = setTimeout(() => {
    rerunTimer = undefined;
    if (!isOpen() || engine === null || shell === null) {
      return;
    }
    const prevIndex = engine.currentIndex;
    const query = shell.value;
    applyEngine(() => {
      engine?.search(query, shell?.caseSensitive ?? false);
      engine?.setCurrent(prevIndex);
    });
    updateCounter(query);
  }, RERUN_DEBOUNCE_MS);
}

function openFindInChat(): void {
  ensureBuilt();
  if (popup === null) {
    return;
  }
  if (!popup.isOpen) {
    lastFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  }
  // show() on an already-open popup is a no-op reveal, so re-focus and re-run
  // here rather than in onOpen alone: the toolbar button and the hotkey both
  // reach an open box and both should land the caret in it.
  popup.show();
  shell?.focus();
  shell?.run();
}

/** Close the transcript search, running the full teardown.
 *
 *  Exported because the close is no longer only this module's business: the tab
 *  store's switch and the app's Escape coordinator both need it, and a caller
 *  that hid the box instead would leave the observer, the marks and the folds
 *  behind. Idempotent — the popup's hide() is a no-op when already closed. */
export function closeChatFind(): void {
  popup?.hide();
}

/** Toggle the transcript search. What the toolbar button means.
 *
 *  The button used to call the OPEN path, so a second click re-focused,
 *  re-selected and re-ran the search of a box that was already open — a control
 *  that looks like a toggle and is not. `files-search.ts` already had this shape
 *  and `tabs.ts`'s toggleSingleton is the same idea for a whole view. */
export function toggleChatFind(): void {
  if (!chatFindActiveContext()) {
    return;
  }
  if (isOpen()) {
    closeChatFind();
    return;
  }
  openFindInChat();
}

/** True when the chat transcript is the active context: the chat view is
 *  visible and focus is not inside the shell terminal panel. When false, the
 *  browser's native find is left untouched. */
function chatFindActiveContext(): boolean {
  const chatView = document.getElementById("chat-view");
  if (chatView === null || chatView.classList.contains("hidden")) {
    return false;
  }
  const active = document.activeElement;
  if (active instanceof Element && active.closest("#shell-panel") !== null) {
    return false;
  }
  return true;
}

function findInputFocused(): boolean {
  return shell !== null && document.activeElement === shell.input;
}

/** Global Ctrl-F / Cmd-F handler, registered on document from app.ts via
 *  find-dispatch. Opens (or refocuses) the in-chat find widget when the chat
 *  view is active. A second Ctrl-F while the find field already has focus falls
 *  through to the browser's native find (the escape hatch).
 *
 *  The HOTKEY opens rather than toggles, deliberately: a second Ctrl-F is the
 *  escape hatch to native find, so making it close would spend the app's only
 *  a11y justification for overriding the chord. The BUTTON toggles
 *  (toggleChatFind) because a button that only ever opens is not a toggle. */
export function handleFindHotkey(e: KeyboardEvent): void {
  if (e.key.toLowerCase() !== "f" || !(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) {
    return;
  }
  // Escape hatch: let the browser's native find open on a repeat press.
  if (isOpen() && findInputFocused()) {
    return;
  }
  if (!chatFindActiveContext()) {
    return;
  }
  e.preventDefault();
  openFindInChat();
}

/** @internal Test seam: whether the transcript search is open. */
export function _isChatFindOpen(): boolean {
  return isOpen();
}
