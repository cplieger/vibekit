package checkpoint

import "testing"

// TestContentAtOrBeforeTag_PriorSnapshotFallback pins the no-exact-
// match fallback: when no snapshot exists at the queried tag,
// contentAtOrBeforeTag returns the afterSHA of the nearest prior
// snapshot (case 1), and reports no content when that afterSHA is empty
// (case 2).
func TestContentAtOrBeforeTag_PriorSnapshotFallback(t *testing.T) {
	// Case 1: a single prior snapshot at index 0 (tag "1"); querying a
	// later tag "2" with no exact match falls back to its afterSHA.
	s := newState()
	s.apply(&event{Kind: kindSnapshot, Turn: 1, Tool: 0, Tag: "1", Path: "f", BeforeSHA: "b1", AfterSHA: "a1"})
	gotSHA, ok := s.contentAtOrBeforeTag("f", "2")
	if !ok || gotSHA != "a1" {
		t.Errorf("contentAtOrBeforeTag(f,2) = (%q,%v), want (a1,true): the nearest prior snapshot's afterSHA is the fallback", gotSHA, ok)
	}

	// Case 2: the nearest prior snapshot has an empty afterSHA, so there
	// is no content to report.
	s2 := newState()
	s2.apply(&event{Kind: kindSnapshot, Turn: 1, Tool: 0, Tag: "1", Path: "g", BeforeSHA: "b2", AfterSHA: ""})
	_, ok2 := s2.contentAtOrBeforeTag("g", "2")
	if ok2 {
		t.Errorf("contentAtOrBeforeTag(g,2) ok = true, want false: an empty afterSHA must not be reported as content")
	}
}

// TestAtoiSafe_LeadingZero pins that atoiSafe keeps parsing past a
// leading zero: the accumulator is 0 after the leading '0', but parsing
// must continue rather than stop mid-string.
func TestAtoiSafe_LeadingZero(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"05", 5},
		{"007", 7},
		{"10", 10},
		{"0", 0},
	}
	for _, tc := range cases {
		if got := atoiSafe(tc.in); got != tc.want {
			t.Errorf("atoiSafe(%q) = %d, want %d: a leading zero must not stop parsing", tc.in, got, tc.want)
		}
	}
}
