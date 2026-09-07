// ---------------------------------------------------------------------------
// Tab-bar label fitting: the segmented pill bar (settings / git / docs) shows
// an icon before every label, then hides EVERY label when one would truncate.
// This replaces the mobile native <select>, which read as foreign chrome.
//
// The decision is MEASURED per bar, never a breakpoint: which labels fit
// depends on the label set and the container. Truncation is detected per
// segment (scrollWidth > clientWidth); one truncated label flips the whole
// bar because mixed icon-plus-label and icon-only segments read as broken.
// ---------------------------------------------------------------------------

/** Bars are measured in icon-plus-label mode, so the icon-only class comes
 *  off before reading. The remove + read + toggle runs synchronously inside
 *  one frame, so there is no visible flicker. */
const ICONS_CLASS = "tab-bar-icons";

/** Set while this bar is VISIBLE and showing its labels, i.e. while it names its
 *  own active section. 12-chat.css reads it to suppress the title bar's subtitle,
 *  which would otherwise print that same section name twice, twenty pixels apart.
 *
 *  Published here rather than derived in CSS because the derivation needs the
 *  bar's visibility as well as its label mode, and a `:has()` chain carrying both
 *  ran past stylelint's complexity ceiling — which was the right complaint: this
 *  module already measures, so it is the one place that knows. */
const NAMED_CLASS = "tab-bar-named";

function measure(bar: HTMLElement): void {
  bar.classList.remove(ICONS_CLASS);
  let overflows = false;
  for (const seg of bar.querySelectorAll<HTMLElement>(".settings-tab")) {
    // A hidden bar (display: none view) reports 0/0 — no overflow, label
    // mode. The ResizeObserver fires again when the view shows and the bar
    // gains real geometry, so hidden bars self-correct on reveal.
    if (seg.scrollWidth > seg.clientWidth) {
      overflows = true;
      break;
    }
  }
  bar.classList.toggle(ICONS_CLASS, overflows);

  // `offsetParent` is null under a `display: none` ancestor, which is how every
  // inactive view's bar reports. A view switch changes this bar's width (to and
  // from 0), so the ResizeObserver re-runs `measure` and the class follows the
  // visible view with no extra wiring.
  bar.classList.toggle(NAMED_CLASS, !overflows && bar.offsetParent !== null);
}

/** Watch a tab bar and keep its label/icon mode fitted to its width.
 *  Call once per bar at init; the observer lives for the app's lifetime
 *  (the three bars are static singletons, so there is nothing to release). */
export function fitTabBar(bar: HTMLElement): void {
  // Re-measure on WIDTH changes only: toggling the class changes the bar's
  // height (icons and labels differ a few px), so an unfiltered callback
  // re-fires once per toggle — a benign but noisy RO loop.
  let lastWidth = -1;
  const ro = new ResizeObserver((entries) => {
    const w = entries[entries.length - 1]?.contentRect.width ?? -1;
    if (w === lastWidth) {
      return;
    }
    lastWidth = w;
    // Deferred a frame: mutating class state inside the RO delivery cycle
    // (labels hide, bar height moves) triggers the browser's benign-but-noisy
    // "loop completed with undelivered notifications" console error.
    requestAnimationFrame(() => {
      measure(bar);
    });
  });
  ro.observe(bar);
  measure(bar);
}
