// ---------------------------------------------------------------------------
// Find in Chat (Ctrl-F / Cmd-F): an in-chat message search overlay.
//
// Scoped to the ACTIVE chat's rendered messages (`#messages`).
//
// THE DOM_MESSAGE_CAP CLAIM THIS COMMENT USED TO MAKE WAS FALSE. It said the
// list is "DOM-capped at 50 nodes (see scroll.ts DOM_MESSAGE_CAP)"; no such
// constant has ever existed and scroll.ts never trims the DOM. The 50 was a
// message CAP on one page — pagination, not eviction, and not even the budget
// that cuts a page (store-load.ts's is in bytes) — and the wrong provenance
// propagated out of here into a design document before it was caught.
//
// The WALKER itself is find-engine.ts now, shared with the editor's find over a
// diff pane or rendered markdown — the same problem, one implementation. What
// stays here is the transcript's own half: the server pre-pass, the counter, the
// streaming re-run, and the popup.
//
// The real blind spots are three, and they are why the enumeration moves
// server-side rather than being patched in that walker: non-resident pages;
// resident content whose `content-visibility: auto` makes checkVisibility report
// false while rendering is skipped — the prose rows AND the four cards that hold
// a transcript's mass (css/14-tools.css), so most of it; and hidden or collapsed
// subtrees, which progressive collapse adds a third time.
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
// The keydown listener arrives through find-dispatch.ts from app.ts (the composition
// root). Nothing below that imports this module, so its own imports cannot cycle.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createPopup } from "@cplieger/ui-primitives/popup";
import type { PopupController } from "@cplieger/ui-primitives/popup";
import { $, byId } from "./dom.js";
import { jumpTo, onTranscriptMutate } from "./scroll.js";
import {
  runServerSearch,
  resetServerSearch,
  revealHitTurn,
  searchHitTotal,
} from "./chat-search.js";
import { getActive, getActiveId } from "./store.js";
import { blockElement } from "./messages-blocks.js";
import { loadMessages } from "./store-load.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { createSearchShell, searchIconButton } from "./search-shell.js";
import { FindEngine, formatCount } from "./find-engine.js";
import type { SearchShell } from "./search-shell.js";
import type { SearchHit } from "./chat-search.js";

// Debounce for live re-runs while the transcript changes (streaming): large
// enough to coalesce a burst of streamed chunks. The TYPING debounce is
// search-shell.ts's SEARCH_DEBOUNCE_MS — it was authored here and in
// files-search.ts with the same value and a comment in each saying so, which is
// what a shared constant is for.
const RERUN_DEBOUNCE_MS = 150;

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

/** The walker's root: the ACTIVE transcript view. Resolved by the multiplexer's
 *  own class contract rather than an import — messages.ts sits above this
 *  module in the graph (it injects the search reveal builder), so reaching it
 *  statically would cycle. Falls back to the multiplexer itself for fixtures
 *  (and the boot instant) where no view is mounted. */
function findRoot(): HTMLElement {
  return $.messages.querySelector<HTMLElement>(":scope > .transcript-view.is-active") ?? $.messages;
}
/** Unregister for the live re-run's ride on the transcript's shared
 *  MutationObserver (scroll.ts owns the one observer); null while closed. */
let unobserveTranscript: (() => void) | null = null;
/** Engine ops in flight whose own DOM writes must not re-trigger the re-run.
 *  A counter rather than a boolean so nested `applyEngine` calls stay safe. */
let engineWrites = 0;
/** Unsubscribe for the tab-change teardown, so a rebuilt module does not stack
 *  a second subscriber on the bus. */
let unsubTab: (() => void) | null = null;

// ---------------------------------------------------------------------------
// Server-hit navigation state.
//
// The hits themselves, kept so stepping can NAVIGATE them when the DOM walker
// marked nothing: every occurrence can sit inside collapsed delegate bodies,
// on a non-resident page, or in markdown the renderer never paints — and the
// counter already admits they exist ("N in chat"), so Enter going dead on
// them would be the overlay contradicting itself.
// ---------------------------------------------------------------------------

