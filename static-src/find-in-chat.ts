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
// The WALKER itself is find-engine.ts now, shared with the editor's find over a
// diff pane or rendered markdown — the same problem, one implementation. What
// stays here is the transcript's own half: the server pre-pass, the counter, the
// step list, the streaming re-run, and the popup.
//
// The real blind spots are three, and they are why the enumeration moves
// server-side rather than being patched in that walker: non-resident pages;
// resident rows whose `content-visibility: auto` makes checkVisibility report
// false while rendering is skipped; and hidden or collapsed subtrees, which
// progressive collapse adds a third time.
//
// So the server's answer is the STEP LIST as well as the count, and a list belongs
// to the TEXT IN THE BOX (`serverHitsQuery`, one writer). The DOM pass keeps two
// jobs — it highlights, and it is what a step lands on — and it is the whole step
// list when no answer is owned yet.
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
// imports it except app.ts, the History page's cross-chat handoff and the test),
// so there is no import cycle. The one door out of it, the run tab a
// workflow-step hit navigates to, is reached through a LAZY import, so that chunk
// stays lazy and the leaf claim still holds.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { createPopup } from "@cplieger/ui-primitives/popup";
import type { PopupController } from "@cplieger/ui-primitives/popup";
import { $, byId } from "./dom.js";
import { jumpTo, onTranscriptMutate } from "./scroll.js";
import { runServerSearch, resetServerSearch, revealHitTurn } from "./chat-search.js";
import { getActive, getActiveId } from "./store.js";
import { blockElement } from "./messages-blocks.js";
import { loadMessages } from "./store-load.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { createSearchShell, searchIconButton } from "./search-shell.js";
import { parseStepSubtask } from "./step-subtask.js";
import { FindEngine } from "./find-engine.js";
import { classify, cursorCount, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns, Tally } from "./textsearch/copy.js";
import type { SearchShell } from "./search-shell.js";
import type { Hit, SearchResult, SegmentKind } from "./wire/types.gen.js";

// Debounce for live re-runs while the transcript changes (streaming): large
// enough to coalesce a burst of streamed chunks. The TYPING debounce is
// search-shell.ts's SEARCH_DEBOUNCE_MS — it was authored here and in
// files-search.ts with the same value and a comment in each saying so, which is
// what a shared constant is for.
const RERUN_DEBOUNCE_MS = 150;

/** The transcript's unit nouns: a hit is a match, and the scan reads messages. */
const NOUNS: Nouns = {
  match: { one: "match", many: "matches" },
  scanned: { one: "message", many: "messages" },
};

/** The tally of no answer: nothing read, nothing matched, nothing cut. */
const NO_ANSWER: Tally = { scanned: 0, matched: 0, truncated: false };

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
// The step list: the SERVER's answer, and who it belongs to.
//
// Stepping walks the server's hits whenever there is an answer FOR THE TEXT IN
// THE BOX. That is the whole rule, and it replaced a gate that walked them only
// when the DOM had marked nothing — which left every hit the walker cannot see
// unreachable on any transcript holding one resident mark: collapsed delegate
// output, a non-resident page, a workflow step. The counter already admits those
// exist ("N in chat"), so Enter going dead on them was the overlay contradicting
// itself. The DOM pass keeps its other two jobs — it HIGHLIGHTS, and it is what a
// step LANDS on — and it is the whole step list when no server answer is owned.
// ---------------------------------------------------------------------------

/** The last successful server answer's list, in WIRE order. Counting reads this;
 *  the WALK reads `stepOrder`. A failed re-fetch keeps the previous list, matching
 *  chat-search.ts's rule for the reveal: a transient failure must not collapse the
 *  search under the reader. */
let serverHits: Hit[] = [];
/** That answer's tally, adopted in the same write as `serverHits` so the two
 *  cannot describe different answers. `matched` is the whole-chat count the list
 *  is cut against (cut iff it exceeds `serverHits.length`); `scanned` is how many
 *  messages were read; `truncated` is the scan's own reach, false for a chat read
 *  whole. */
let serverTally: Tally = NO_ANSWER;
/** The query whose answer is standing. WRITTEN IN EXACTLY ONE PLACE — `render`,
 *  when it adopts an answer — and read by four consumers: `step`, both of
 *  `updateCounter`'s branches, and the note.
 *
 *  It is not defensive dressing. The shell's `query` callback runs the DOM pass
 *  synchronously and RETURNS the fetch, so for the whole debounce-plus-round-trip
 *  window `engine.query === shell.value` holds while these hits still belong to
 *  the PREVIOUS query — and an Enter in that window would navigate to text the
 *  reader has already replaced, which on a chat with delegated work most likely
 *  means opening another tab and tearing this overlay down. */
let serverHitsQuery = "";
/** The walk order: `[...local, ...crossTab]`, each phase in wire order. A hit this
 *  client answers IN PLACE comes before one it answers by opening another view, so
 *  a reader stepping a chat with delegated work is not thrown out of the
 *  transcript every second press. */
let stepOrder: Hit[] = [];
/** `stepOrder[localCount]` is the first cross-tab hit, so the cursor reaching it
 *  IS the crossing. */
let localCount = 0;
/** The boundary sentence fires once per answer, not once per crossing: a reader
 *  who wraps round does not need telling twice. */
let announcedBoundary = false;
/** Position in `stepOrder` while stepping drives navigation; -1 = none. */
let hitCursor = -1;
/** One navigation in flight at a time: paging in a hit spans awaits, and a
 *  second Enter mid-flight would race the first's reveal and selection. */
let navBusy = false;
/** THIS client jumped to another tab from a hit, so the tab change about to
 *  arrive is ours. `navigateToHit` writes it (with `resumeKey`) immediately before
 *  the lazy import, because on success the switch tears the overlay down before
 *  anything else could; the `BUS_TAB_CHANGED` subscriber only READS it and clears
 *  it, which is why it stays a plain boolean rather than carrying the identity. */
let crossTabJump = false;
/** The hit the reader left on, as a `@cplieger/keyenc` join of
 *  `[message_id, block_index ?? -1, segment_kind, offset]` — an IDENTITY rather
 *  than the index, because the chat can grow while the reader is away and every
 *  index would move. Spent by the next open's re-run, in `render`, which clears it
 *  whether or not it resolved; a key naming no hit in the fresh `stepOrder` resets
 *  the cursor to -1 rather than throwing. */
