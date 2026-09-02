// ---------------------------------------------------------------------------
// Reading position for the transcript: TWO NAMED STATES, and one helper that
// every layout change goes through.
//
//   Following — pinned to the live edge. The default while a turn streams.
//   Reading   — parked on purpose. Nothing may move under the reader.
//
// Reading is entered by scrolling up, and by a timeline or search jump that
// actually leaves the live edge (`jumpTo` — a jump with nowhere to go keeps the
// reader Following). It is left by reaching the bottom again, by the resume
// control, or by End.
//
// ONLY A READER GESTURE MAY ENTER READING. Every scroll this module performs
// records its landing (`scrollSelfTo`) and the listener drops the event that
// write produces, because Following pins to the ANCHOR rather than to the
// document bottom: the pin is legitimately far from the bottom whenever tall
// evidence renders below the live text block, so a controller that re-derived
// its state from its own pin declared the reader Reading and switched its own
// auto-scroll off. That is how a large tool card used to stop the transcript
// following, with the resume control appearing untouched.
//
// The value of naming the state is that the user stops fighting an invisible
// heuristic. Log viewers have taught this for decades, and an agent transcript
// IS a log.
//
// WHY A BESPOKE HELPER RATHER THAN CSS SCROLL ANCHORING. Safari does not support
// `overflow-anchor` at all, and on iOS every browser is WebKit — so the platform
// this app is most read on has no native anchoring whatsoever. Worse, where it IS
// supported, leaving it on alongside the helper is not a harmless no-op but a
// DOUBLE correction: anchoring queues an adjustment and applies it at the end of
// its suppression window while the helper adds the same delta itself, so the
// reader moves roughly twice as far. So `overflow-anchor: none` is set on the
// scroller (css/13-messages.css) and this module owns every mutation, on every
// engine. Deterministic, and it removes the feature-detect entirely.
//
// (If native anchoring is ever wanted back, the helper must switch from ADDING a
// height delta to correcting the RESIDUAL displacement of a snapshotted anchor
// rect. That is a different algorithm, not a flag.)
//
// Auto-scroll during streaming: a MutationObserver on the container fires on
// every chunk; a ResizeObserver catches images loading and code blocks
// expanding. `behavior: "instant"` — not "smooth" — is correct here, because
// each chunk's small delta compounds into perceptually continuous motion while
// "smooth" schedules a 250ms animation the next chunk cancels, producing
// visible stutter. Reference: vercel/ai-chatbot use-scroll-to-bottom.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { loadMoreSkeleton } from "./skeleton.js";
import { $ } from "./dom.js";

/** Distance from the top at which older messages start loading. */
const LOAD_MORE_THRESHOLD_PX = 100;
/** Distance from the bottom still counted as "at the bottom". Same numeric
 *  value as LOAD_MORE_THRESHOLD_PX by coincidence; semantically independent. */
const BOTTOM_TOLERANCE_PX = 100;
/** Debounce before re-evaluating the state after a user scroll. */
const USER_SCROLL_DEBOUNCE_MS = 150;
/** How long a bottom pin keeps re-asserting the maximum. Derived from the fold
 *  choreography the pin itself releases: `--fold-slide` runs 0.3s and a close
 *  flips `content-visibility` at 0.42s (css/29-turns.css), so the document is
 *  still growing ~450ms after the click. */
const PIN_SETTLE_MS = 700;

/** The reader's position, as a state rather than an inferred boolean. */
export type ReadingState = "following" | "reading";

/** The scroll-owned slice of a parked view's saved state: where the scroller
 *  stood and which reading state the reader was in. The multiplexer
 *  (messages.ts) carries it inside its ViewHandle across a park/unpark cycle;
 *  everything else the controller holds per view (the deferred-mutation queue,
 *  the pagination pass) is deliberately NOT saved — the pass is abandoned via
 *  its existing cancellable hook and the catch-up paint re-derives the queue. */
export interface ViewScrollState {
  scrollTop: number;
  readingState: ReadingState;
}

/** What `attach` needs to hand the scroller to a transcript view: the view
 *  element (the observers' new root) plus the state to restore into it. */
export interface ViewAttachHandle extends ViewScrollState {
  el: HTMLElement;
}

/** Which geometry a mutation disturbs. The two are physically different and a
 *  helper that measures the wrong one compensates by ZERO:
 *
 *   content-growth  content is inserted above the reader; the box is unchanged,
 *                   so `scrollHeight` moves and `clientHeight` does not.
 *                   (Loading older messages; a turn folding above the reader.)
 *   viewport-shrink a panel takes vertical space; the content is unchanged, so
 *                   `clientHeight` moves and `scrollHeight` does not.
 *                   (The shell panel opening; the composer dock growing.)
 *
 *  A growing prompt bar measured as content-growth yields a delta of zero and
 *  compensates nothing — which is precisely the failure the dock needs
 *  prevented. Each call site declares the shift it causes. */
