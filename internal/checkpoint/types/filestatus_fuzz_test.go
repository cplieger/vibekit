package types

import "testing"

// FuzzFileStatusValid exercises FileStatus.Valid() with arbitrary
// strings and asserts it returns true only for the three known
// statuses: "A", "M", "D".
func FuzzFileStatusValid(f *testing.F) {
	f.Add("A")
	f.Add("M")
	f.Add("D")
	f.Add("")
	f.Add("X")
	f.Add("AM")
	f.Add("a")

	f.Fuzz(func(t *testing.T, s string) {
		got := FileStatus(s).Valid()
		want := s == "A" || s == "M" || s == "D"
		if got != want {
			t.Errorf("FileStatus(%q).Valid() = %v, want %v", s, got, want)
		}
	})
}
