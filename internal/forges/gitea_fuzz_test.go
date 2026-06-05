package forges

import "testing"

// FuzzMapGiteaStatus verifies mapGiteaStatus maps CI status strings to a known
// set or returns the input unchanged.
//
// Bug class: case-sensitivity bypass, unexpected empty-string passthrough.
func FuzzMapGiteaStatus(f *testing.F) {
	f.Add("pending")
	f.Add("running")
	f.Add("success")
	f.Add("failure")
	f.Add("error")
	f.Add("warning")
	f.Add("")
	f.Add("PENDING")

	f.Fuzz(func(t *testing.T, s string) {
		result := mapGiteaStatus(s)

		// Invariant: result is one of the known mapped values or the original input.
		switch result {
		case "queued", "in_progress", stateCompleted, s:
			// ok
		default:
			t.Fatalf("mapGiteaStatus(%q) = %q; unexpected value", s, result)
		}
	})
}

// FuzzMapGiteaConclusion verifies mapGiteaConclusion returns only known
// conclusion values or empty string.
//
// Bug class: unmapped values returning non-empty garbage, case bypass.
func FuzzMapGiteaConclusion(f *testing.F) {
	f.Add("success")
	f.Add("failure")
	f.Add("error")
	f.Add("warning")
	f.Add("")
	f.Add("unknown")
	f.Add("SUCCESS")

	f.Fuzz(func(t *testing.T, s string) {
		result := mapGiteaConclusion(s)

		// Invariant: result is one of the known values.
		switch result {
		case statusSuccess, statusFailure, stateSkipped, "":
			// ok
		default:
			t.Fatalf("mapGiteaConclusion(%q) = %q; unexpected value", s, result)
		}
	})
}