export type ShiftKind = "content-growth" | "viewport-shrink";

class ScrollController {
  readonly scrollEl: HTMLElement;

  /** The observers' root: the ACTIVE transcript view under the multiplexer,
   *  or the multiplexer itself before any view has attached (which is also
   *  what keeps the pre-multiplexer test fixtures valid — they populate
   *  `#messages` directly and never attach a view). Every mutation callback,
   *  the per-child ResizeObserver set and the pagination furniture key off
   *  this element, so a parked view receives no callbacks and no furniture. */
  private viewEl: HTMLElement;

  private state: ReadingState = "following";
  private userScrollingUntil = 0;

  private hasMoreMessages = false;
  private loadingMore = false;
  private onLoadMore: (() => void) | null = null;

  /** Mutations that were postponed because the reader is Reading. Applied in
   *  arrival order on the return to Following. */
  private deferred: (() => void)[] = [];
  private stateListeners: ((s: ReadingState) => void)[] = [];
  /** Callbacks riding the transcript MutationObserver this module already owns
   *  (childList + subtree + characterData on `#messages`). One observer for the
   *  container instead of one per consumer: find-in-chat's live re-run used to
   *  duplicate it wholesale. */
  private mutateListeners: (() => void)[] = [];
  /** Supplies the element Following should keep visible while a turn streams.
   *  Null (or a null return) falls back to the document bottom. */
  private anchorProvider: (() => HTMLElement | null) | null = null;

  private rafPending = false;

  /** The bottom pin's deadline, and the frame it has queued (0 = none). */
  private pinUntil = 0;
  private pinFrame = 0;

  /** The scrollTop this controller last wrote, or -1.
   *
   *  A `scroll` event landing on it is the controller's OWN and must not be read
   *  as a reader gesture. Following pins to the ANCHOR, which is deliberately
   *  not the document bottom, so deriving the state from a self-inflicted scroll
   *  declared the reader Reading — and Reading is what makes
   *  `autoScrollIfAnchored` return early, so the auto-scroll latched off for the
   *  rest of the session. Measured in a real browser: an anchor 200px tall at
   *  offsetTop 1500 in a 400px viewport with a 900px tool card below it pins to
   *  1350 against a maximum of 2200, which is 850px from the bottom, so the
   *  controller's own pin failed its own `isAtBottom` check by 750px. The reader
   *  saw the transcript stop following and the `Latest` control appear without
   *  having touched anything.
   *
   *  Compared as a POSITION rather than tracked as a boolean, because a
   *  programmatic scroll that changes nothing fires no event at all and a flag
   *  would then swallow the reader's next real gesture. Consumed on the first
   *  event either way, so a stale marker cannot outlive one. */
  private selfScrollTop = -1;

  /** Last value written to `--scrollbar-w`, so a resize storm costs at most one
   *  style invalidation. */
  private scrollbarWidth = "";

  /** Teardown for the pagination pass in flight, or null when none is running.
   *
   *  A pass belongs to the chat that started it, and both halves of its
   *  completion handshake are chat-blind: the signal is the presence of one
   *  global element id, and the compensation is a height captured in a closure.
   *  So the pass has to be reachable from outside the method that armed it, or a
   *  chat switch cannot cancel it. */
  private pendingLoad: (() => void) | null = null;

  /** The observers rooted on `viewEl`, held as fields so `attach` can re-root
   *  them on the incoming view. ONE ResizeObserver serves the scroller AND the
   *  view's children — the scroller entry is permanent, the child set is
   *  re-pointed per view. */
  private contentObserver: MutationObserver | null = null;
  private childObserver: MutationObserver | null = null;
  private resizeObserver: ResizeObserver | null = null;
  private observedChildren = new Set<Element>();

  constructor(messagesEl: HTMLElement, scrollEl: HTMLElement) {
    this.scrollEl = scrollEl;
    this.viewEl = messagesEl;
  }

