package version

import "testing"

func FuzzBuildAccess(f *testing.F) {
	f.Add(0)

	f.Fuzz(func(t *testing.T, _ int) {
		if Build == "" {
			t.Fatal("Build must never be empty")
		}
	})
}
