// Where the exec view seats a reconciled child, and why it refuses to re-seat one.

/** Put `node` at `index` inside `into`, touching the DOM only when it is not already
 *  there.
 *
 *  `appendChild` on an ALREADY-ATTACHED node is a remove plus an insert, so it restarts
 *  every CSS animation in the subtree it moves and drops the reader state that lives on
 *  the element — `:hover`, and focus. The tree pane re-seated every row on every render
 *  and a live run renders on each store bump, so a running row's `vk-spin` ring was
 *  knocked back to its start angle 2.58 times a second against the 600ms it needs for
 *  one revolution: it never completed a turn, which is what a reader reports as the
 *  spinner freezing and restarting. Measured on the live app, pointer parked away from
 *  every row; writing the same row's attributes in that pass restarted nothing, so the
 *  re-seat was the whole of it. `detail.ts` records the same class for its own host
 *  seating, where the cost was a blurred link instead.
 *
 *  Correct because both callers place a parent's children in ASCENDING index order:
 *  every earlier index is already occupied by its own child, so `node` is either
 *  already at `index` or somewhere after it, and both land right. Called out of order
 *  it would be off by one, so keep the loops ascending. */
export function place(into: HTMLElement, node: HTMLElement, index: number): void {
  const current = into.childNodes[index];
  if (current === node) {
    return;
  }
  into.insertBefore(node, current ?? null);
}