/** The last successful server answer, session-wide and in server order. A
 *  failed re-fetch keeps the previous list, matching chat-search.ts's rule for
 *  the reveal: a transient failure must not collapse the search under the
 *  reader. */
let serverHits: SearchHit[] = [];
/** Position in `serverHits` while stepping drives navigation; -1 = none. */
let hitCursor = -1;
/** One navigation in flight at a time: paging in a hit spans awaits, and a
 *  second Enter mid-flight would race the first's reveal and selection. */
let navBusy = false;

/** Open state lives on the popup and NOWHERE ELSE.
 *
 *  It used to be a module boolean, and that is what made a tab switch leave the
 *  feature half-alive: hiding the view left the flag true, the mutation
 *  registration active, the <mark> elements welded into the transcript and every fold the
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
  engine = new FindEngine(findRoot());

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
    regionClass: "chat-find search-pop uip-popup",
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
      if (hits !== null) {
        // A fresh answer replaces the navigable set and drops the cursor: a
        // position in the previous query's hits means nothing in this one. A
        // null (failed fetch) keeps the previous set, same as the reveal.
        serverHits = hits;
        hitCursor = -1;
      }
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
      // Re-root the walker on the ACTIVE transcript view: parked sibling views
      // hold other chats' text, which must stay out of the count, the marks and
      // the tab order (they are inert). The root is stable for the whole open —
      // a tab switch closes the search (below) — so a fresh engine per open is
      // the entire lifecycle. Falls back to the multiplexer for fixtures that
      // never mounted a view.
      if (engine !== null && engine.root !== findRoot()) {
        applyEngine(() => {
          engine?.clear();
        });
        engine = new FindEngine(findRoot());
      }
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

  // A tab switch CLOSES the search and FORGETS the query. Subscribed here, once,
  // at build time; the unsubscribe exists so a rebuilt module cannot stack two.
  //
  // Closing alone left the box pre-filled, and `openFindInChat` runs on open — so
  // the next chat's find opened holding the previous chat's query and immediately
  // searched a transcript that query was never typed against. Dropping it on the
  // switch and NOT on an ordinary close is the useful split: reopening the find on
  // the same chat still remembers what you were looking for, which is what the
  // browser's own find does.
  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, () => {
    closeChatFind();
    if (shell !== null) {
      shell.input.value = "";
    }
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
  serverHits = [];
  hitCursor = -1;
  resetServerSearch(getActiveId());
  updateCounter("");
}

/** Run a DOM-mutating engine op without our own <mark> writes and class toggles
 *  re-triggering the live re-run (the loop would be rerun → marks → rerun at the
 *  debounce's period). The shared observer delivers records on a microtask
 *  queued WHILE `fn` mutates, so releasing the guard one microtask after `fn`
 *  returns is deterministic where a synchronous flag would race the delivery —
 *  the same guarantee the disconnect/reconnect of a privately-owned observer
 *  used to give. */
