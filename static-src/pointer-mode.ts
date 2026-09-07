// ---------------------------------------------------------------------------
// The touch/mouse toggle in the sidebar header.
//
// MODE is what the reader asked for, TIER is what the document is in: this module
// owns the button, `pointer-tier.ts` owns the tier, one-way (no cycle). It gates
// the button on `coarseEverSeen()` and `50-mobile.css` on the viewport; both gates
// hide, so neither can reveal what the other hid.
// ---------------------------------------------------------------------------

import { coarseEverSeen, type PointerTier } from "./device-view.js";
import { $, forceReflow } from "./dom.js";
import { currentTier, setPointerMode } from "./pointer-tier.js";

/** The accessible NAME, stable in both states, because `aria-pressed` is the
 *  state channel and the two must not both carry it (ARIA APG). */
const NAME = "Touch mode";

/** Keyed by the tier IN FORCE. The tooltip has no state channel beside it, so it
 *  carries state plus action. */
const TOOLTIP: Record<PointerTier, string> = {
  fine: "Mouse mode. Switch to touch mode",
  coarse: "Touch mode. Switch to mouse mode",
};

/** What the reader can SEE, which `paint` animates from — null until the first
 *  paint, where there is nothing to slide out. */
let shown: PointerTier | null = null;

/** Bumped by every `paint`, so a settle deferred behind the slide can tell whether
 *  it is still the newest one. Two clicks inside the 350ms window leave the first
 *  closure holding a superseded target, which would otherwise put the glyph the
 *  second click replaced back on screen until that closure converges. */
let generation = 0;

/** The click binding, so a repeat init replaces its listener rather than stacking. */
let bound: AbortController | null = null;

function glyphFor(tier: PointerTier): Element | null {
  return $.pointerModeBtn.querySelector(
    tier === "fine" ? ".pointer-icon-fine" : ".pointer-icon-coarse",
  );
}

/** Show `tier`'s glyph, mark it on `aria-pressed`, and say in the tooltip what a
 *  click would do next.
 *
 *  `transitionend` carries a timeout because reduced motion zeroes the duration
 *  rather than suppressing the transition, and a never-fired event would leave both
 *  glyphs hidden. A mid-slide click is repaired rather than queued: the generation
 *  guard drops the superseded settle and this paint clears what it left. */
function paint(tier: PointerTier): void {
  const btn = $.pointerModeBtn;
  const incoming = glyphFor(tier);
  const other = glyphFor(tier === "fine" ? "coarse" : "fine");
  if (incoming === null) {
    return;
  }
  // An abandoned settle leaves its outgoing glyph parked below its resting
  // position, and that glyph is this paint's INCOMING one, whose classes the settle
  // below never clears.
  incoming.classList.remove("icon-setting", "icon-rising");
  other?.classList.remove("icon-setting", "icon-rising");
  // There is something to slide out only when the other glyph is on screen: the
  // first paint of a load settles at once (nothing has been drawn yet), and so does
  // a repeat of the tier already shown or a click that arrives mid-slide.
  const outgoing =
    shown !== null && other !== null && !other.classList.contains("hidden") ? other : null;
  shown = tier;
  generation += 1;
  const mine = generation;

  let settled = false;
  const settle = (): void => {
    if (settled || mine !== generation) {
      return;
    }
    settled = true;
    if (other !== null) {
      other.classList.add("hidden");
      other.classList.remove("icon-setting");
    }
    incoming.classList.remove("hidden");
    incoming.classList.add("icon-rising");
    forceReflow(incoming);
    incoming.classList.remove("icon-rising");
  };

  if (outgoing === null) {
    settle();
  } else {
    outgoing.classList.add("icon-setting");
    outgoing.addEventListener("transitionend", settle, { once: true });
    setTimeout(settle, 350);
  }

  btn.setAttribute("aria-pressed", tier === "coarse" ? "true" : "false");
  btn.setAttribute("aria-label", NAME);
  btn.setAttribute("data-tooltip", TOOLTIP[tier]);
}

/** Show the toggle. Called from the composition root the first time this device
 *  reports a coarse pointer; the button is authored HTML, so this is a class
 *  removal rather than an insertion and it is idempotent. */
export function revealPointerModeToggle(): void {
  $.pointerModeBtn.classList.remove("hidden");
}

/** Wire the toggle. `initPointerTier` must already have run, since the button
 *  reports the tier that is in force. */
export function initPointerModeToggle(): void {
  const btn = $.pointerModeBtn;
  btn.classList.toggle("hidden", !coarseEverSeen());
  shown = null;
  // No attribute means nothing has applied a tier, which is the state the authored
  // markup and 01-tokens.css's no-JS fallback both read as the compact one.
  paint(currentTier() ?? "fine");

  bound?.abort();
  bound = new AbortController();
  btn.addEventListener(
    "click",
    () => {
      const next: PointerTier = currentTier() === "coarse" ? "fine" : "coarse";
      setPointerMode(next);
      paint(next);
    },
    { signal: bound.signal },
  );
}
