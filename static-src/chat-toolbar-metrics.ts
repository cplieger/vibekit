// Publishes the chat toolbar's rendered width as `--chat-toolbar-w`, which is
// what lets `.banner-stack` (13-messages.css) share the toolbar's row and stop at
// its left edge. CSS cannot ask how wide another element is, and the two are not
// in one flex row on purpose: the band stays inside `#chat-view` so hiding a
// non-active view still hides it, which a shared wrapper in `#chat-area` would
// have cost.

/** Last value written, so an unchanged measurement costs no style recalc. */
let published = "";

/** `#find-btn` collapses and expands per active tab kind, animated over
 *  `--dur-exit` (12-chat.css), so the toolbar's width genuinely changes at
 *  runtime and a one-shot measurement goes stale on the first tab switch. Rounded
 *  UP because the value is a CLEARANCE: a fractional width floored would leave
 *  the band's end a sub-pixel under the toolbar, which is the overlap its
 *  `inset-inline-end` exists to prevent.
 *
 *  At `width <= 48rem` the toolbar is full-width and the band is back in flow, so
 *  the value is published and unread there. */
function publish(el: Element): void {
  const next = `${String(Math.ceil(el.getBoundingClientRect().width))}px`;
  if (next === published) {
    return;
  }
  published = next;
  document.documentElement.style.setProperty("--chat-toolbar-w", next);
}

/** Measure once and keep measuring. Called from the composition root at boot, so
 *  the property is set before any banner can arrive over SSE — without it
 *  `.banner-stack`'s `0px` fallback would run the band under the toolbar. */
export function initChatToolbarMetrics(): void {
  const toolbar = document.querySelector(".chat-toolbar");
  if (toolbar === null) {
    return;
  }
  publish(toolbar);
  new ResizeObserver(() => {
    publish(toolbar);
  }).observe(toolbar);
}