  init(): void {
    const scrollBtn = $.scrollBottom;

    // The transcript's scrollbar belongs in the gutter, not in the measure the
    // column and the composer share: a classic bar is placed at the scroller's
    // inline-end border edge and takes its width out of the CONTENT box, so
    // `#messages` centred inside sits half a scrollbar left of `.prompt-box`
    // unless the scroller gives that width back. `scrollbar-gutter: stable`
    // (css/13-messages.css) makes this reading valid whether or not the transcript
    // overflows, and that file's END inset is what subtracts it.
    this.publishScrollbarWidth();

    this.scrollEl.addEventListener(
      "scroll",
      () => {
        // Consume the marker whichever branch runs: it may only ever excuse the
        // one event its own write produced.
        const self = this.selfScrollTop;
        this.selfScrollTop = -1;
        if (self >= 0 && Math.abs(this.scrollEl.scrollTop - self) <= 1) {
          // The controller's own landing. It says nothing about where the reader
          // wants to be, so neither the state nor the debounce may move — and
          // the debounce moving is what throttled the pin to one per 150ms
          // while a turn streamed.
          this.maybeLoadMore();
          return;
        }
        this.userScrollingUntil = Date.now() + USER_SCROLL_DEBOUNCE_MS;
        this.setState(this.isAtBottom() ? "following" : "reading");
        this.maybeLoadMore();
      },
      { passive: true },
    );

    scrollBtn.addEventListener("click", () => {
      this.resume();
    });

    // End resumes Following, which is the keyboard half of the resume control.
    // Ignored while the caret is in a text field, where End means end-of-line.
    document.addEventListener("keydown", (e) => {
      if (e.key !== "End" || e.ctrlKey || e.metaKey || e.altKey) {
        return;
      }
      const t = e.target as HTMLElement | null;
      if (t !== null && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) {
        return;
      }
      if (this.state === "reading") {
        this.resume();
      }
    });

    const mutationObserver = new MutationObserver(() => {
      this.revalidateReadingState();
      this.autoScrollIfAnchored();
      for (const cb of this.mutateListeners) {
        cb();
      }
    });
    this.contentObserver = mutationObserver;

    const resizeObserver = new ResizeObserver(() => {
      // Re-measured here rather than on `window.resize`: this fires AFTER layout
      // and only when the scroller's content box actually moved, which is exactly
      // when the reserved gutter can have changed (browser zoom moves its width in
      // CSS pixels; a classic bar swapped for an overlay one frees all 10px). A
      // window listener read the pre-relayout value and left the bar reserving a
      // strip that no longer existed. `stable` means overflow alone never resizes
      // this box, so streaming costs no extra writes.
      this.publishScrollbarWidth();
      this.revalidateReadingState();
      this.autoScrollIfAnchored();
    });
    resizeObserver.observe(this.scrollEl);
    this.resizeObserver = resizeObserver;
    this.childObserver = new MutationObserver(() => {
      this.reobserveChildren();
    });
    this.observeView(this.viewEl);
  }

  /** Root the content observers on `el`: the transcript MutationObserver, the
   *  childList watcher behind the per-child ResizeObserver set, and that set
   *  itself (the view's children = the turn cards, as before the multiplexer).
   *  `attach` calls this with the incoming view; `detach` disconnects without
   *  re-rooting, which is what makes a parked view observer-silent. */
  private observeView(el: HTMLElement): void {
    this.viewEl = el;
    this.contentObserver?.disconnect();
    this.contentObserver?.observe(el, {
      childList: true,
      subtree: true,
      characterData: true,
    });
    this.childObserver?.disconnect();
    this.childObserver?.observe(el, { childList: true });
    this.reobserveChildren();
  }

  private disconnectView(): void {
    this.contentObserver?.disconnect();
    this.childObserver?.disconnect();
    for (const child of this.observedChildren) {
      this.resizeObserver?.unobserve(child);
    }
    this.observedChildren.clear();
  }

  private reobserveChildren(): void {
    const observer = this.resizeObserver;
    if (observer === null) {
      return;
    }
    const current = new Set<Element>(this.viewEl.children);
    for (const child of this.observedChildren) {
      if (!current.has(child)) {
        observer.unobserve(child);
        this.observedChildren.delete(child);
      }
    }
    for (const child of current) {
      if (!this.observedChildren.has(child)) {
        observer.observe(child);
        this.observedChildren.add(child);
      }
    }
  }

  /** Write the scroller's reserved gutter to `--scrollbar-w` — the width its own
   *  inline-END inset gives back so the scrollbar lands in the gutter rather than
   *  in the measure the column shares with the composer. Reads the real element
   *  rather than a probe div, so the number is the gutter actually reserved on the
   *  box being compensated. */
  private publishScrollbarWidth(): void {
    const next = `${String(this.scrollEl.offsetWidth - this.scrollEl.clientWidth)}px`;
    if (next === this.scrollbarWidth) {
      return;
    }
    this.scrollbarWidth = next;
    document.documentElement.style.setProperty("--scrollbar-w", next);
  }

  // --- Public API ---

  readingState(): ReadingState {
    return this.state;
  }

  onReadingStateChange(cb: (s: ReadingState) => void): void {
    this.stateListeners.push(cb);
  }

  /** Register `cb` on the transcript's own MutationObserver; returns the
   *  unregister. Delivery keeps the observer's microtask timing, so a consumer
   *  that mutates the transcript itself can suppress its own echo the same way
   *  it would with an observer of its own. */
  onTranscriptMutate(cb: () => void): () => void {
    this.mutateListeners.push(cb);
    return () => {
      const at = this.mutateListeners.indexOf(cb);
      if (at >= 0) {
        this.mutateListeners.splice(at, 1);
      }
    };
  }

  setAnchorProvider(fn: (() => HTMLElement | null) | null): void {
    this.anchorProvider = fn;
  }

