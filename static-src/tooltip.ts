// ---------------------------------------------------------------------------
// Styled tooltip system — adopted from @cplieger/ui-primitives.
//
// The hand-rolled delegated TooltipController was replaced by the library's
// `initTooltips`, configured to keep vibekit's existing `data-tooltip`
// attribute. So every existing `data-tooltip="…"` in the HTML/TS keeps working
// unchanged.
//
// What the library adds over the old copy: a shared placement engine (flip
// below when there is no room above, viewport clamp, visualViewport-aware for
// the mobile keyboard), aria-describedby TOKEN-LIST preservation (it appends
// its id rather than clobbering any token the app set), `\n`→<br> line breaks,
// and re-parenting the tip into an open ancestor <dialog> so a tooltip on a
// control inside a modal shares that dialog's top layer instead of rendering
// behind it.
//
// Visuals are unchanged: the .uip-tooltip skin (css/04-uip-skin.css) ports the
// old .vk-tooltip look 1:1 (dark chip, vk-fade-in enter, 80ms ease-exit fade).
// ---------------------------------------------------------------------------

import { initTooltips as uipInitTooltips } from "@cplieger/ui-primitives/tooltip";

/** Hover time every tooltip waits out, matching a native `title`: Firefox's
 *  `ui.tooltipDelay` default is 500ms and the Windows mouse-hover time is
 *  400ms. ONE value for every hover, cold or warm — a native tooltip has no
 *  concept of a warm group, and the warm path was what made vibekit's tooltips
 *  read as instant: the group stayed warm for `cooldown + delayCold` after a
 *  show, so every neighbouring pill and toolbar button popped with no delay
 *  once one had. */
const HOVER_DELAY_MS = 500;

/** Install the delegated tooltip controller once. Idempotent (the library
 *  guards re-initialization internally).
 *
 *  Focus-triggered tooltips are keyboard-only: ui-primitives >= 2.1.2 gates
 *  its focusin path on `:focus-visible`, so programmatic focus (a modal
 *  focusing its first control, a focus-trap restoring focus) no longer pops
 *  tooltips. */
export function initTooltips(): void {
  uipInitTooltips({
    attribute: "data-tooltip",
    // Both delays restate what ui-primitives 3.0.1 makes the default
    // (`delayWarm` follows `delayCold`, 500ms). The pin is still 3.0.0, whose
    // defaults are 1000/0 — the instant-peer shape — so the values have to be
    // passed to get the behavior. Drop both lines when the pin reaches 3.0.1.
    delayCold: HOVER_DELAY_MS,
    delayWarm: HOVER_DELAY_MS,
  });
}