function applyEngine(fn: () => void): void {
  engineWrites++;
  try {
    fn();
  } finally {
    queueMicrotask(() => {
      engineWrites--;
    });
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
  // No DOM mark anywhere, but the server found the text: stepping navigates
  // the server hits instead of dying on a counter that says "N in chat".
  // Enter-cycling with resident marks stays exactly what it was.
  if (engine.total === 0 && serverHits.length > 0) {
    stepServerHit(dir);
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
  const session = searchHitTotal();
  countEl.textContent = formatCount(engine.total, engine.currentIndex, query, session);
  // The no-results skin is a claim that the text is not in the conversation, so
  // the server's answer gates it too: a query whose only matches sit in collapsed
  // or non-resident content found something, and painting the box as a miss would
  // contradict the count beside it.
  const noResults = query !== "" && engine.total === 0 && session === 0;
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
  // view back to the bottom while the user reads a match — but only when the
  // jump actually leaves the live edge, which is `jumpTo`'s call to make.
  jumpTo(mark, {
    block: "center",
    inline: "nearest",
    behavior: prefersReducedMotion() ? "auto" : "smooth",
  });
}

// ---------------------------------------------------------------------------
// Server-hit navigation: the pipeline that makes a hit with no DOM mark
// reachable. Page the message in, reveal its turn (stub build included), open
// the delegate/reasoning chain over the matched block, re-walk, and select the
// nearest mark — or, when the match is not in rendered text at all, select the
// block itself and SAY so. Never a silent no-op.
// ---------------------------------------------------------------------------

/** How many older pages one navigation may fetch hunting for its message.
 *  Generous — at 50 messages a page this is 10000 messages — because the
 *  reader asked for exactly this hit; the cap only bounds a server that keeps
 *  claiming `has_more` without ever delivering the message. */
const HIT_PAGE_CAP = 200;

/** Below this excerpt similarity the best candidate mark is not credibly THE
 *  hit — the same text elsewhere in the block, not this occurrence — so
 *  navigation falls back to selecting the block rather than claiming a
 *  precision it does not have. Dice over word tokens; an honest match scores
 *  well above this even with markdown syntax stripped out of the rendering,
 *  and a wrong occurrence shares little beyond the needle itself. */
const SIM_FLOOR = 0.3;

/** How long the "look here" wash stays on a container the navigation selected
 *  (block selection and `message`-kind hits). Past the CSS animation so the
 *  class strip is a cleanup, not the visual cutoff; also the reduced-motion
 *  backstop, where `animationend` never fires (settings-highlight.ts sets the
 *  precedent). */
const FLASH_FALLBACK_MS = 2000;

function stepServerHit(dir: 1 | -1): void {
  if (navBusy || serverHits.length === 0) {
    return;
  }
  hitCursor =
    hitCursor === -1
      ? dir === 1
        ? 0
        : serverHits.length - 1
      : (hitCursor + dir + serverHits.length) % serverHits.length;
  const hit = serverHits[hitCursor];
  if (hit === undefined) {
    return;
  }
  navBusy = true;
  void navigateToHit(hit).finally(() => {
    navBusy = false;
  });
}

/** The counter line while server hits are being stepped: the position is in
 *  the SESSION-wide list, and the "in chat" suffix keeps the vocabulary the
 *  counter already uses for that figure. */
function serverHitCounter(): string {
  return `${String(hitCursor + 1)} of ${String(serverHits.length)} in chat`;
}

/** State the one thing navigation could not do, on the live-region counter.
 *  The next counter repaint replaces it, which is the right lifetime for a
 *  per-hit remark. */
function showHitNotice(suffix: string): void {
  if (countEl !== null) {
    countEl.textContent = `${serverHitCounter()} \u00b7 ${suffix}`;
  }
}

async function navigateToHit(hit: SearchHit): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "" || engine === null) {
    return;
  }
  if (!(await ensureHitResident(chatID, hit))) {
    if (isOpen()) {
      showHitNotice("could not be loaded");
    }
    return;
  }
  await revealHitTurn(chatID, hit);
  // Closed (or switched away) while paging in: the surface this would select
  // on is gone, and teardown already reset the reveal.
  if (!isOpen() || shell === null) {
    return;
  }
  const row = messageRowEl(hit.message_id);
  if (row === null) {
    showHitNotice("could not be shown");
    return;
  }
  // A `message` hit locates the message, not a span in it: container
  // navigation, scroll + brief highlight. Routing here is what makes the
  // ranker's segment_len division unreachable for this kind — its
  // segment_len is 0 by contract, and no zero-guard below has to know that.
  if (hit.segment_kind === "message") {
    selectContainer(row);
    if (countEl !== null) {
      countEl.textContent = serverHitCounter();
    }
    return;
  }
  const target = resolveSegmentEl(row, hit) ?? row;
  openDisclosureChain(row, target, hit);
  // Re-walk now that the chain is open: the marks inside it exist only after
  // the walker can see the text.
  let chosen = walkAndPick(target, hit);
  // Whether the second walk had a rendered frame to walk. A first-walk hit never
  // asks, and never reaches the notice below either.
  let rendered = true;
  if (chosen === -1) {
    // A miss can mean the text is not THERE, or that it was not RENDERED when the
    // walker ran. The four cards that carry a transcript's mass are
    // `content-visibility: auto` (css/14-tools.css), so their content is skipped
    // while off screen and the walker prunes it — and a hit the reader has not
    // scrolled to yet is exactly that. Scrolling to it is what renders it, so
    // navigate FIRST and ask once more before claiming the text is not there.
    jumpTo(target, { block: "center", inline: "nearest", behavior: "auto" });
    rendered = await nextRender();
    // Closed, or the transcript repainted this element away, while the frame
    // rendered. `shell` needs no re-check: it is assigned once at build.
    if (!isOpen() || !target.isConnected) {
      return;
    }
    chosen = walkAndPick(target, hit);
  }
  if (chosen === -1) {
    // Syntax-only match (link target, emphasis marker, fence info) or a best
    // candidate below the similarity floor: select the block and say why. With no
    // frame delivered the walk ran against content that may simply not be painted
    // yet — a hidden tab, or a paint past the ceiling — so the notice claims
    // "later", not "absent". Either way the block is selected, which is the part
    // that is true in both.
    selectContainer(target);
    showHitNotice(rendered ? "not in rendered text" : "not rendered yet");
    return;
  }
  applyEngine(() => {
    engine?.setCurrent(chosen);
  });
  updateCounter(shell.value);
  revealCurrent();
}