  /** Set the resume control's label, so the one element that knows the reader is
   *  behind is the one that says how far. Counts BLOCKS, not messages: a long
   *  streaming turn should show progress rather than a static badge. */
  setResumeLabel(text: string): void {
    const btn = $.scrollBottom;
    const label = btn.querySelector("span");
    if (label !== null) {
      label.textContent = text;
    }
  }

  /** Return to Following: pin to the live edge and flush deferred mutations. */
  resume(): void {
    this.pinToLiveEdge();
  }

  /** Enter Reading explicitly — a collapse the user asked for parks them on the
   *  content above it, and nothing may then move under them.
   *
   *  The park is not unconditional, and `revalidateReadingState` is the one thing
   *  that revokes it: a LATER size change leaving the reader within
   *  BOTTOM_TOLERANCE_PX of the end releases them back to Following. That is the
   *  case this park cannot serve — a collapse that removes everything below the
   *  reader would otherwise hold them on a transcript that no longer extends past
   *  the fold, with the resume control offering a journey to where they already
   *  are. */
  setUserScrolledUp(v: boolean): void {
    this.setState(v ? "reading" : "following");
  }

  /**
   * Park the reader on `target`: a timeline marker's jump, or a search hit.
   *
   * Reading is entered ONLY when the jump actually leaves the live edge, and
   * that condition is why this lives here rather than at the two call sites. A
   * transcript that does not overflow cannot move, and a target already at the
   * bottom does not move the reader off it — so declaring Reading there shows
   * the resume control with nothing to resume from, and because nothing
   * scrolled, no scroll event arrives to re-derive the state. It sticks until
   * the reader happens to scroll something. Measured on a one-turn chat with no
   * scrollbar: clicking the rail's marker 1 raised `Latest` while `scrollTop`
   * never left 0.
   */
  jumpTo(target: HTMLElement, opts: ScrollIntoViewOptions = {}): void {
    // A jump is a new destination, and a landing at the live edge keeps the state
    // Following — so the pass's own guard cannot stop it, and the next frame would
    // overwrite the landing and abort the smooth scroll in flight with it.
    this.cancelPinPass();
    this.setState(this.landsAtLiveEdge(target, opts.block ?? "start") ? "following" : "reading");
    // Guarded because jsdom does not implement scrollIntoView, and both callers
    // are unit-tested against the DOM they build.
    const fn = (target as { scrollIntoView?: (o?: ScrollIntoViewOptions) => void }).scrollIntoView;
    if (typeof fn === "function") {
      fn.call(target, { block: "start", behavior: "smooth", ...opts });
    }
  }

  /** Would a jump to `target` leave the reader at the live edge?
   *
   *  Answered from the CLAMPED landing position, which is what makes the
   *  non-overflowing case fall out rather than needing its own branch: such a
   *  scroller's only landing is 0, and 0 is also its bottom. */
  private landsAtLiveEdge(target: HTMLElement, block: ScrollLogicalPosition): boolean {
    const max = Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
    // Read off rects so the answer holds whatever the offsetParent turns out to
    // be — the transcript's scroller is a positioned ancestor, its column is not.
    const top =
      this.scrollEl.scrollTop +
      (target.getBoundingClientRect().top - this.scrollEl.getBoundingClientRect().top);
    const room = this.scrollEl.clientHeight - target.offsetHeight;
    let wanted = top;
    if (block === "center") {
      wanted = top - room / 2;
    } else if (block === "end") {
      wanted = top - room;
    }
    const landing = Math.max(0, Math.min(wanted, max));
    return landing >= max - BOTTOM_TOLERANCE_PX;
  }

  /**
   * Run `mutate` without moving what the reader is looking at.
   *
   * THE ONE ENTRY POINT for every layout change in the transcript. §3.5's
   * turn-level auto-collapse moves hundreds of pixels rather than tens, which is
   * what makes this mandatory rather than incidental.
   *
   * While Following there is nothing to preserve — the reader is pinned to the
   * live edge and the auto-scroll will re-pin — so the mutation runs bare.
   */
  preserveReadingPosition(mutate: () => void, kind: ShiftKind): void {
    if (this.state === "following") {
      mutate();
      return;
    }
    const before =
      kind === "content-growth" ? this.scrollEl.scrollHeight : this.scrollEl.clientHeight;
    mutate();
    const after =
      kind === "content-growth" ? this.scrollEl.scrollHeight : this.scrollEl.clientHeight;
    const delta = kind === "content-growth" ? after - before : before - after;
    if (delta !== 0) {
      this.scrollEl.scrollTop += delta;
    }
  }

  /**
   * Apply `mutate` now if Following, or queue it until the reader returns.
   *
   * Content must never disappear from above the reader through no action of
   * their own, which is exactly what a turn folding while they read does.
   */
  deferWhileReading(mutate: () => void): void {
    if (this.state === "following") {
      mutate();
      return;
    }
    this.deferred.push(mutate);
  }

