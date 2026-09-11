// The clamp: a text element capped to N lines, with a show-more that opens it.
//
// Serves the in-turn steer note, the dock's steer row, the run page's
// instructions and results, and both of the dock's question cards. The turn
// header is NOT among them: its clamp is CSS-only and fold-conditional, so it
// carries no constant and attaches no observation. The two measured facts that
// decide the shape are on `watchClamp` below.

/** Above this many characters, assume the text overflows when layout cannot be
 *  measured — a first guess only, corrected by the observer. Deliberately
 *  generous: a false positive shows an unneeded opener for one frame, a false
 *  negative makes a long text unreadable for one. */
const CLAMP_FALLBACK_CHARS = 220;

/** Lines a clamp shows when the caller states none. No consumer relies on it —
 *  every one of them states its own count — so this is a floor rather than any
 *  surface's number. */
const CLAMP_LINES = 3;

const LABEL_MORE = "Show more";
const LABEL_LESS = "Show less";

export interface ClampOptions {
  /** Lines the STYLESHEET clamps to; read here by the character fallback only. */
  readonly lines?: number;
  /** Character threshold for the no-layout guess. */
  readonly fallbackChars?: number;
  /** Where the caller keeps the expanded flag, when it keeps one. */
  readonly isExpanded?: () => boolean;
  readonly setExpanded?: (on: boolean) => void;
}

export interface ClampHandle {
  /** Re-decide the opener against the current text, PRESERVING an expansion. */
  sync(): void;
  /** Collapse and forget any expansion — for content that has changed. */
  collapse(): void;
  /** Stop clamping: no attribute, no opener. Reversed by the next `sync`. */
  disable(): void;
}

interface ClampState {
  readonly more: HTMLButtonElement;
  readonly lines: number;
  readonly fallbackChars: number;
  readonly isExpanded: () => boolean;
  readonly setExpanded: (on: boolean) => void;
  /** Set by `disable`: a resize must not put the clamp back. */
  off: boolean;
}

interface ClampEntry {
  readonly state: ClampState;
  readonly handle: ClampHandle;
}

const clamps = new WeakMap<HTMLElement, ClampEntry>();

/** Every element the shared observer is watching. No new retention: the observer already
 *  holds each target strongly. It exists because a WeakMap cannot be enumerated and a
 *  subtree sweep has to be. */
const observed = new Set<HTMLElement>();

/** Clamp `text` behind `more`, and return the handle a repaint drives it with.
 *
 *  Idempotent: a repeat call returns the existing handle rather than wiring a
 *  second listener, so a caller holding only the element re-derives its handle.
 *
 *  Where the expanded flag is STORED stays the caller's: `turn-header.ts` keeps it
 *  on the header so a user expansion survives a repaint. */
export function attachClamp(
  text: HTMLElement,
  more: HTMLButtonElement,
  opts: ClampOptions = {},
): ClampHandle {
  const found = clamps.get(text);
  if (found !== undefined) {
    return found.handle;
  }

  let localExpanded = false;
  const state: ClampState = {
    more,
    lines: opts.lines ?? CLAMP_LINES,
    fallbackChars: opts.fallbackChars ?? CLAMP_FALLBACK_CHARS,
    isExpanded: opts.isExpanded ?? ((): boolean => localExpanded),
    setExpanded:
      opts.setExpanded ??
      ((on: boolean): void => {
        localExpanded = on;
      }),
    off: false,
  };
  const handle: ClampHandle = {
    sync: () => {
      state.off = false;
      reviewClamp(text, state);
    },
    collapse: () => {
      state.off = false;
      writeExpanded(text, state, false);
      reviewClamp(text, state);
    },
    disable: () => {
      state.off = true;
      text.removeAttribute("data-clamped");
      more.hidden = true;
    },
  };
  clamps.set(text, { state, handle });

  more.hidden = true;
  more.addEventListener("click", () => {
    writeExpanded(text, state, !state.isExpanded());
  });
  watchClamp(text);
  reviewClamp(text, state);
  return handle;
}

/** Decide whether the opener is needed, and keep the clamp attribute in sync.
 *  Measurement is the truth when layout is available; the character fallback
 *  covers the no-layout case, so a long text is never clamped with no way to open
 *  it. */
function reviewClamp(text: HTMLElement, s: ClampState): void {
  if (s.off) {
    return;
  }
  if (s.isExpanded()) {
    s.more.hidden = false;
    return;
  }
  text.setAttribute("data-clamped", "");
  s.more.textContent = LABEL_MORE;
  s.more.setAttribute("aria-expanded", "false");

  const measured = text.scrollHeight;
  const visible = text.clientHeight;
  const body = text.textContent;
  const overflows =
    measured > 0 && visible > 0
      ? measured - visible > 1
      : body.length > s.fallbackChars || countLines(body) > s.lines;
  s.more.hidden = !overflows;
}

