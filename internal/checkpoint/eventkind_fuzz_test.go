package checkpoint

import "testing"

// FuzzEventKindValid exercises eventKind.valid() with arbitrary strings
// and asserts it returns true only for the known event kinds.
func FuzzEventKindValid(f *testing.F) {
	f.Add("turn_start")
	f.Add("snapshot")
	f.Add("restore")
	f.Add("restore_started")
	f.Add("restore_committed")
	f.Add("conflict_detected")
	f.Add("")
	f.Add("unknown")
	f.Add("SNAPSHOT")

	f.Fuzz(func(t *testing.T, s string) {
		got := eventKind(s).valid()
		want := s == "turn_start" || s == "snapshot" || s == "restore" ||
			s == "restore_started" || s == "restore_committed" || s == "conflict_detected"
		if got != want {
			t.Errorf("eventKind(%q).valid() = %v, want %v", s, got, want)
		}
	})
}
