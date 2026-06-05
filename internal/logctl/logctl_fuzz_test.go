package logctl

import "testing"

// FuzzSetDebug exercises the level toggle with arbitrary boolean values.
// Primarily validates no race or panic under rapid toggling.
func FuzzSetDebug(f *testing.F) {
	f.Add(true)
	f.Add(false)

	f.Fuzz(func(t *testing.T, on bool) {
		SetDebug(on)
	})
}
