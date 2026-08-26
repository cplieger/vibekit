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

/** The reader's position, as a state rather than an inferred boolean. */
export type ReadingState = "following" | "reading";

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
  private readonly messagesEl: HTMLElement;
  readonly scrollEl: HTMLElement;

  private state: ReadingState = "following";
  private userScrollingUntil = 0;

  private hasMoreMessages = false;
  private loadingMore = false;
  private onLoadMore: (() => void) | null = null;

  /** Mutations that were postponed because the reader is Reading. Applied in
   *  arrival order on the return to Following. */
  private deferred: (() => void)[] = [];
  private stateListeners: ((s: ReadingState) => void)[] = [];
  /** Supplies the element Following should keep visible while a turn streams.
   *  Null (or a null return) falls back to the document bottom. */
  private anchorProvider: (() => HTMLElement | null) | null = null;

  private rafPending = false;

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

  constructor(messagesEl: HTMLElement, scrollEl: HTMLElement) {
    this.messagesEl = messagesEl;
    this.scrollEl = scrollEl;
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
      this.autoScrollIfAnchored();
    });
    mutationObserver.observe(this.messagesEl, {
      childList: true,
      subtree: true,
      characterData: true,
    });

    const resizeObserver = new ResizeObserver(() => {
      // Re-measured here rather than on `window.resize`: this fires AFTER layout
      // and only when the scroller's content box actually moved, which is exactly
      // when the reserved gutter can have changed (browser zoom moves its width in
      // CSS pixels; a classic bar swapped for an overlay one frees all 10px). A
      // window listener read the pre-relayout value and left the bar reserving a
      // strip that no longer existed. `stable` means overflow alone never resizes
      // this box, so streaming costs no extra writes.
      this.publishScrollbarWidth();
      this.autoScrollIfAnchored();
    });
    resizeObserver.observe(this.scrollEl);
    const observed = new Set<Element>();
    const reobserveChildren = (): void => {
      const current = new Set<Element>(this.messagesEl.children);
      for (const child of observed) {
        if (!current.has(child)) {
          resizeObserver.unobserve(child);
          observed.delete(child);
        }
      }
      for (const child of current) {
        if (!observed.has(child)) {
          resizeObserver.observe(child);
          observed.add(child);
        }
      }
    };
    reobserveChildren();
    const childObserver = new MutationObserver(reobserveChildren);
    childObserver.observe(this.messagesEl, { childList: true });
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
    this.setState("following");
    this.userScrollingUntil = 0;
    this.scrollSelfTo(this.scrollEl.scrollHeight, "smooth");
  }

  /** Enter Reading explicitly — a collapse the user asked for parks them on the
   *  content above it, and nothing may then move under them. */
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
    this.setState("following");
    this.userScrollingUntil = 0;
    requestAnimationFrame(() => {
      this.scrollSelfTo(this.scrollEl.scrollHeight, "instant");
    });
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
    if (Date.now() < this.userScrollingUntil) {
      return;
    }
    if (this.rafPending) {
      return;
    }
    this.rafPending = true;
    requestAnimationFrame(() => {
      this.rafPending = false;
      const anchor = this.anchorProvider?.() ?? null;
      const target = anchor === null ? this.scrollEl.scrollHeight : this.anchorTop(anchor);
      this.scrollSelfTo(target, "instant");
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
    const indicator = document.getElementById("load-more-indicator");
    if (indicator !== null) {
      indicator.replaceWith(skel);
    } else {
      this.messagesEl.prepend(skel);
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
    observer.observe(this.messagesEl, { childList: true });
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
    const existing = document.getElementById("load-more-indicator");
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
    this.messagesEl.prepend(btn);
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
