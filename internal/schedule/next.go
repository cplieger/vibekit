package schedule

import "time"

// NextRunFrom is the ONE derivation of a schedule's next run, used by both the
// runner and the REST view so neither can disagree about when a schedule fires.
//
// The origin is the ANCHOR (last fire or skip), and `notBefore` floors the
// answer: NextRun returns the first occurrence strictly after its argument, so
// an anchor left behind while the container was down would otherwise resolve
// to a slot already in the past. The runner passes a ZERO notBefore because its
// sweep must SEE a stale slot to tell "may still fire" (inside MissGrace) from
// "missed while down"; the view floors at now so a row never names a past time.
func NextRunFrom(s Spec, anchor, notBefore time.Time) (time.Time, error) {
	from := anchor
	if notBefore.After(from) {
		from = notBefore
	}
	return NextRun(s, from)
}