/** Open or close the clamp, and hand the flag to whoever stores it. */
function writeExpanded(text: HTMLElement, s: ClampState, on: boolean): void {
  s.setExpanded(on);
  if (on) {
    text.removeAttribute("data-clamped");
  } else {
    text.setAttribute("data-clamped", "");
  }
  s.more.textContent = on ? LABEL_LESS : LABEL_MORE;
  s.more.setAttribute("aria-expanded", on ? "true" : "false");
}

/** Stop clamping `text` and release its observation. PRECONDITION, the caller's: the
 *  element is being DISCARDED, so release at a teardown and never at a repaint.
 *  `attachClamp` is idempotent through the `clamps` entry, so releasing a LIVE element
 *  that is re-attached later wires a SECOND click listener and one press toggles twice.
 *  Explicit rather than callback-driven because WebKit may never deliver the final
 *  zero-size entry, and a parked view's `content-visibility: hidden` defers it while an
 *  EVICTED view never un-parks. */
export function releaseClamp(text: HTMLElement): void {
  clampWatcher?.unobserve(text);
  observed.delete(text);
  clamps.delete(text);
  // A discarded element leaves no scheduled write behind either, which is the same
  // precondition the observation itself has.
  pendingReview.delete(text);
  if (pendingReview.size === 0 && reviewFrame !== 0) {
    cancelAnimationFrame(reviewFrame);
    reviewFrame = 0;
  }
}

/** Release every clamp inside `root`, `root` itself included. Same precondition as
 *  {@link releaseClamp}. A sweep rather than a per-element call keeps the owner count at
 *  one per teardown, and lets `disposeChatView` cover every steer note in a chat. */
export function releaseClampsIn(root: HTMLElement): void {
  for (const text of [...observed]) {
    if (root === text || root.contains(text)) {
      releaseClamp(text);
    }
  }
}

/** How many elements the shared observer is watching. Test-only: `knip.json` treats every
 *  `*.test.ts` as an entry, so a test-consumed export is not an unused one. */
export function clampObservationCount(): number {
  return observed.size;
}

let clampWatcher: ResizeObserver | undefined;

/** One observer for every clamped element, because measurement is the only honest
 *  answer to "does this overflow N lines".
 *
 *  A DETACHED element measures 0 on both sides, so the first verdict can only be
 *  the character guess, and nothing revisited it: a settled block gets no repaint.
 *  A resize callback is delivered AFTER layout and BEFORE paint, so the first
 *  lands in the insertion frame and the guess is never painted — and it lands at
 *  every width, which measure-once could not. */
function watchClamp(text: HTMLElement): void {
  clampWatcher ??= new ResizeObserver((entries) => {
    for (const entry of entries) {
      // BELT AND BRACES, not the mechanism: an element discarded without a
      // `releaseClamp` is swept if this callback happens to arrive. See `releaseClamp`.
      if (!entry.target.isConnected) {
        releaseClamp(entry.target as HTMLElement);
        continue;
      }
      scheduleReview(entry.target as HTMLElement);
    }
  });
  clampWatcher.observe(text);
  observed.add(text);
}

/** Elements whose resize-driven review is waiting on `reviewFrame`. A set, because one
 *  callback carries every element whose box moved. */
const pendingReview = new Set<HTMLElement>();

/** The single slot the deferred review is held in. */
let reviewFrame = 0;

/** Defer the resize-driven review one animation frame, behind a single slot.
 *
 *  THE VERDICT MAY NOT BE WRITTEN INSIDE THE RESIZE DELIVERY. `more.hidden` is
 *  `display: none`, so a flip changes the height of the card the opener sits in, and
 *  that card is observed at a SHALLOWER depth by `scroll.ts`'s per-child set: both are
 *  gathered into ONE broadcast, so re-activating the card's observation fails the
 *  `depth > shallowest` test and the engine reports "ResizeObserver loop completed with
 *  undelivered notifications". An unchanged-value guard cannot stand in for the
 *  deferral — `hidden = false` on an unhidden element already dirties nothing, so the
 *  write that reaches layout is a real state change. Same shape as `scroll.ts`'s
 *  `scheduleScrollbarWidth`; the synchronous `reviewClamp` calls in `attachClamp` and
 *  on the handle are outside any delivery and are what paint the first guess. */
function scheduleReview(text: HTMLElement): void {
  pendingReview.add(text);
  if (reviewFrame !== 0) {
    return;
  }
  reviewFrame = requestAnimationFrame(() => {
    reviewFrame = 0;
    const due = [...pendingReview];
    pendingReview.clear();
    for (const target of due) {
      // A detached element measures 0 on both sides, so the character guess would
      // answer for one discarded between the delivery and this frame.
      if (!target.isConnected) {
        continue;
      }
      const found = clamps.get(target);
      if (found !== undefined) {
        reviewClamp(target, found.state);
      }
    }
  });
}

function countLines(s: string): number {
  let n = 1;
  for (const ch of s) {
    if (ch === "\n") {
      n++;
    }
  }
  return n;
}
