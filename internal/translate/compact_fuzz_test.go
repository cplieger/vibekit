package translate

import "testing"

// FuzzCompactionStatusValid exercises CompactionStatus.Valid() with
// arbitrary strings. Invariant: only the three known constants return
// true; all other inputs must return false.
func FuzzCompactionStatusValid(f *testing.F) {
	f.Add("started")
	f.Add("completed")
	f.Add("failed")
	f.Add("")
	f.Add("unknown")
	f.Add("STARTED")
	f.Add("Started")
	f.Add("completed\x00")
	f.Add(" started")
	f.Add("failed ")

	known := map[CompactionStatus]bool{
		CompactionStarted:   true,
		CompactionCompleted: true,
		CompactionFailed:    true,
	}

	f.Fuzz(func(t *testing.T, s string) {
		cs := CompactionStatus(s)
		got := cs.Valid()
		want := known[cs]
		if got != want {
			t.Fatalf("CompactionStatus(%q).Valid() = %v, want %v", s, got, want)
		}
	})
}

// FuzzIsSubagentNoiseTitle exercises the noise-title lookup with
// arbitrary inputs. Invariant: only the exact titles in
// subagentNoiseRules return true; all other inputs return false.
func FuzzIsSubagentNoiseTitle(f *testing.F) {
	f.Add("Summarizing")
	f.Add("Spawning agent crew")
	f.Add("subagent")
	f.Add("summary")
	f.Add("")
	f.Add("summarizing") // case differs
	f.Add("SUBAGENT")
	f.Add("Summarizing ")
	f.Add(" Summarizing")
	f.Add("subagent\x00")
	f.Add("unknown title")

	f.Fuzz(func(t *testing.T, title string) {
		got := IsSubagentNoiseTitle(title)
		_, expected := subagentNoiseTitles[title]
		if got != expected {
			t.Fatalf("IsSubagentNoiseTitle(%q) = %v, want %v", title, got, expected)
		}
	})
}