/** Walk the transcript again and pick the mark that is credibly THIS hit.
 *
 *  The walk is what makes marks exist inside content that has only just become
 *  visible — a disclosure chain the navigation opened, or a card a scroll brought
 *  on screen. -1 when nothing inside `target` is credibly the hit. */
function walkAndPick(target: HTMLElement, hit: SearchHit): number {
  applyEngine(() => {
    engine?.search(shell?.value ?? "", shell?.caseSensitive ?? false);
  });
  return pickNearestMark(target, hit);
}

/** Ceiling on the frame wait below. Four 60Hz frames: long enough that a busy
 *  frame does not lose the re-walk, short enough that the fallback is not itself
 *  a stall the reader can feel. */
const RENDER_WAIT_CEILING_MS = 64;

/** One RENDERED frame, or the ceiling, whichever lands first. `true` when a frame
 *  was actually delivered, which is what makes a following miss CONCLUSIVE.
 *
 *  The second callback is the first point after the previous frame was laid out,
 *  which is when a `content-visibility: auto` card a scroll just reached holds
 *  walkable text. THE TIMEOUT IS NOT BELT-AND-BRACES: a hidden page gets no
 *  animation frames, so the bare pair never settled inside `stepServerHit`'s
 *  `navBusy` latch, leaving find's next/prev inert until the tab came forward. */
function nextRender(): Promise<boolean> {
  return new Promise((resolve) => {
    const ceiling = setTimeout(() => {
      resolve(false);
    }, RENDER_WAIT_CEILING_MS);
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        clearTimeout(ceiling);
        resolve(true);
      });
    });
  });
}

/** Page older history in until the hit's message is resident. Bounded by the
 *  server's own `has_more`, a no-progress check, and a page cap. */
async function ensureHitResident(chatID: string, hit: SearchHit): Promise<boolean> {
  const resident = (): boolean =>
    getActive()?.messages.some((m) => m.id === hit.message_id) === true;
  for (let pages = 0; !resident(); pages++) {
    const session = getActive();
    if (session?.has_more !== true || session.id !== chatID || pages >= HIT_PAGE_CAP) {
      return false;
    }
    const oldest = session.messages[0]?.id;
    if (!(await loadMessages(chatID, oldest))) {
      return false;
    }
    // No progress: the server answered but the window's edge did not move, so
    // more requests would loop on the same answer.
    if (getActive()?.messages[0]?.id === oldest) {
      return false;
    }
  }
  return true;
}

