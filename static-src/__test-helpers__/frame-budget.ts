// Failure bounds for a test whose subject is FRAMES.
//
// A browser running a large suite can deliver animation frames at 1Hz instead of
// 60Hz. Measured in this suite with a per-file probe: the median rAF gap is a
// clean 16.7ms for the first ~49 files and then a flat 1016ms for every file
// after, while the page still reports `visibility: visible` — a throttle, not
// contention, which is why the number is dead flat rather than noisy. It does not
// reproduce with a subset, so no single test causes it and no test can undo it.
//
// The consequence is only ever a BOUND. A ResizeObserver re-measure, a reveal
// cadence, an animation restart and a scheduled retry all still happen; they take
// a second per frame instead of 16ms. So a budget sized in frames × 16ms fails a
// path that works, which is a false red, and the fix is to size it in seconds per
// frame waited for.
//
// These are failure bounds, never targets: every consumer either polls on the
// product's own output or awaits a fixed number of frames, so a working path
// never spends one and the wide bound costs nothing.
//
// Each consumer must ALSO keep its per-test timeout above the budget it uses, or
// vitest's 5s default preempts the wait and the failure reads as a bare timeout
// instead of naming the assertion that was wrong.

/** Rough wall-clock cost of one frame delivery when the throttle is in force. */
const THROTTLED_FRAME_MS = 1_200;

/** Budget for a poll that settles within a handful of frames. */
export const FRAME_BUDGET_MS = 10_000;

/** Budget for a wait of `frames` fixed frames, floored at {@link FRAME_BUDGET_MS}. */
export function framesBudgetMs(frames: number): number {
  return Math.max(FRAME_BUDGET_MS, frames * THROTTLED_FRAME_MS);
}

/** A per-test timeout that cannot preempt `budget`. */
export function testTimeoutFor(budget: number): number {
  return budget + 5_000;
}
