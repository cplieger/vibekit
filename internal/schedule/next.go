package schedule

import "time"

// NextRunFrom is the ONE derivation of a schedule's next run. The runner and the
// REST view both go through it, because a row that renders one time while the
// runner honours another is a lie the reader cannot see through: the row said
// "next 02:00 tomorrow" while the runner was about to fire a slot from last week.
//
// The origin is the ANCHOR — the last fire, or the last skip — because that is
// what the recurrence measures from. `notBefore` then floors the answer.
//
// That floor is the load-bearing half, and the reason this is not simply
// NextRun(s, e.Anchor). NextRun returns the first occurrence strictly AFTER its
// argument, so an anchor left behind while the container was down resolves to a
// slot that has already passed, and a row rendering it advertises a next run in
// the past. Flooring is also what the runner arrives at on its own over two
// ticks (skip the stale slot, then recompute from now), so doing it here makes
// the display agree with the runner immediately instead of one tick later.
//
// A ZERO notBefore floors nothing, and that is what the runner passes: sweep has
// to SEE a slot that has already gone, to tell one it may still fire (inside
// MissGrace) from one missed while the container was down. The view floors at
// now, because a row must never name a time that has passed.
func NextRunFrom(s Spec, anchor, notBefore time.Time) (time.Time, error) {
	from := anchor
	if notBefore.After(from) {
		from = notBefore
	}
	return NextRun(s, from)
}
