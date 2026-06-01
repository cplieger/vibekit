// ---------------------------------------------------------------------------
// Scroll-preservation utility for the git multi-repo dashboard.
//
// Each tab module calls preserveGitScroll(paintFn) when re-rendering
// the per-repo sections. The git view's scroll container is
// #git-view (overflow-y: auto); replaceChildren on any descendant
// resets its scrollTop to 0, which the user perceives as "the page
// scrolled to the top after I clicked Clone".
// ---------------------------------------------------------------------------

/** Run `paint` while preserving the scroll position of #git-view.
 *  If the element doesn't exist (e.g. test harness without the git
 *  view rendered), `paint` runs unchanged. */
export function preserveGitScroll(paint: () => void): void {
  const view = document.getElementById("git-view");
  if (view === null) {
    paint();
    return;
  }
  const saved = view.scrollTop;
  paint();
  // Restore on next frame so layout has had a chance to settle.
  // We re-check after paint because `paint` might rebuild a tab
  // panel that affected the available scroll height — clamp to the
  // post-paint max so we don't try to restore past the end.
  requestAnimationFrame(() => {
    const max = view.scrollHeight - view.clientHeight;
    view.scrollTop = Math.min(saved, Math.max(0, max));
  });
}