let resumeKey = "";
/** A handoff asked the next answer to LAND on `resumeKey`'s hit rather than only
 *  restore the cursor to it: another surface found the conversation and counted
 *  its matches, so opening the box on the first DOM mark would leave the reader
 *  where a retyped query would have. Spent with `resumeKey`, in `render`. */
let landOnResume = false;

/** The walk order for one answer, partitioned ONCE by destination.
 *
 *  The key is `agent_subtask_id`, a property of the hit that never changes and the
 *  same predicate `navigateToHit` already routes on — the client's own ROUTING
 *  rather than a claim about what the transcript renders. So an INVOCATION hit,
 *  which is drawn in the transcript as the delegate's card, is phase 2 like every
 *  other hit carrying that id: that is today's routing and nothing here changes it.
 *
 *  `serverHits` is deliberately NOT sorted in place — every other consumer counts
 *  that list — and this runs once per answer rather than per press, so the cursor
 *  means the same thing across a re-render of the same answer. */
function buildStepOrder(hits: Hit[]): Hit[] {
  const isLocal = (h: Hit): boolean => (h.agent_subtask_id ?? "") === "";
  const local = hits.filter(isLocal);
  localCount = local.length;
  return [...local, ...hits.filter((h) => !isLocal(h))];
}

/** Where a cross-tab return resumes: the position of `resumeKey`'s hit in the
 *  FRESH `stepOrder`, or -1 when there is no key or the chat grew past it. Spends
 *  the key either way, so a stale identity cannot bind a later answer. */
function resumeIndex(): number {
  const key = resumeKey;
  resumeKey = "";
  if (key === "") {
    return -1;
  }
  return stepOrder.findIndex((h) => hitKey(h) === key);
}