  scrollToBottom(): void {
    this.pinToLiveEdge();
  }

  /** Land at the live edge and HOLD it there while the layout settles.
   *
   *  The live edge is `followTarget`, the same number `autoScrollIfAnchored`
   *  writes. ONE target for both writers, or the window holds a position
   *  Following does not mean and the hand-off at its deadline has to move the
   *  reader.
   *
   *  INSTANT, not smooth: a smooth scroll fires ~50 scroll events whose first is
   *  a whole viewport from the target, so the single-shot `selfScrollTop` marker
   *  is consumed by an event the listener then reads as a reader gesture — which
   *  parks the reader, un-hides this control and switches the anchor re-pin off.
   *  Its target is also frozen at flight start, so height the flush itself
   *  animates in (`--fold-slide`) never moves it. Measured in Chromium: a
   *  landing 2000px above the bottom, state stuck at Reading.
   *
   *  The re-assert is what covers the growth, and it costs ~42 frames of two
   *  layout reads with no write in the common case. That is not optimised away
   *  with an it-stopped-moving exit: this runs only on an explicit resume or a
   *  sent turn, and it already stops the moment the reader scrolls. Every write
   *  goes through `scrollSelfTo`, so each frame records its own marker and the
   *  listener stays quiet. */
  private pinToLiveEdge(): void {
    this.setState("following");
    this.userScrollingUntil = 0;
    this.pinUntil = Date.now() + PIN_SETTLE_MS;
    this.pinLiveEdgeNow();
    this.queuePinFrame();
  }

  private queuePinFrame(): void {
    if (this.pinFrame !== 0) {
      return;
    }
    this.pinFrame = requestAnimationFrame(() => {
      this.pinFrame = 0;
      // The reader outranks their own earlier click, and the state alone cannot
      // see that: a gesture landing inside BOTTOM_TOLERANCE_PX keeps Following,
      // so the debounce is the condition that catches it — the same guard
      // `autoScrollIfAnchored` applies. `pinToLiveEdge` zeroes the debounce on
      // entry and the listener's self-scroll branch never arms it, so the pass's
      // own writes cannot trip this.
      if (
        this.state !== "following" ||
        Date.now() < this.userScrollingUntil ||
        Date.now() >= this.pinUntil
      ) {
        this.pinUntil = 0;
        return;
      }
      this.pinLiveEdgeNow();
      this.queuePinFrame();
    });
  }

  /** The clamp is what makes the COMPARE work, rather than tidiness `scrollSelfTo`
   *  would repeat: a follow target of `scrollHeight` is a whole viewport past the
   *  maximum, so an unclamped compare never matches and every frame writes. */
  private pinLiveEdgeNow(): void {
    const max = Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
    const landing = Math.max(0, Math.min(this.followTarget(), max));
    if (this.scrollEl.scrollTop !== landing) {
      this.scrollSelfTo(landing, "instant");
    }
  }

  /** Where Following belongs: the anchor's pin while a turn streams, the document
   *  bottom otherwise. ONE definition, read by both writers — the streaming
   *  re-pin and the bottom pin's settle pass — so they cannot disagree about the
   *  position the state means. */
  private followTarget(): number {
    const anchor = this.anchorProvider?.() ?? null;
    return anchor === null ? this.scrollEl.scrollHeight : this.anchorTop(anchor);
  }

  private cancelPinPass(): void {
    if (this.pinFrame !== 0) {
      cancelAnimationFrame(this.pinFrame);
      this.pinFrame = 0;
    }
    this.pinUntil = 0;
  }

  /** Hand the scroller to a transcript view: re-root the observers on it and
   *  restore its saved reading state and scroll position. The unpark half of
   *  the park/unpark pair; a freshly created view attaches with
   *  `{scrollTop: 0, readingState: "following"}`. */
  attach(handle: ViewAttachHandle): void {
    // Before anything else: a live pin pass belongs to the OUTGOING view, and
    // this method is about to write the incoming one's own scrollTop.
    this.cancelPinPass();
    this.observeView(handle.el);
    this.userScrollingUntil = 0;
    this.setState(handle.readingState);
    this.scrollSelfTo(handle.scrollTop, "instant");
  }

  /** Take the scroller away from the current view: snapshot the scroll-owned
   *  state for the view's handle, abandon the pagination pass in flight (its
   *  completion signal and its height compensation both belong to the outgoing
   *  transcript), drop the deferred-mutation queue (the unpark catch-up paint
   *  re-derives every fold the queue was holding), and disconnect the
   *  observers so the parked view can never produce a callback. */
  detach(): ViewScrollState {
    const snapshot: ViewScrollState = {
      scrollTop: this.scrollEl.scrollTop,
      readingState: this.state,
    };
    this.deferred = [];
    this.abandonLoadPass();
    this.cancelPinPass();
    this.userScrollingUntil = 0;
    this.selfScrollTop = -1;
    this.onLoadMore = null;
    this.hasMoreMessages = false;
    this.disconnectView();
    this.setState("following");
    return snapshot;
  }