/** The rendered row for a message id: reconcile keys message rows by id inside
 *  each turn card's body. */
function messageRowEl(messageID: string): HTMLElement | null {
  return findRoot().querySelector<HTMLElement>(
    `.turn-body > [data-reconcile-key="${CSS.escape(messageID)}"]`,
  );
}

/**
 * The rendered container of the hit's SEGMENT: a tool segment by its call's id
 * (`data-tool-id`, scoped to the row because one call can hold several slots), a text
 * or reasoning block through the renderer's own per-block map. Null — the caller falls
 * back to the message row — for a legacy blockless hit, for anything unmounted, and
 * for a mounted tool slot the row scope cannot see: a run card hosts every later
 * message's steps, so those sit in the FIRST message's row until a drop re-homes it.
 */
function resolveSegmentEl(row: HTMLElement, hit: SearchHit): HTMLElement | null {
  const bi = hit.block_index;
  if (bi === undefined) {
    return null;
  }
  const block = getActive()?.messages.find((m) => m.id === hit.message_id)?.blocks?.[bi];
  if (block === undefined) {
    return null;
  }
  if (hit.segment_kind === "tool_title" || hit.segment_kind === "tool_output") {
    const tid = block.tool_call_id ?? "";
    if (tid === "") {
      return null;
    }
    return row.querySelector<HTMLElement>(`[data-tool-id="${CSS.escape(tid)}"]`);
  }
  // content | reasoning: the block INDEX, keyed by message, because the mounted
  // window can start anywhere and a same-kind ordinal counted from 0 in the store
  // then names a different element. A subtree query cannot answer either: a
  // `.run-card` in this row can host another message's blocks at the same index.
  const stamped = blockElement(hit.message_id, bi);
  // A map can name an element that has LEFT the document, where the subtree query this
  // replaced could not; `navigateToHit` exits silently on one, so decline it here.
  if (stamped?.isConnected !== true) {
    return null;
  }
  // A top-level text block is stamped on its ROW — that is what a window drop
  // removes — so both shapes answer with the bubble, which is what every consumer
  // downstream flashes, walks and jumps to.
  return hit.segment_kind === "content"
    ? (stamped.querySelector<HTMLElement>(":scope > .message") ?? stamped)
    : stamped;
}

/**
 * Open every closed disclosure between the hit's container and its row, so the
 * walker can reach the text: reasoning `<details>` by the platform API, and
 * delegate boxes / tool groups by ACTIVATING their real header — the
 * disclosure controller behind it flips `aria-hidden` + `inert` synchronously,
 * and going through it keeps its state agreeing with the DOM. A tool_output
 * hit additionally opens its card's own disclosure, where the output body
 * lives (and is often first BUILT).
 */
function openDisclosureChain(row: HTMLElement, target: HTMLElement, hit: SearchHit): void {
  for (let cur: HTMLElement | null = target; cur !== null && cur !== row.parentElement;) {
    if (cur instanceof HTMLDetailsElement && !cur.open) {
      cur.open = true;
    }
    if (cur.classList.contains("subagent-block") && cur.classList.contains("collapsed")) {
      cur.querySelector<HTMLElement>(":scope > .subagent-header")?.click();
    }
    if (cur.classList.contains("tool-group") && cur.classList.contains("tool-group-collapsed")) {
      cur.querySelector<HTMLElement>(":scope > .tool-group-header")?.click();
    }
    if (cur === row) {
      break;
    }
    cur = cur.parentElement;
  }
  if (hit.segment_kind === "tool_output" && target.classList.contains("tool-call")) {
    const toggle = target.querySelector<HTMLElement>(".tool-disclosure");
    if (toggle?.getAttribute("aria-expanded") === "false") {
      toggle.click();
    }
  }
}

/**
 * The nearest-match ranking over the target's marks, as the engine index of
 * the winner (-1 = no credible mark). Excerpt similarity first — the server
 * matched raw markdown and the DOM holds rendered text, so ordinals cannot be
 * trusted across the two — then relative position (`offset / segment_len`
 * against the mark's offset in the target's text), ties to the lowest index.
 */
