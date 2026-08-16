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
import { $, byId } from "./dom.js";
import { setUserScrolledUp } from "./scroll.js";
import { runServerSearch, resetServerSearch } from "./chat-search.js";
import { getActiveId } from "./store.js";

const HIT_CLASS = "chat-find-hit";
const CURRENT_CLASS = "chat-find-hit-current";

const TEXT_NODE = 3;
const ELEMENT_NODE = 1;

// Debounce for search-as-you-type and for live re-runs while the transcript
// changes (streaming). Small enough to feel instant, large enough to coalesce
// a burst of keystrokes / streamed chunks.
const TYPE_DEBOUNCE_MS = 90;
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
let inputEl: HTMLInputElement | null = null;
let countEl: HTMLElement | null = null;
let caseBtn: HTMLButtonElement | null = null;
let engine: FindEngine | null = null;
/** The match-case toggle's state, shared by BOTH halves of the search: the DOM
 *  walker that highlights and the server pre-pass that enumerates. Persists
 *  across open/close — it is a preference about how the reader searches, not
 *  state belonging to one query. */
let caseSensitive = false;
let isOpen = false;
let lastFocus: HTMLElement | null = null;
let typeTimer: ReturnType<typeof setTimeout> | undefined;
let rerunTimer: ReturnType<typeof setTimeout> | undefined;
let observer: MutationObserver | null = null;

function prefersReducedMotion(): boolean {
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

function navButton(
  label: string,
  glyph: string,
  hint: string,
  onClick: () => void,
): HTMLButtonElement {
  const btn = el(
    "button",
    {
      type: "button",
      className: "chat-find-btn",
      "aria-label": label,
      title: hint,
      tabindex: "0",
    },
    glyph,
  ) as HTMLButtonElement;
  btn.addEventListener("click", onClick);
  return btn;
}

function ensureBuilt(): void {
  if (overlayEl !== null) {
    return;
  }
  engine = new FindEngine($.messages);

  const input = el("input", {
    id: "chat-find-input",
    type: "text",
    className: "chat-find-input",
    placeholder: "Find in chat\u2026",
    "aria-label": "Find in conversation",
    autocomplete: "off",
    autocapitalize: "off",
    spellcheck: "false",
    enterkeyhint: "search",
    title: "Find in chat. Press Ctrl+F again to use the browser's find.",
  }) as HTMLInputElement;

  const count = el("span", {
    id: "chat-find-count",
    className: "chat-find-count",
    role: "status",
    "aria-live": "polite",
    "aria-atomic": "true",
  });

  // Match case. A latched toggle, so it carries aria-pressed rather than
  // relying on the tint alone.
  const caseToggle = navButton("Match case", "Aa", "Match case", () => {
    setCaseSensitive(!caseSensitive);
  });
  caseToggle.classList.add("chat-find-case");
  caseToggle.setAttribute("aria-pressed", caseSensitive ? "true" : "false");
  caseBtn = caseToggle;

  const prevBtn = navButton("Previous match", "\u2191", "Previous (Shift+Enter)", () => {
    step(-1);
  });
  const nextBtn = navButton("Next match", "\u2193", "Next (Enter)", () => {
    step(1);
  });
  const closeBtn = navButton("Close find", "\u00d7", "Close (Esc)", () => {
    closeFindInChat();
  });

  const nav = el("div", { className: "chat-find-nav" }, caseToggle, prevBtn, nextBtn, closeBtn);
  const overlay = el(
    "div",
    {
      id: "chat-find",
      className: "chat-find hidden",
      role: "search",
      "aria-label": "Find in conversation",
    },
    input,
    count,
    nav,
  );

  input.addEventListener("input", () => {
    if (typeTimer !== undefined) {
      clearTimeout(typeTimer);
    }
    typeTimer = setTimeout(() => {
      typeTimer = undefined;
      runSearch(input.value);
    }, TYPE_DEBOUNCE_MS);
  });

  input.addEventListener("keydown", (e: KeyboardEvent) => {
    switch (e.key) {
      case "Enter":
        e.preventDefault();
        step(e.shiftKey ? -1 : 1);
        break;
      case "ArrowDown":
        e.preventDefault();
        step(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        step(-1);
        break;
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        closeFindInChat();
        break;
    }
  });

  // Anchor over the transcript (messages-wrap-outer is position:relative).
  byId("messages-wrap-outer").appendChild(overlay);
  overlayEl = overlay;
  inputEl = input;
  countEl = count;
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
    if (wasObserving && isOpen) {
      startObserving();
    }
  }
}

/** Flip the match-case toggle and re-run.
 *
 *  The re-run is FORCED, because `step()` decides whether to re-search by
 *  comparing the query STRING to the last one and flipping this changes neither.
 *  It also deliberately does not preserve the current index the way
 *  `scheduleRerun` does: the match set itself changed, so a position in the old
 *  one means nothing. */
function setCaseSensitive(on: boolean): void {
  caseSensitive = on;
  caseBtn?.setAttribute("aria-pressed", on ? "true" : "false");
  if (inputEl !== null) {
    runSearch(inputEl.value);
  }
}

function runSearch(query: string): void {
  if (engine === null) {
    return;
  }
  // The SERVER pre-pass runs first and reveals the turns holding hits, because
  // the DOM walker below prunes hidden and collapsed subtrees — so a folded
  // turn's hit is invisible to it until the fold is lifted. Enumerating in the
  // DOM alone was a silent miss on non-resident pages, on rows whose
  // `content-visibility: auto` reports invisible, and on anything folded.
  //
  // The local pass still runs unconditionally rather than waiting: it keeps
  // typing responsive on what IS resident, and the reveal simply widens what it
  // can see when it lands a moment later.
  applyEngine(() => {
    engine?.search(query, caseSensitive);
  });
  updateCounter();
  revealCurrent();

  const chatID = getActiveId();
  void runServerSearch(chatID, query, caseSensitive)
    .then((hits) => {
      if (!isOpen || inputEl?.value !== query) {
        return; // superseded by newer typing, or the overlay closed
      }
      if (hits.length === 0) {
        return;
      }
      // Re-run over the now-revealed DOM so the marks and the count cover the
      // turns the reveal opened.
      applyEngine(() => {
        engine?.search(query, caseSensitive);
      });
      updateCounter();
      revealCurrent();
    })
    .catch((e: unknown) => {
      console.warn("[find] server search failed", e);
    });
}

function step(dir: 1 | -1): void {
  if (engine === null || inputEl === null) {
    return;
  }
  // If the query changed since the last search (fast type-then-Enter), search
  // first — that lands on the first match, matching native find's behaviour.
  if (engine.query !== inputEl.value) {
    runSearch(inputEl.value);
    return;
  }
  applyEngine(() => {
    if (dir === 1) {
      engine?.next();
    } else {
      engine?.prev();
    }
  });
  updateCounter();
  revealCurrent();
}

function updateCounter(): void {
  if (countEl === null || engine === null || inputEl === null) {
    return;
  }
  countEl.textContent = formatCount(engine.total, engine.currentIndex, inputEl.value);
  const noResults = inputEl.value !== "" && engine.total === 0;
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
    if (!isOpen || engine === null || inputEl === null) {
      return;
    }
    const prevIndex = engine.currentIndex;
    applyEngine(() => {
      engine?.search(inputEl?.value ?? "", caseSensitive);
      engine?.setCurrent(prevIndex);
    });
    updateCounter();
  }, RERUN_DEBOUNCE_MS);
}