  setLoadMore(fn: (() => void) | null, hasMore: boolean): void {
    this.onLoadMore = fn;
    this.hasMoreMessages = hasMore;
    this.updateLoadMoreIndicator();
  }

  /** Fetch until the scroller actually overflows.
   *
   *  Folding can starve its own pagination trigger: maybeLoadMore has exactly
   *  one caller (the scroll listener) and returns early unless scrollTop < 100,
   *  so once ~23 resident turns fold to one row each the page can be SHORTER
   *  than the viewport — no overflow, no scroll event, no fetch, and nothing to
   *  click. After a fold pass this restores the trigger.
   *
   *  The three preconditions (nothing older, a fetch already in flight, no
   *  callback) are maybeLoadMore's own and are checked there for every caller,
   *  so this only has to decide whether the viewport is already full.
   */
  fillViewport(): void {
    if (this.scrollEl.scrollHeight > this.scrollEl.clientHeight + BOTTOM_TOLERANCE_PX) {
      return;
    }
    this.maybeLoadMore(true);
  }

  /** How far the transcript can scroll: content height minus viewport height, 0
   *  when it fits.
   *
   *  A pure MEASUREMENT with no threshold applied, because the only caller that
   *  wants one (the turn rail, deciding whether it is worth existing) has a
   *  different question from this module's own bottom-detection tolerance. Two
   *  unrelated decisions that happen to want similar numbers must not share a
   *  constant — see BOTTOM_TOLERANCE_PX, which is about what counts as AT the
   *  bottom, not about whether there is a bottom to reach. */
  scrollableBy(): number {
    return Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
  }

  /** Hand the scroller to a different chat, or to no chat at all.
   *
   *  Every line here is mechanism- or order-sensitive, and each one was a way the
   *  outgoing chat's furniture survived into the next chat.
   *
   *  The queue is emptied FIRST: returning to Following flushes deferred
   *  mutations, and those close over the transcript that is about to be replaced.
   *
   *  The state change goes through `setState`, the only writer of the resume
   *  control's `hidden` class. Assigning the field left the control visible over
   *  a chat the reader had never scrolled, and the unchanged-state guard then
   *  made it unclearable — the field already said Following, so the next genuine
   *  return to Following was a no-op.
   *
   *  Pagination is dropped through `setLoadMore`, which also REMOVES the "Load
   *  older messages" button. That button is an unkeyed child of `#messages`, so
   *  the transcript's keyed reconcile never touches it; nulling the two fields
   *  alone left it on screen over the next chat, still holding the previous
   *  chat's callback, so pressing it fetched a page of the wrong conversation.
   *
   *  A fetch already in flight is ABANDONED rather than left to land. Its page
   *  still arrives in the store for the chat that asked, which is where it
   *  belongs; what must not survive is this scroller's half of the pass. */
  resetScrollState(): void {
    this.deferred = [];
    this.abandonLoadPass();
    this.cancelPinPass();
    this.userScrollingUntil = 0;
    this.selfScrollTop = -1;
    this.setLoadMore(null, false);
    this.setState("following");
  }

  // --- Internal ---

  private setState(next: ReadingState): void {
    if (this.state === next) {
      return;
    }
    this.state = next;
    // The single owner of the resume control's visibility: visible ⇔ Reading.
    // Nothing else writes this class, so a caller that has just moved the
    // scroller does not need to hide the control itself.
    $.scrollBottom.classList.toggle("hidden", next === "following");
    if (next === "following") {
      this.flushDeferred();
    }
    for (const cb of this.stateListeners) {
      cb(next);
    }
  }

  /** Apply queued mutations, each still compensated: the reader is at the live
   *  edge now, but a fold above them would still yank the content. */
  private flushDeferred(): void {
    if (this.deferred.length === 0) {
      return;
    }
    const queue = this.deferred;
    this.deferred = [];
    for (const fn of queue) {
      fn();
    }
  }

  private isAtBottom(): boolean {
    return (
      this.scrollEl.scrollTop + this.scrollEl.clientHeight >=
      this.scrollEl.scrollHeight - BOTTOM_TOLERANCE_PX
    );
  }