function pickNearestMark(target: HTMLElement, hit: SearchHit): number {
  const all = [...findRoot().querySelectorAll<HTMLElement>("mark.find-hit")];
  const excerptTokens = tokenSet(hit.excerpt);
  const offsets = markOffsets(target);
  const want = hit.offset / hit.segment_len;
  let best = -1;
  let bestSim = -1;
  let bestDist = Infinity;
  for (let i = 0; i < all.length; i++) {
    const mark = all[i];
    if (mark === undefined || !target.contains(mark)) {
      continue;
    }
    const at = offsets.marks.get(mark) ?? 0;
    const sim = dice(excerptTokens, tokenSet(contextAround(offsets.text, at, mark)));
    const dist = Math.abs(want - at / Math.max(1, offsets.text.length));
    if (sim > bestSim || (sim === bestSim && dist < bestDist)) {
      best = i;
      bestSim = sim;
      bestDist = dist;
    }
  }
  return bestSim >= SIM_FLOOR ? best : -1;
}

/** The target's full text in document order plus each mark's start offset in
 *  it. One walk serves every candidate. UTF-16 units rather than the server's
 *  runes — both sides of the position comparison are RATIOS, so the skew of a
 *  surrogate pair moves both numerators the same way. */
function markOffsets(target: HTMLElement): { text: string; marks: Map<HTMLElement, number> } {
  const marks = new Map<HTMLElement, number>();
  let text = "";
  const walk = (node: Node): void => {
    if (node.nodeType === Node.TEXT_NODE) {
      text += node.nodeValue ?? "";
      return;
    }
    if (node instanceof HTMLElement && node.matches("mark.find-hit")) {
      marks.set(node, text.length);
    }
    for (const child of node.childNodes) {
      walk(child);
    }
  };
  walk(target);
  return { text, marks };
}

/** The rendered-side counterpart of the server's excerpt: the text around the
 *  mark, at the same radius the server uses (searchExcerptRadius = 60). */
function contextAround(text: string, at: number, mark: HTMLElement): string {
  const len = mark.textContent.length;
  return text.slice(Math.max(0, at - 60), at + len + 60);
}

/** Word tokens for similarity: lowercased, split on anything that is not a
 *  letter or digit — which is what strips markdown syntax (`**`, backticks,
 *  link brackets) out of the comparison between raw and rendered text. */
function tokenSet(s: string): Set<string> {
  return new Set(
    s
      .toLowerCase()
      .split(/[^\p{L}\p{N}]+/u)
      .filter((t) => t !== ""),
  );
}

/** Dice coefficient over two token sets: 2·|A∩B| / (|A|+|B|). */
function dice(a: Set<string>, b: Set<string>): number {
  if (a.size === 0 || b.size === 0) {
    return 0;
  }
  let inter = 0;
  for (const t of a) {
    if (b.has(t)) {
      inter++;
    }
  }
  return (2 * inter) / (a.size + b.size);
}

/** Container selection: scroll it into view and flash the shared "look here"
 *  wash (24-find.css). The class comes off on animationend, with a timer as
 *  the reduced-motion backstop where the animation never runs. */
function selectContainer(target: HTMLElement): void {
  target.classList.add("find-target-flash");
  target.addEventListener(
    "animationend",
    () => {
      target.classList.remove("find-target-flash");
    },
    { once: true },
  );
  setTimeout(() => {
    target.classList.remove("find-target-flash");
  }, FLASH_FALLBACK_MS);
  jumpTo(target, {
    block: "center",
    inline: "nearest",
    behavior: prefersReducedMotion() ? "auto" : "smooth",
  });
}

function startObserving(): void {
  if (unobserveTranscript !== null) {
    return;
  }
  unobserveTranscript = onTranscriptMutate(() => {
    if (engineWrites > 0) {
      return; // our own mark writes; see applyEngine
    }
    scheduleRerun();
  });
}

function stopObserving(): void {
  unobserveTranscript?.();
  unobserveTranscript = null;
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
