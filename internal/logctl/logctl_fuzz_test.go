package logctl

import (
	"log/slog"
	"testing"
)

// FuzzSetDebug exercises the level toggle with arbitrary boolean values and
// pins the postcondition: after SetDebug(on) the shared levelVar must read
// Debug when on and Info otherwise (and no panic under rapid toggling).
func FuzzSetDebug(f *testing.F) {
	f.Add(true)
	f.Add(false)

	f.Fuzz(func(t *testing.T, on bool) {
		SetDebug(on)
		want := slog.LevelInfo
		if on {
			want = slog.LevelDebug
		}
		if got := snapshotLevel(); got != want {
			t.Errorf("SetDebug(%v) -> level %v, want %v", on, got, want)
		}
	})
}