  /**
   * Re-derive Reading from the geometry after the content's SIZE changed.
   *
   * Both observers reach this, and without it a Reading reader could only ever
   * be released by a scroll event — so content DISAPPEARING from below them left
   * the state (and therefore the resume control) describing a transcript that no
   * longer exists. Collapsing a delegate's card is the case that reported it: the
   * body goes to `height: 0`, the reader is now at the end, and no node was
   * inserted or removed and no gesture was made, so nothing re-asked the
   * question. A shrink need not even move `scrollTop`, so there may be no scroll
   * event at all.
   *
   * ONE-DIRECTIONAL, and that is the whole safety of it. Promoting Following to
   * Reading from a size change is what the `selfScrollTop` marker exists to
   * prevent (see its comment): tall evidence rendering below the anchor puts the
   * controller's own legitimate pin hundreds of pixels from the bottom, so a
   * resize-driven demotion would switch the auto-scroll off exactly when a turn
   * is streaming. Deleting `autoScrollIfAnchored`'s early return would be that
   * bug; this is the other half of the state machine and nothing else.
   *
   * The debounce window still wins: a gesture in flight is the reader's answer,
   * not the layout's — and it is also what keeps this off a smooth `jumpTo`,
   * whose intermediate scroll events refresh the window all the way to the
   * landing.
   *
   * Cheap on the hot path by construction: a streaming turn holds Following, so
   * the per-chunk call ends at the first field compare and reads no layout.
   */
  private revalidateReadingState(): void {
    if (this.state !== "reading") {
      return;
    }
    if (Date.now() < this.userScrollingUntil) {
      return;
    }
    if (this.isAtBottom()) {
      this.setState("following");
    }
  }

  /**
   * Pin to the ACTIVE TEXT BLOCK, not to the document bottom.
   *
   * A bug fix rather than a refinement: the agent streams a sentence, a 400-line
   * diff card renders below it, and pinning to `scrollHeight` scrolls the
   * sentence the reader is mid-way through off the top. Now that evidence is
   * full-width that stops being an edge case — tall evidence renders below the
   * fold and STAYS there until the reader goes to it.
   *
   * Falls back to the document bottom when no anchor is offered, which is every
   * non-streaming append.
   */
  private autoScrollIfAnchored(): void {
    if (this.state === "reading") {
      return;
    }
    // Exactly one writer moves the scroller per frame, or the second write
    // overwrites the first's single-shot `selfScrollTop` marker and the orphaned
    // event is read as a reader gesture. Yielding costs nothing: the pass
    // re-asserts `followTarget`, the same number this frame would have written,
    // so the window neither overrides the anchor nor has anything to correct
    // when it closes.
    if (this.pinFrame !== 0) {
      return;
    }
    if (Date.now() < this.userScrollingUntil) {
      return;
    }
    if (this.rafPending) {
      return;
    }
    this.rafPending = true;
    requestAnimationFrame(() => {
      this.rafPending = false;
      this.scrollSelfTo(this.followTarget(), "instant");
    });
  }

  /** Move the scroller and record where it will LAND, so the `scroll` event the
   *  write produces is recognised as this controller's own.
   *
   *  The clamp is not tidiness: the marker has to be the position the browser
   *  will actually reach, and both callers pass values the platform clamps for
   *  them (`scrollHeight` is a whole viewport past the maximum, and an anchor
   *  inside a collapsed disclosure reports offsets that overflow the document
   *  it no longer contributes height to). An unclamped marker never matches the
   *  event, which is the same as having no marker at all. */
  private scrollSelfTo(top: number, behavior: ScrollBehavior): void {
    const max = Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
    const landing = Math.max(0, Math.min(top, max));
    this.selfScrollTop = landing;
    this.scrollEl.scrollTo({ top: landing, behavior });
  }

  /** The scrollTop that puts `anchor`'s bottom at the viewport's bottom, never
   *  scrolling backwards (the anchor grows downward as text streams in). */
  private anchorTop(anchor: HTMLElement): number {
    const wanted =
      anchor.offsetTop + anchor.offsetHeight - this.scrollEl.clientHeight + BOTTOM_TOLERANCE_PX / 2;
    return Math.max(0, Math.min(wanted, this.scrollEl.scrollHeight));
  }

  private maybeLoadMore(force = false): void {
    if (!force && this.scrollEl.scrollTop >= LOAD_MORE_THRESHOLD_PX) {
      return;
    }
    if (!this.hasMoreMessages || this.loadingMore || this.onLoadMore === null) {
      return;
    }
    this.loadingMore = true;
    const skel = loadMoreSkeleton();
    skel.id = "load-more-skeleton";
    // Scoped to the attached view: a parked view keeps its own pagination
    // furniture (it is that view's DOM), so a document-wide id lookup could
    // find a sibling view's button and mount this pass's skeleton there.
    const indicator = this.viewEl.querySelector(`[id="load-more-indicator"]`);
    if (indicator !== null) {
      indicator.replaceWith(skel);
    } else {
      this.viewEl.prepend(skel);
    }
    // The content-growth instance this helper was generalised from: older
    // messages land ABOVE the reader, so the box is unchanged and scrollHeight
    // is the delta to restore. Measured across the whole load rather than inside
    // preserveReadingPosition because the mutation is asynchronous — the
    // observer below is what marks its completion.
    const prevHeight = this.scrollEl.scrollHeight;
    this.onLoadMore();
    const observer = new MutationObserver(() => {
      if (document.getElementById("load-more-skeleton") === null) {
        this.endLoadPass();
        const newHeight = this.scrollEl.scrollHeight;
        this.scrollEl.scrollTop += newHeight - prevHeight;
      }
    });
    const safetyTimer = setTimeout(() => {
      this.abandonLoadPass();
    }, 15_000);
    // What makes the pass CANCELLABLE, and it has to be a field because the
    // observer and the timer are locals nothing outside this method can reach.
    // Both the completion signal and the height above are the previous chat's
    // the moment the scroller changes hands: the signal is a global element id
    // that the caller drops when ITS fetch resolves, whichever chat is on screen
    // by then, so an uncancelled pass charged the incoming chat a delta measured
    // against a transcript it never showed.
    this.pendingLoad = (): void => {
      observer.disconnect();
      clearTimeout(safetyTimer);
    };
    observer.observe(this.viewEl, { childList: true });
  }