/** The hit's identity, for the cross-tab resume. */
function hitKey(hit: Hit): string {
  return join(hit.message_id, String(hit.block_index ?? -1), hit.segment_kind, String(hit.offset));
}

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
  const built = createSearchShell<SearchResult>({
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
    // `noteClass` is not optional here: the shell defaults it to `search-note`,
    // so without it every `.chat-find-note` rule matches nothing and the note
    // keeps intrinsic height while empty.
    note: true,
    noteClass: "chat-find-note",
    compose: ({ input, caseButton, closeButton, note }) => [
      el(
        "div",
        { className: "chat-find-row" },
        input,
        count,
        el("div", { className: "chat-find-nav" }, caseButton, prevBtn, nextBtn, closeButton),
      ),
      note,
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
      // Not while a handoff's landing is pending: the first mark is not where that
      // open is going, and a scroll to it would be undone by the answer.
      if (!landOnResume) {
        revealCurrent();
      }
      return runServerSearch(getActiveId(), query, ctx.caseSensitive);
    },
    render: (result, query) => {
      // THE ONE WRITER of the standing answer. Seven values move together or none
      // does, which is what makes "a step list belongs to the text in the box" a
      // property of the code rather than a rule to remember: the list, its tally,
      // its walk order, the phase boundary, the cursor and the OWNERSHIP.
      //
      // A null is a failed fetch, and every one of them STANDS — the previous
      // answer, its tally, its cursor, its reveal and its ownership — so a
      // transient failure never un-says something true. What keeps that safe is
      // the ownership itself: every reader below is gated on it, so an answer left
      // standing across a query change is unspendable rather than wrong, and the
      // moment the reader types that text back it is spendable again against the
      // same list.
      // Whether this answer is the one a handoff asked to land on: the flag is
      // spent with the key it rides on, and a key the fresh list does not hold
      // (the chat moved on) lands nowhere rather than on a guessed neighbour.
      let land = false;
      if (result !== null) {
        serverHits = result.matches;
        serverTally = result;
        stepOrder = buildStepOrder(result.matches);
        hitCursor = resumeIndex();
        land = landOnResume;
        landOnResume = false;
        serverHitsQuery = query;
        announcedBoundary = false;
      }
      const owned = serverHitsQuery === query;
      // ABOVE the early return: refining after a cut is exactly the gesture the
      // note invites, so a query refined to zero hits has to clear the sentence
      // rather than leave it beside a counter reading "No matches".
      // OWNERSHIP-gated for the failed-fetch case on the same path: the standing
      // answer may belong to text the reader has since replaced, and the note
      // describes THAT answer.
      shell?.setNote(owned ? cutNote() : "");
      if (result === null || result.matches.length === 0) {
        // Repaint even with nothing to walk: the OWNERSHIP moved in this call, and
        // the counter's session figure and its no-results skin are both gated on
        // it — so an empty answer is where a genuine miss earns that skin, rather
        // than on every keystroke while the fetch was still in flight.
        updateCounter(query);
        return;
      }
      // Re-run over the now-revealed DOM so the marks and the count cover the
      // turns the reveal opened.
      applyEngine(() => {
        engine?.search(query, shell?.caseSensitive ?? false);
      });
      updateCounter(query);
      const target = land ? stepOrder[hitCursor] : undefined;
      if (target !== undefined) {
        landOnHit(target);
        return;
      }
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
  //
  // ONE EXCEPTION, and it is the return trip: a switch THIS box caused by stepping
  // onto a cross-tab hit keeps the query, so coming back and pressing Ctrl-F
  // re-runs it and `render` restores the cursor to the hit the reader left on.
  // Activating a run or a delegate page does not change the active CHAT, so the
  // kept query still belongs to the transcript it was typed against — which is the
  // exact condition the clear exists to protect.
  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, () => {
    // Consumed here, one-shot, BEFORE the close: teardown leaves it standing (it
    // cannot know which switch it is running under), so this is the only reader.
    const ours = crossTabJump;
    crossTabJump = false;
    closeChatFind();
    if (shell !== null && !ours) {
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
  // Everything that describes the CURRENT open, and nothing that describes the
  // journey out of it: `crossTabJump` and `resumeKey` are deliberately LEFT
  // STANDING. The ordering makes that unavoidable rather than a special case — the
  // BUS_TAB_CHANGED subscriber calls closeChatFind(), whose popup onClose runs this
  // function, so a teardown that cleared them would delete the resume state in the
  // same turn the jump set it. Recording them AFTER the close instead is refused:
  // the subscriber has to READ the flag to decide whether to keep the query, so it
  // must already be set when the subscriber runs.
  serverHits = [];
  serverTally = NO_ANSWER;
  stepOrder = [];
  localCount = 0;
  announcedBoundary = false;
  serverHitsQuery = "";
  hitCursor = -1;
  // Unlike `resumeKey`, the landing describes the open it was asked for: a
  // handoff arms it AFTER the switch's own close has run, so nothing here can
  // delete it early, and a box the reader closed before its answer landed owes
  // the next open a restored cursor, not a jump.
  landOnResume = false;
  resetServerSearch();
  shell?.setNote("");
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
  // THE SPINE: an owned server answer IS the step list, whatever the walker
  // managed to mark. The DOM pass still highlights and is still what a step lands
  // on, and it is the whole list when nothing is owned — an in-flight window, or a
  // first query whose fetch failed.
  //
  // The two shapes this replaced are both unsound, and the suite already contained
  // the fixture that proves it. `serverHits.length > engine.total` cannot detect
  // divergence: 2 marks in one message against 2 hits, one of them in a message
  // that is not resident, reads as `2 > 2` = false while a hit is unreachable. And
  // a UNION needs a join the client cannot make honestly — the server's offset
  // indexes raw source, a mark's indexes rendered text, the per-block counts differ
  // wherever markdown or a diff window intervenes, and `pickNearestMark` is a
  // ranking with a similarity floor rather than an identity — so it would
  // double-visit and report a total that is neither list's.
  if (serverHits.length > 0 && serverHitsQuery === shell.value) {
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
  const owned = serverHitsQuery === query;
  // TWO GRAMMARS, one owner. Stepping the session list prints a position IN that
  // list; everything else prints the DOM position with the session figure beside
  // it. One owner is what keeps the position stable across the re-run a streaming
  // turn triggers — a second writer would repaint the DOM form under a reader who
  // is walking the server's.
  if (stepsOwnedList(query)) {
    countEl.textContent = steppedCounter();
    overlayEl?.classList.remove("chat-find-no-results");
    return;
  }
  // The session tally is ownership-gated, for the same reason the note is: until
  // the answer belongs to the text in the box it describes a query the reader has
  // replaced. The no-results SKIN takes the same condition, because "unknown" and
  // "zero" are different states and flashing the miss skin on every keystroke
  // would be a worse lie than a stale number.
  const session = owned ? serverTally : NO_ANSWER;
  countEl.textContent = domCounter(engine, query, session);
  // The skin is a claim that the text is NOWHERE in the conversation, so it needs
  // an owned answer saying so: while the fetch is in flight the honest state is
  // "not known yet", and painting a miss on every keystroke of a query the server
  // has not answered is a worse lie than a stale number.
  const noResults = query !== "" && engine.total === 0 && owned && serverTally.matched === 0;
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

/** The sentence that explains the partition, once per answer, on the press that
 *  first crosses into phase 2. Without it the reader has to infer the rule from
 *  being thrown out of the transcript. */
const BOUNDARY_NOTICE = "the rest are in delegate pages and run tabs";

function stepServerHit(dir: 1 | -1): void {
  if (navBusy || stepOrder.length === 0) {
    return;
  }
  hitCursor =
    hitCursor === -1
      ? dir === 1
        ? 0
        : stepOrder.length - 1
      : (hitCursor + dir + stepOrder.length) % stepOrder.length;
  const hit = stepOrder[hitCursor];
  if (hit === undefined) {
    return;
  }
  landOnHit(hit);
}

/** Land on `hit`, the way every press does.
 *
 *  The SYNCHRONOUS landing first. Without it every press takes the async pipeline
 *  — a reveal, an on-demand turn build, possibly a rAF pair — and `navBusy` DROPS
 *  an Enter arriving mid-flight, so holding Enter would drop presses on hits whose
 *  text is already on screen, where today it steps briskly. */
function landOnHit(hit: Hit): void {
  if (landInPlace(hit)) {
    return;
  }
  navBusy = true;
  void navigateToHit(hit).finally(() => {
    navBusy = false;
  });
}

/** Land on `hit` with no await and no re-walk, or answer false and let the async
 *  pipeline run.
 *
 *  It succeeds only where nothing has to be fetched, revealed or opened: the hit is
 *  this client's to answer in place, it names a span rather than a whole message,
 *  its element is mounted, and one of the marks ALREADY inside that element is
 *  credibly this occurrence. Everything else is the existing path, unchanged. */
function landInPlace(hit: Hit): boolean {
  if (engine === null || shell === null) {
    return false;
  }
  if ((hit.agent_subtask_id ?? "") !== "" || hit.segment_kind === "message") {
    return false;
  }
  const target = resolveSegmentEl(hit);
  if (target === null) {
    return false;
  }
  const walkTarget = narrowToolTarget(target, hit) ?? target;
  const chosen = pickNearestMark(walkTarget, hit);
  if (chosen === -1) {
    return false;
  }
  applyEngine(() => {
    engine?.setCurrent(chosen);
  });
  // Through the counter's own owner rather than painting the line here: the
  // position it prints is `updateCounter`'s first branch, and one writer is what
  // keeps a landing from disagreeing with the next repaint.
  updateCounter(shell.value);
  revealCurrent();
  return true;
}

/** The counter over the DOM marks: the cursor among them, with the server's
 *  whole-chat count beside it only when it exceeds them. An empty walk reads as
 *  the empty state the tally decides: nothing anywhere, or matched and not shown
 *  here (every hit inside a pruned subtree or a non-resident page). */
function domCounter(eng: FindEngine, query: string, tally: Tally): string {
  if (query === "") {
    return "";
  }
  if (eng.total === 0) {
    return emptyNote(
      classify({ matched: tally.matched, shown: 0, truncated: tally.truncated }),
      NOUNS,
    );
  }
  return cursorCount(eng.currentIndex + 1, eng.total, exceeding(tally.matched, eng.total));
}

/** The counter while the server's list is being stepped: the position in THAT
 *  list, with the whole-chat count beside it only when the list was cut short of
 *  it. The same gate `domCounter` applies to the marks, so both grammars show the
 *  second figure exactly when it says more than the first. */
function steppedCounter(): string {
  return cursorCount(
    hitCursor + 1,
    serverHits.length,
    exceeding(serverTally.matched, serverHits.length),
  );
}

/** `matched` when it exceeds the navigable list's length, else nothing to add. */
function exceeding(matched: number, total: number): number | undefined {
  return matched > total ? matched : undefined;
}

/** The note under a CUT answer: how much of the chat's count the list holds and
 *  how far the scan reached. "" for an answer the list holds whole, because a
 *  sentence restating the counter is noise (24-find.css's rule for this line). */
function cutNote(): string {
  return serverTally.matched > serverHits.length
    ? scanNote(serverTally, serverHits.length, NOUNS)
    : "";
}

/** Whether the STEPPED grammar is the honest thing to print for `query`: a cursor
 *  is standing in the server's list, and that list is the answer for `query`.
 *
 *  ONE owner, read by `updateCounter`'s stepped branch and by `paintHitPosition`
 *  below, because all three print that same grammar and only one of them may decide
 *  when it is true. */
function stepsOwnedList(query: string): boolean {
  return hitCursor >= 0 && serverHits.length > 0 && serverHitsQuery === query;
}

/** Paint the stepped position, with `·`-separated suffixes for whatever this press
 *  also has to say — the phase boundary, the destination, or what navigation could
 *  not do.
 *
 *  GATED HERE rather than at the call sites, and it closes a defect the deferred
 *  diff introduced: this function and `showHitNotice` (which is one call to it) are
 *  the two that WRITE, so a guard beside one await would leave the next caller
 *  unprotected. A landing now spans up to `DEFERRED_DIFF_WAIT_MS`, which is long
 *  enough for the reader to retype or for a fresh answer to land underneath it, and
 *  either way this press's position describes a list nobody is walking — so it
 *  paints nothing, exactly as `updateCounter` prints no stepped position there.
 *  The BOX rather than a parameter, because the question is whether the standing
 *  answer is still the one the reader has. */
function paintHitPosition(...suffixes: (string | (() => string))[]): void {
  if (countEl === null || !stepsOwnedList(shell?.value ?? "")) {
    return;
  }
  // A suffix may arrive as a THUNK so that producing it happens after the gate: an
  // argument is evaluated before the call, so `takeBoundaryNotice()` spelled as a
  // value spends its once-per-answer latch even on a press this function then
  // refuses, and the crossing sentence is lost for good. Unreachable while both
  // crossing callers are synchronous and immediately post-step, and made structural
  // rather than left to that coincidence, because this file's landings now span
  // awaits. The alternative — a second entry point carrying its own copy of the gate
  // — is the two-spellings-of-one-rule shape the file avoids elsewhere.
  const parts = suffixes.map((s) => (typeof s === "function" ? s() : s)).filter((s) => s !== "");
  countEl.textContent = [steppedCounter(), ...parts].join(" \u00b7 ");
}

/** The boundary sentence if this press is the crossing, "" otherwise. Spends the
 *  once-per-answer latch, so it composes into whatever paint the press was going to
 *  make rather than needing a paint of its own — the crossing is always a cross-tab
 *  press, whose own paint the switch is about to tear down. */
function takeBoundaryNotice(): string {
  // `localCount === 0` is not the crossing: with no phase 1 there is nothing to
  // cross FROM, and "the rest are elsewhere" would be false about an answer whose
  // every hit is elsewhere.
  if (announcedBoundary || localCount === 0 || hitCursor !== localCount) {
    return "";
  }
  announcedBoundary = true;
  return BOUNDARY_NOTICE;
}

/** State the one thing navigation could not do, on the live-region counter.
 *  The next counter repaint replaces it, which is the right lifetime for a
 *  per-hit remark.
 *
 *  Every caller is POST-AWAIT, which is why it is one call to `paintHitPosition`
 *  rather than a second writer: the ownership gate there covers this one by
 *  construction. */
function showHitNotice(suffix: string): void {
  paintHitPosition(suffix);
}

async function navigateToHit(hit: Hit): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "" || engine === null) {
    return;
  }
  // A WORKFLOW STEP's hit goes to the RUN TAB, the only surface that renders it: the
  // transcript's dispatcher drops a step's blocks, so there is no DOM segment for
  // `resolveSegmentEl` to find. Skipping `ensureHitResident` and `revealHitTurn` is the
  // point — the destination is another tab. The predicate is the renderer's own
  // `parseStepSubtask`, so a malformed `wf:` id falls to the DELEGATE branch below.
  const step = parseStepSubtask(hit.agent_subtask_id ?? "");
  if (step !== null) {
    // Painted BEFORE the open, with the DESTINATION and, on the crossing, the
    // boundary sentence: on success the tab switch tears the overlay down
    // (BUS_TAB_CHANGED -> closeChatFind), so anything said afterwards is said to
    // nobody, and on a refused open this is the accurate position for a reader
    // still on the transcript. Through the existing role=status counter, so it is
    // spoken as well as shown.
    paintHitPosition(takeBoundaryNotice, "opening the run tab");
    // BOTH resume values, immediately before the lazy import, because the switch
    // this open causes is what tears the overlay down: `teardown` therefore has to
    // EXEMPT them (it runs in the same turn), and the subscriber only reads the
    // flag. Written even on a path that may fail — a stale one-shot flag costs a
    // kept query, where a missing one costs the reader their place.
    crossTabJump = true;
    resumeKey = hitKey(hit);
    try {
      // Lazily imported, the shape `messages-blocks.ts` uses for the run card's own
      // opener: a static import would pull `exec-view/**`, `run-exec-source` and
      // `actions/runs` into the main bundle for a branch that is rarely taken.
      const { openRunView } = await import("./run-view.js");
      // Name "" so the tab factory derives the label from the run store — find has
      // no better one than the factory does.
      void openRunView(step.workflowID, "", chatID, step.nodePath);
    } catch {
      if (isOpen()) {
        showHitNotice("could not be opened");
      }
    }
    return;
  }
  // A DELEGATE's hit goes to that delegate's TAB, for the same reason and by the same
  // route. Every non-empty subtask id that is not a step is a delegate, malformed `wf:`
  // ids included, which is where the renderer sends them too.
  const subtask = hit.agent_subtask_id ?? "";
  if (subtask !== "") {
    paintHitPosition(takeBoundaryNotice, "opening the delegate's page");
    crossTabJump = true;
    resumeKey = hitKey(hit);
    try {
      const { openSubagentView } = await import("./subagent-view.js");
      void openSubagentView(chatID, subtask);
    } catch {
      if (isOpen()) {
        showHitNotice("could not be opened");
      }
    }
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
  // THE TWO TURN-LEVEL KINDS RESOLVE FROM THE TURN, AHEAD OF THE ROW LOOKUP,
  // because neither renders inside the message row: the attachment pills live in
  // the turn HEADER and the failure reason in the card-level `.turn-notice`. A
  // tier-3 STUB turn has no `.turn-body` at all, so on the very turns where the
  // notice is the whole rendered content the row lookup below returns null and
  // this arm would never be reached.
  //
  // AFTER residency and the reveal, though: a turn paged out of the window has no
  // card to select, and paging it in is what makes one exist.
  if (hit.segment_kind === "turn_failure" || hit.segment_kind === "attachment") {
    const card = turnCardEl(hit.turn_message_id);
    if (card === null) {
      // The same sentence the row path answers, for the same reason: the surface
      // this hit names is not on screen and navigation cannot invent it.
      showHitNotice("could not be shown");
      return;
    }
    const region =
      hit.segment_kind === "turn_failure"
        ? card.querySelector<HTMLElement>(":scope > .turn-notice")
        : card.querySelector<HTMLElement>(":scope > .turn-header .turn-req-attachments");
    // No disclosure chain and no row requirement: both regions sit in the card's
    // resting state, and a turn whose text `turnFailureText` suppresses has no
    // notice at all — which is what the walk's own miss notice then reports.
    await landOrNotice(region ?? card, hit, region !== null);
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
    updateCounter(shell.value);
    return;
  }
  const target = resolveSegmentEl(hit) ?? row;
  openDisclosureChain(row, target, hit);
  // A preview-less diff card loads its diff FROM THE OPEN, so the text this hit
  // names does not exist until the bulk lands. Bounded, and `null` for every other
  // shape — including a card already showing its preview, which therefore takes no
  // await at all and behaves exactly as it did.
  const arriving = awaitDeferredDiff(target, hit);
  if (arriving !== null) {
    await arriving;
    // Closed, or the transcript repainted this element away, while the bulk was in
    // flight: the same guard the residency and frame awaits already take.
    if (!isOpen() || !target.isConnected) {
      return;
    }
  }
  // Narrow AFTER the chain opened, never inside `resolveSegmentEl`: the regions a
  // tool kind narrows to reach their state two ways, and both are late. The input
  // `<pre>` and the denial block are BUILT by `detailsBody` on the first open, so
  // on a closed card neither exists; `.tool-output` is part of the card shell, so
  // the element exists from the mount but holds no text until that open. Either
  // way, narrowing before the open answers a mark-free element for every closed
  // card — the common case, and exactly the case the narrowing is for.
  const narrowed = narrowToolTarget(target, hit) ?? narrowRowTarget(row, hit);
  await landOrNotice(narrowed ?? target, hit, narrowed !== null);
}

/** Walk `walkTarget`, select the mark that is credibly THIS hit, or select the
 *  region and SAY what happened. The shared tail of every resolution path — the
 *  row path and the turn-level one — so the two cannot answer a miss differently.
 *
 *  `narrowed` is the narrowing OUTCOME rather than the selector, because
 *  `missNotice` reads it and one owner of the query is the point. */
async function landOrNotice(walkTarget: HTMLElement, hit: Hit, narrowed: boolean): Promise<void> {
  // A landing may not MOVE the reader once this press has stopped owning the list,
  // and that is a wider rule than the counter's: every effect below is a write the
  // reader SEES — `jumpTo` and `revealCurrent` scroll, `selectContainer` flashes,
  // `engine.setCurrent` moves the mark — so a landing arriving up to
  // `DEFERRED_DIFF_WAIT_MS` after the reader retyped would jump the transcript to a
  // hit for a query they have left, with the counter (correctly) saying nothing
  // about it. Silent motion is worse than a stale sentence, not better.
  //
  // The SAME predicate the counter uses, because it is the same question — is the
  // standing answer still the one the reader has — and one owner is what stops the
  // two disagreeing about whether a press is live. Every legitimate entry passes it:
  // `landOnHit` is reached only through `stepOrder[hitCursor]`, which is `undefined`
  // for a cursor of -1 under `noUncheckedIndexedAccess`, so a cursor is always
  // standing in the owned list by the time this runs.
  if (!stepsOwnedList(shell?.value ?? "")) {
    return;
  }
  // Re-walk now that the chain is open: the marks inside it exist only after
  // the walker can see the text.
  let chosen = walkAndPick(walkTarget, hit);
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
    jumpTo(walkTarget, { block: "center", inline: "nearest", behavior: "auto" });
    rendered = await nextRender();
    // Closed, the transcript repainted this element away, or the reader moved off
    // this answer, while the frame rendered. The ownership term is the entry guard's,
    // re-asked because this is the file's own idiom at an await boundary (`isOpen()`
    // is checked at each one for the same reason) and because the two effects left
    // below — the flash and the mark — are the ones the entry guard was protecting.
    // `shell` needs no re-check: it is assigned once at build.
    if (!isOpen() || !walkTarget.isConnected || !stepsOwnedList(shell?.value ?? "")) {
      return;
    }
    chosen = walkAndPick(walkTarget, hit);
  }
  if (chosen === -1) {
    // Syntax-only match (link target, emphasis marker, fence info), a best
    // candidate below the similarity floor, or text that is genuinely elsewhere:
    // select the NARROWED region and say why, so the flash lands on the diff
    // rather than on the whole card.
    selectContainer(walkTarget);
    showHitNotice(missNotice(hit, rendered, narrowed));
    return;
  }
  applyEngine(() => {
    engine?.setCurrent(chosen);
  });
  updateCounter(shell?.value ?? "");
  revealCurrent();
}

/** Walk the transcript again and pick the mark that is credibly THIS hit.
 *
 *  The walk is what makes marks exist inside content that has only just become
 *  visible — a disclosure chain the navigation opened, or a card a scroll brought
 *  on screen. -1 when nothing inside `target` is credibly the hit. */
function walkAndPick(target: HTMLElement, hit: Hit): number {
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
async function ensureHitResident(chatID: string, hit: Hit): Promise<boolean> {
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

/** The rendered TURN CARD for a turn's opening message id, which is what the two
 *  turn-level kinds resolve from.
 *
 *  One query mirroring `messageRowEl`, keyed the same way one level up: a turn card
 *  is the OUTER reconcile's element and its key is the turn's opening message id,
 *  which is exactly the `turn_message_id` already on the wire. Null when that turn
 *  is not mounted in the active view. */
function turnCardEl(turnMessageID: string): HTMLElement | null {
  return findRoot().querySelector<HTMLElement>(
    `.turn[data-reconcile-key="${CSS.escape(turnMessageID)}"]`,
  );
}

/** The rendered container of the hit's SEGMENT, from the renderer's own per-block map. NOT scoped
 *  to the hit's row: a run card holds every later message's steps, so a mounted card can sit in an
 *  EARLIER message's row. Every kind the transcript MOUNTS is stamped, so null means nothing is
 *  mounted here and the caller falls back to the row.
 *
 *  It does not answer for every segment kind, and four whole populations resolve ELSEWHERE rather
 *  than through this map. A hit carrying a subtask id is answered by another TAB (a `wf:` id by the
 *  run tab, everything else by the delegate's page). `turn_failure` and `attachment` are answered by
 *  the TURN CARD, ahead of the row lookup, because neither renders inside the row. A `message`-kind
 *  hit names the message rather than a span in it. And `plan` resolves to the row and is narrowed
 *  from there by `narrowRowTarget`, because a plan carries no block index either.
 *
 *  A TOOL kind's answer here is the whole `.tool-call`; the region inside it is
 *  `narrowToolTarget`'s, after the disclosure has opened.
 *
 *  It answers the whole `.tool-call` for a tool block, and per-kind narrowing does
 *  NOT belong here: the regions those kinds narrow to are built or filled by the
 *  card's first open, so an arm here would answer a mark-free element for every
 *  closed card. `narrowToolTarget` runs after `openDisclosureChain` instead. */
function resolveSegmentEl(hit: Hit): HTMLElement | null {
  const bi = hit.block_index;
  if (bi === undefined) {
    return null;
  }
  const block = getActive()?.messages.find((m) => m.id === hit.message_id)?.blocks?.[bi];
  if (block === undefined) {
    return null;
  }
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

/** Which region inside a tool card each tool kind's text is RENDERED in. A kind
 *  with no row here narrows to nothing and the card is walked whole, which is
 *  what every non-tool kind wants.
 *
 *  `tool_title` and `tool_disclosed` are ONE rendered position, because
 *  `disclosedClaim` replaces the card's title in the display — so exactly one of
 *  the two is ever on screen per card and both walk `.tool-title`. */
const TOOL_DIFF_PREVIEW = ".tool-diff-preview";
const TOOL_TARGET: Partial<Record<SegmentKind, string>> = {
  tool_title: ".tool-title",
  tool_disclosed: ".tool-title",
  tool_diff: TOOL_DIFF_PREVIEW,
  tool_denial: ".tool-denial",
  tool_input: ".tool-input",
  tool_output: ".tool-output",
};

/** The region inside the hit's tool card that holds THIS kind's text, or null
 *  when there is none to narrow to.
 *
 *  Without it six kinds would share one walk target — the whole `.tool-call` —
 *  and `pickNearestMark` ranks every mark inside it, so a query matching both a
 *  call's arguments and its output could land a `tool_input` hit on the output's
 *  mark, and a diff's own notice could never fire while any other credible mark
 *  existed in the card. */
function narrowToolTarget(target: HTMLElement, hit: Hit): HTMLElement | null {
  const selector = TOOL_TARGET[hit.segment_kind];
  if (selector === undefined) {
    return null;
  }
  const card = target.closest<HTMLElement>(".tool-call") ?? target;
  return card.querySelector<HTMLElement>(selector);
}

/** The region inside the hit's ROW that holds a MESSAGE-level kind's text, or null
 *  when there is none. Only `plan` has one: `mountPlan` appends the plan card to
 *  the message's own wrap — which IS the row `messageRowEl` selects — and finds it
 *  back with this same selector.
 *
 *  Here rather than in `resolveSegmentEl`, which opens with a block-index guard a
 *  message-level hit can never pass: an arm there would be unreachable dead code,
 *  and its named test would fail with nothing pointing at the cause. */
function narrowRowTarget(row: HTMLElement, hit: Hit): HTMLElement | null {
  return hit.segment_kind === "plan"
    ? row.querySelector<HTMLElement>(":scope > .plan-message")
    : null;
}

/** The one-line remark for a hit whose second walk found no credible mark.
 *
 *  THREE states, not two, and the diff sentences replace exactly one of them.
 *  With NO frame delivered the walk ran against content that may simply not be
 *  painted yet — a hidden tab, or a paint past the ceiling — so every kind claims
 *  "later" rather than "absent"; a diff on a hidden tab is unpainted rather than
 *  windowed out, and pointing at the diff would send the reader to a region for a
 *  problem they do not have. With a frame delivered, a `tool_diff` says which of
 *  the two things it is: the card renders a mini-diff whose shown hunks do not
 *  carry the match (`windowHunks` keeps 24 rows), or it renders no diff at all
 *  even after `awaitDeferredDiff` opened the card and waited for one.
 *
 *  THAT SECOND SENTENCE SAYS WHAT HAPPENED RATHER THAN WHAT TO DO, because
 *  opening the card is now what this code just tried: it is the reader's only
 *  remaining lever (retry it, or read the whole diff from the file), and telling
 *  them to open a card that is already open would be advice for a state they are
 *  not in. Neither sentence claims more than the wire can support: a hit carries no
 *  diff ordinal, and with Diffs[0]-only there is nothing else it could name. */
function missNotice(hit: Hit, rendered: boolean, narrowed: boolean): string {
  if (!rendered) {
    return "not rendered yet";
  }
  if (hit.segment_kind === "tool_diff") {
    return narrowed
      ? "not in the shown hunks \u2014 open the diff"
      : "only in this call's diff, which did not load";
  }
  return "not in rendered text";
}

/** The kinds whose rendered element lives inside `.tool-details`, so reaching one
 *  means opening the card's own disclosure — that region is where `detailsBody`
 *  BUILDS the input `<pre>` and the denial block, and where it appends a settled
 *  call's output text. `tool_title` and `tool_disclosed` are deliberately absent:
 *  both share the claim line, which is in the card's resting state.
 *
 *  `tool_diff` is absent too and is NOT the same case, which is why it is answered
 *  by `awaitDeferredDiff` instead of by a member here. `insertDiffPreview` inserts
 *  the mini-diff BEFORE `.tool-details`, so this set's question — does the text
 *  live inside that region — is simply false for it; and a preview-less card needs
 *  an AWAIT beside the open, which membership of a synchronous set cannot carry. */
const OPENS_TOOL_DETAILS = new Set<SegmentKind>(["tool_output", "tool_input", "tool_denial"]);

/** Open a tool card's own disclosure, when it has one and it is closed.
 *
 *  The single owner of that click, because two callers make it — this file's
 *  disclosure chain for the three kinds above, and `awaitDeferredDiff` for a
 *  preview-less diff — and they must not drift on how a closed card is recognised.
 *  Activating the real control rather than writing the attribute is what keeps the
 *  disclosure controller behind it agreeing with the DOM, exactly as the group
 *  case does. */
function openToolCardDetails(card: HTMLElement): void {
  const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
  if (toggle?.getAttribute("aria-expanded") === "false") {
    activateQuietly(toggle);
  }
}

/** Activate a transcript control the way a reader would, WITHOUT the click reaching
 *  the document.
 *
 *  `element.click()` dispatches a bubbling click, and this search box is a popup
 *  whose outside-dismissal listens for one at `document` (ui-primitives'
 *  `popup-core.ts`), so find opening a card or a group ON THE READER'S BEHALF read
 *  as the reader clicking outside the box and closed it — taking the query, the
 *  cursor and the counter with it. Latent for the three kinds whose landing is
 *  synchronous, which paint their position into a box that is already leaving; FATAL
 *  for a deferred diff, whose answer lands after an await and would find the overlay
 *  gone, so nothing at all reaches the reader.
 *
 *  A bubble-phase `stopPropagation` on the control itself rather than a hand-built
 *  event: every listener on the control still runs, in registration order
 *  (`stopPropagation` is not `stopImmediatePropagation`), so `createDisclosure`'s
 *  handler and `tool-card.ts`'s body builder are activated exactly as a real click
 *  activates them — and only the ancestors, `document` among them, are spared. */
function activateQuietly(control: HTMLElement): void {
  const stop = (e: Event): void => {
    e.stopPropagation();
  };
  control.addEventListener("click", stop);
  try {
    control.click();
  } finally {
    control.removeEventListener("click", stop);
  }
}

/** Open every closed disclosure between the hit's container and its row, so the walker
 *  can reach the text: reasoning `<details>` through the platform API, tool groups by
 *  ACTIVATING their real header, so the controller behind it keeps agreeing with the DOM.
 *  A hit whose kind lives inside `.tool-details` also opens its card's own disclosure.
 *
 *  The card is resolved with `closest` rather than by testing `target` itself, so
 *  the walk survives a future caller that passes a REGION inside the card: no
 *  caller does today, because the narrowing happens after this returns. */
function openDisclosureChain(row: HTMLElement, target: HTMLElement, hit: Hit): void {
  for (let cur: HTMLElement | null = target; cur !== null && cur !== row.parentElement;) {
    if (cur instanceof HTMLDetailsElement && !cur.open) {
      cur.open = true;
    }
    if (cur.classList.contains("tool-group") && cur.classList.contains("tool-group-collapsed")) {
      const header = cur.querySelector<HTMLElement>(":scope > .tool-group-header");
      if (header !== null) {
        activateQuietly(header);
      }
    }
    if (cur === row) {
      break;
    }
    cur = cur.parentElement;
  }
  const card = OPENS_TOOL_DETAILS.has(hit.segment_kind)
    ? target.closest<HTMLElement>(".tool-call")
    : null;
  if (card !== null) {
    openToolCardDetails(card);
  }
}

/** Ceiling on the deferred-diff wait below.
 *
 *  `stepServerHit` DROPS an Enter while `navBusy` is latched, so this bounds how
 *  long a keystroke may be swallowed rather than how long a request may take: the
 *  bulk's own budget is `@cplieger/fetch`'s 30s, and hanging the walk for that is
 *  worse than the miss notice. Long enough for a same-origin request the server
 *  answers off the chat file; a second visit to the same hit is free either way — the
 *  SUCCESS path because `toolCallBulk` memoises per `(chatID, toolCallID)` and the
 *  preview is then on screen, the DEAD one because `openBroughtNoDiff` answers before
 *  this ceiling is armed again. */
const DEFERRED_DIFF_WAIT_MS = 1500;

/** Cards whose open already tried the bulk and brought no diff back.
 *
 *  It bounds a DEAD diff hit to one ceiling per card rather than one per visit: the
 *  three endings that deliver nothing — no `chatID`, a null bulk, a zero-change diff
 *  — leave the preview absent and the disclosure present, and `detailsBody`'s
 *  builder has already run and will not run again, so every later visit re-entered
 *  and waited the full `DEFERRED_DIFF_WAIT_MS` for something nothing was going to
 *  send. `toolCallBulk` memoises only the SUCCESS path, so it cannot answer this.
 *
 *  A WeakSet keyed on the element rather than a `data-` attribute: this is find's own
 *  bookkeeping, nothing else reads it, and publishing it into the card's markup would
 *  invite a second reader — where the set keeps the DOM clean and dies with the
 *  element, so a re-mounted card is asked once more, which is right because its
 *  builder is fresh too. A bulk that answers PAST the ceiling still lands its
 *  preview, and the resting-preview guard below wins on the next visit, so marking
 *  at the ceiling cannot hide a diff that did arrive. */
const openBroughtNoDiff = new WeakSet<HTMLElement>();

/** Open a preview-less `tool_diff` card and answer the bounded wait for its diff,
 *  or `null` when there is nothing to wait for.
 *
 *  A call whose diff exceeded the preview budget renders NO diff at rest — the
 *  MAJORITY case, 4,625 of 7,285 diff-bearing calls — and loads it from the bulk
 *  when the reader OPENS the card (`tool-card.ts`'s `DEFERRED_PARTS`). So opening
 *  the disclosure is precisely what makes this hit's text exist, and the walk has
 *  to wait for it: the open is synchronous, so widening `OPENS_TOOL_DETAILS` alone
 *  would re-walk before the diff arrived and report a miss for every one of them.
 *
 *  `null` rather than a resolved promise for a card that ALREADY renders its
 *  preview, so that path takes no await at all and behaves exactly as it did
 *  before this existed: no disclosure opened, no fetch, not even a microtask hop.
 *  Same answer for a card carrying no disclosure, where nothing is coming.
 *
 *  THE FETCH IS THE PRICE AND IT IS BOUNDED TO ONE HIT: it happens only for a hit
 *  the reader actually stepped onto, which is what makes it acceptable where a
 *  bulk fetch per Enter was refused. Nothing here prefetches, opens
 *  speculatively, or costs anything per hit at list time. */
function awaitDeferredDiff(target: HTMLElement, hit: Hit): Promise<void> | null {
  if (hit.segment_kind !== "tool_diff") {
    return null;
  }
  const card = target.closest<HTMLElement>(".tool-call");
  if (card === null) {
    return null;
  }
  // A diff already on screen is the resting-state path, unchanged; a card whose open
  // has already brought nothing back has nothing more coming, so a second visit to
  // that hit answers at once instead of paying the ceiling again; a card with no
  // disclosure has nothing to open. All three answer `null`.
  if (
    card.querySelector(TOOL_DIFF_PREVIEW) !== null ||
    openBroughtNoDiff.has(card) ||
    card.querySelector(".tool-disclosure") === null
  ) {
    return null;
  }
  openToolCardDetails(card);
  return waitForDiffPreview(card);
}

/** The first `.tool-diff-preview` to appear on `card`, or the ceiling.
 *
 *  A MutationObserver rather than a poll: the arrival is one `insertBefore` the
 *  bulk's own `then` performs, so there is exactly one thing to observe and a poll
 *  would trade a wake-up cadence for it.
 *
 *  `subtree: true` because the callback and `awaitDeferredDiff`'s own guard both ask
 *  `card.querySelector(TOOL_DIFF_PREVIEW)` — a DESCENDANT question — so the
 *  subscription matches the question rather than the direct-child position
 *  `insertDiffPreview` happens to use today. Pinning that coincidence would make a
 *  nested insert added later fail SILENTLY as a ceiling rather than a landing, which
 *  is the worst failure shape available here; matching the guard costs one option.
 *
 *  THE CEILING IS NOT BELT-AND-BRACES: three ordinary endings deliver no preview
 *  at all — a card with no `chatID` requests nothing, a null bulk applies nothing,
 *  and `insertDiffPreview` returns early on a zero-change diff — and each has to
 *  reach the miss notice rather than hang the walk. It also covers a card the
 *  reader had already opened, whose builder ran once and will not run again. Reaching
 *  it MARKS the card, so the next visit to that hit answers without waiting. */
function waitForDiffPreview(card: HTMLElement): Promise<void> {
  return new Promise((resolve) => {
    const obs = new MutationObserver((_records, observer) => {
      if (card.querySelector(TOOL_DIFF_PREVIEW) === null) {
        return;
      }
      clearTimeout(ceiling);
      observer.disconnect();
      resolve();
    });
    const ceiling = setTimeout(() => {
      obs.disconnect();
      openBroughtNoDiff.add(card);
      resolve();
    }, DEFERRED_DIFF_WAIT_MS);
    obs.observe(card, { childList: true, subtree: true });
  });
}

/**
 * The nearest-match ranking over the target's marks, as the engine index of
 * the winner (-1 = no credible mark). Excerpt similarity first — the server
 * matched raw markdown and the DOM holds rendered text, so ordinals cannot be
 * trusted across the two — then relative position (`offset / segment_len`
 * against the mark's offset in the target's text), ties to the lowest index.
 */
function pickNearestMark(target: HTMLElement, hit: Hit): number {
  const excerptTokens = tokenSet(hit.excerpt);
  const offsets = markOffsets(target);
  const want = hit.offset / hit.segment_len;
  let best = -1;
  let bestSim = -1;
  let bestDist = Infinity;
  // A hit spanning several nodes is several marks sharing one `data-hit`; the
  // first piece is where the hit starts, and it alone is a candidate.
  const seen = new Set<number>();
  for (const mark of target.querySelectorAll<HTMLElement>("mark.find-hit")) {
    const index = Number(mark.getAttribute("data-hit"));
    if (seen.has(index)) {
      continue;
    }
    seen.add(index);
    const at = offsets.marks.get(mark) ?? 0;
    const sim = dice(excerptTokens, tokenSet(contextAround(offsets.text, at, mark)));
    const dist = Math.abs(want - at / Math.max(1, offsets.text.length));
    if (sim > bestSim || (sim === bestSim && dist < bestDist)) {
      best = index;
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

/** The context radius the server's excerpt carries, so the two sides of the
 *  similarity comparison span the same amount of text. The Go twin is
 *  `chat.searchExcerptRadius` (internal/chat/search.go), and the two are held
 *  together by chat-search.node.test.ts, which reads both files as text — there is
 *  no wire field to carry it and no codegen either side. */
const EXCERPT_RADIUS = 60;

/** The rendered-side counterpart of the server's excerpt: the text around the
 *  mark, at the same radius. */
function contextAround(text: string, at: number, mark: HTMLElement): string {
  const len = mark.textContent.length;
  return text.slice(Math.max(0, at - EXCERPT_RADIUS), at + len + EXCERPT_RADIUS);
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

/** Open the transcript search carrying `query` and land on `hit`: the handoff
 *  from a surface that found the CONVERSATION and counted its matches (the
 *  History page), so the count it showed is a number the reader can reach. Rides
 *  the cross-tab RESUME: the box re-runs `query`, `render` finds the hit in the
 *  fresh answer, and the landing is the pipeline a keypress takes; a hit the fresh
 *  answer no longer holds is not chased. The caller opens the chat's TAB first and
 *  awaits it: the switch closes this box and clears the active chat's query, so a
 *  handoff running before it would be undone by it. */
export function openChatFindAt(query: string, hit: Hit): void {
  ensureBuilt();
  if (shell === null) {
    return;
  }
  shell.input.value = query;
  resumeKey = hitKey(hit);
  landOnResume = true;
  openFindInChat();
}

/** Close the transcript search, running the full teardown: hiding the box instead
 *  would leave the observer, the marks and the folds behind. Idempotent, so a
 *  close that re-enters through the popup's onClose is a no-op.
 *
 *  @internal Test seam.
 *  @knipignore The test loads this module through a cache-busting dynamic specifier. */
export function closeChatFind(): void {
  popup?.hide();
}

/** Toggle the transcript search. What the toolbar button means.
 *
 *  The button used to call the OPEN path, so a second click re-focused,
 *  re-selected and re-ran the search of a box that was already open — a control
 *  that looks like a toggle and is not. `files-search.ts` already had this shape,
 *  and `tabs.ts`'s toggle/open verb pair is the same distinction for a whole
 *  view. */
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

/** @internal Test seam: whether the transcript search is open.
 *
 *  @knipignore The test loads this module through a cache-busting dynamic specifier. */
export function _isChatFindOpen(): boolean {
  return isOpen();
}
