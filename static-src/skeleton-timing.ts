// ---------------------------------------------------------------------------
// Anti-flicker timing for loading skeletons.
// ---------------------------------------------------------------------------

/** Defer showing a loading skeleton so a fast load never flashes it.
 *
 *  Calls `show` after `delayMs` (default 150ms) unless the returned `cancel`
 *  fires first. `show` performs the reveal (e.g. create + append the skeleton)
 *  and returns its own teardown (e.g. remove that element). `cancel` clears a
 *  still-pending timer and, if `show` already ran, invokes its teardown — so
 *  the caller neither tracks "was it shown" nor removes the skeleton itself.
 *  `cancel` is idempotent: the teardown runs at most once.
 *
 *  Show-delay only, deliberately no minimum-visible time: a skeleton that
 *  shares its container with the real content must be removed the instant the
 *  load completes, otherwise it would overlap the freshly-rendered content. */
export function deferSkeleton(show: () => () => void, delayMs = 150): () => void {
  let teardown: (() => void) | undefined;
  let done = false;
  const timer = setTimeout(() => {
    teardown = show();
  }, delayMs);
  return function cancel(): void {
    if (done) {
      return;
    }
    done = true;
    clearTimeout(timer);
    teardown?.();
  };
}