  /** End the pagination pass in flight, so neither its observer nor its timer can
   *  fire again. Idempotent, and safe to call when there is no pass — the
   *  in-flight flag is cleared either way, so a pass that died before it could be
   *  armed cannot wedge pagination off. */
  private endLoadPass(): void {
    const end = this.pendingLoad;
    this.pendingLoad = null;
    this.loadingMore = false;
    end?.();
  }

  /** Give up on the pass in flight and take its skeleton down.
   *
   *  Order is load-bearing: ending the pass first is what stops the removal below
   *  from being read as the fetch completing and compensating with a stale
   *  height. */
  private abandonLoadPass(): void {
    this.endLoadPass();
    document.getElementById("load-more-skeleton")?.remove();
  }

  /** A real BUTTON, not inert text.
   *
   *  It used to be a `div.message.system` reading "Scroll up for older
   *  messages" — an instruction rather than a control, which is unusable the
   *  moment folding makes the transcript non-scrollable. */
  private updateLoadMoreIndicator(): void {
    // Scoped to the attached view — see maybeLoadMore: parked views keep their
    // own furniture, and removing "the" indicator by document id could reach
    // into one of them.
    const existing = this.viewEl.querySelector(`[id="load-more-indicator"]`);
    if (!this.hasMoreMessages || this.onLoadMore === null) {
      existing?.remove();
      return;
    }
    if (existing !== null) {
      return;
    }
    const btn = el(
      "button",
      { id: "load-more-indicator", className: "load-more-btn", type: "button" },
      "Load older messages",
    );
    btn.addEventListener("click", () => {
      this.maybeLoadMore(true);
    });
    this.viewEl.prepend(btn);
  }
}

// ---------------------------------------------------------------------------
// Singleton instance + the module's public API.
// ---------------------------------------------------------------------------

let instance: ScrollController | null = null;

function getInstance(): ScrollController {
  if (instance === null) {
    instance = new ScrollController($.messages, $.messagesWrap);
    instance.init();
  }
  return instance;
}

/** Deferred DOM access — safe to import before DOMContentLoaded. */
export function getScrollEl(): HTMLElement {
  return getInstance().scrollEl;
}

/** How far the transcript can scroll, in px; 0 when it fits its viewport. */
export function scrollableBy(): number {
  return getInstance().scrollableBy();
}

export function setUserScrolledUp(v: boolean): void {
  getInstance().setUserScrolledUp(v);
}
/** Hand the scroller to a transcript view (unpark / fresh view). */
export function attach(handle: ViewAttachHandle): void {
  getInstance().attach(handle);
}
/** Snapshot and release the current view's scroll state (park). */
export function detach(): ViewScrollState {
  return getInstance().detach();
}
export function jumpTo(target: HTMLElement, opts?: ScrollIntoViewOptions): void {
  getInstance().jumpTo(target, opts);
}
export function scrollToBottom(): void {
  getInstance().scrollToBottom();
}
export function setLoadMore(fn: (() => void) | null, hasMore: boolean): void {
  getInstance().setLoadMore(fn, hasMore);
}
export function resetScrollState(): void {
  getInstance().resetScrollState();
}
export function readingState(): ReadingState {
  return getInstance().readingState();
}
export function onReadingStateChange(cb: (s: ReadingState) => void): void {
  getInstance().onReadingStateChange(cb);
}
export function onTranscriptMutate(cb: () => void): () => void {
  return getInstance().onTranscriptMutate(cb);
}
export function setAnchorProvider(fn: (() => HTMLElement | null) | null): void {
  getInstance().setAnchorProvider(fn);
}
export function setResumeLabel(text: string): void {
  getInstance().setResumeLabel(text);
}
export function preserveReadingPosition(mutate: () => void, kind: ShiftKind): void {
  getInstance().preserveReadingPosition(mutate, kind);
}
export function deferWhileReading(mutate: () => void): void {
  getInstance().deferWhileReading(mutate);
}
export function fillViewport(): void {
  getInstance().fillViewport();
}

// Init on load.
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => {
    getInstance();
  });
} else {
  getInstance();
}
