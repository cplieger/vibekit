package version

import "testing"

// TestBuildDefaultsToDev pins the documented default: built outside the image
// (plain go build / go test, with no -ldflags "-X ...version.Build=<tag>"),
// Build stays "dev" so callers can distinguish development from release.
func TestBuildDefaultsToDev(t *testing.T) {
	if Build != "dev" {
		t.Errorf("Build = %q, want %q (the default for non-release builds)", Build, "dev")
	}
}