function openFindInChat(): void {
  ensureBuilt();
  if (overlayEl === null || inputEl === null) {
    return;
  }
  if (!isOpen) {
    lastFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    overlayEl.classList.remove("hidden");
    isOpen = true;
    startObserving();
  }
  inputEl.focus();
  inputEl.select();
  runSearch(inputEl.value);
}

function closeFindInChat(): void {
  if (!isOpen || overlayEl === null) {
    return;
  }
  stopObserving();
  if (typeTimer !== undefined) {
    clearTimeout(typeTimer);
    typeTimer = undefined;
  }
  if (rerunTimer !== undefined) {
    clearTimeout(rerunTimer);
    rerunTimer = undefined;
  }
  applyEngine(() => {
    engine?.clear();
  });
  // Turns opened BY SEARCH re-fold; turns the reader opened by hand carry a
  // persisted override and are left alone. A search must not permanently
  // rearrange the transcript as a side effect of having been used.
  resetServerSearch(getActiveId());
  overlayEl.classList.add("hidden");
  isOpen = false;
  updateCounter();
  const target = lastFocus;
  lastFocus = null;
  if (target?.isConnected === true) {
    target.focus();
  } else {
    $.promptInput.focus();
  }
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
  return inputEl !== null && document.activeElement === inputEl;
}

/** Global Ctrl-F / Cmd-F handler, registered on document from app.ts. Opens
 *  (or refocuses) the in-chat find widget when the chat view is active. A
 *  second Ctrl-F while the find field already has focus falls through to the
 *  browser's native find (the escape hatch). */
/** Open the transcript search from a control rather than the hotkey.
 *
 *  Ctrl+F was the ONLY way in, which made a whole feature undiscoverable — and
 *  on a tablet with no keyboard, unreachable. The toolbar button calls this.
 *  Guarded the same way the hotkey is: search only means something over a
 *  visible transcript. */
export function openChatFind(): void {
  if (!chatFindActiveContext()) {
    return;
  }
  openFindInChat();
}

export function handleFindHotkey(e: KeyboardEvent): void {
  if (e.key.toLowerCase() !== "f" || !(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) {
    return;
  }
  // Escape hatch: let the browser's native find open on a repeat press.
  if (isOpen && findInputFocused()) {
    return;
  }
  if (!chatFindActiveContext()) {
    return;
  }
  e.preventDefault();
  openFindInChat();
}
