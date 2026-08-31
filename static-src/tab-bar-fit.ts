// ---------------------------------------------------------------------------
// Tab-bar label fitting: the segmented pill bar (settings / git / docs) keeps
// its labels while they fit and swaps EVERY segment to its centered icon the
// moment any label would truncate. Replaces the mobile native <select>, which
// read as foreign chrome (user ruling: keep the pill bar; icons when text
// does not fit).
//
// The decision is MEASURED per bar, never a breakpoint: which labels fit
// depends on the label set and the container, so a fixed width would be
// wrong for one bar or another (git's three labels fit a phone, docs' six do
// not). Truncation is detected per segment (scrollWidth > clientWidth — the
// segments are flex: 1 + min-width: 0 + overflow: hidden, so overflow shows
// as ellipsis, never wrapping), and one truncated label flips the whole bar:
// a row mixing text pills and icon pills reads as broken.
// ---------------------------------------------------------------------------

/** Bars are re-measured in label mode, so the icons class comes off before
 *  reading. The remove + read + toggle runs synchronously inside one frame —
 *  no paint happens between them, so there is no visible flicker. */
const ICONS_CLASS = "tab-bar-icons";

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
