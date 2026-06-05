package api

import (
	"testing"
)

// FuzzFindAllowOnce targets the permission option selector that finds
// the "allow_once" option for auto-approval flows. Bug class: returning
// the wrong option_id when multiple options match partially, or missing
// a match when Kind and OptionID both independently qualify — could
// cause the bridge to auto-approve with the wrong option or fail to
// find a valid allow-once when one exists.
func FuzzFindAllowOnce(f *testing.F) {
	f.Add("opt1", "allow_once", "allow_once", "opt2", "deny", "deny_always")
	f.Add("", "", "", "", "", "")
	f.Add("allow_once", "allow_once", "other", "x", "y", "z")
	f.Add("a", "b", "allow_once", "c", "allow_once", "allow_once")

	f.Fuzz(func(t *testing.T, id1, kind1, optID1, id2, kind2, optID2 string) {
		opts := []PermissionOption{
			{OptionID: id1, Kind: kind1, Name: optID1},
			{OptionID: id2, Kind: kind2, Name: optID2},
		}

		result := FindAllowOnce(opts)

		// Determine which option the function should return (first-match semantics).
		var expectedResult string
		expectedFound := false
		for _, opt := range opts {
			if opt.Kind == PermissionKindAllowOnce || opt.OptionID == PermissionKindAllowOnce {
				expectedResult = opt.OptionID
				expectedFound = true
				break
			}
		}

		// Invariant 1: result must match the expected first-match result.
		if result != expectedResult {
			t.Fatalf("FindAllowOnce = %q, want %q", result, expectedResult)
		}

		// Invariant 2: if no option qualifies, result must be empty.
		if !expectedFound && result != "" {
			t.Fatalf("FindAllowOnce returned %q but no option qualifies", result)
		}

		// Invariant 3: result is deterministic.
		result2 := FindAllowOnce(opts)
		if result != result2 {
			t.Fatalf("FindAllowOnce not deterministic: %q vs %q", result, result2)
		}
	})
}
