// ---------------------------------------------------------------------------
// Styled tooltip system — adopted from @cplieger/ui-primitives.
//
// The hand-rolled delegated TooltipController was replaced by the library's
// `initTooltips`, configured to keep vibekit's existing `data-tooltip`
// attribute and its cold/warm delay grouping (first tooltip in a cold group
// waits 1000ms; peers show instantly while warm; 500ms cooldown). So every
// existing `data-tooltip="…"` in the HTML/TS keeps working unchanged.
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
    delayCold: 1000,
    delayWarm: 0,
    cooldown: 500,
  });
}
