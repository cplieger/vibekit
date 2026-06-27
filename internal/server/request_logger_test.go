package server

import (
	"strings"
	"testing"
)

// TestValidReqID_lengthBoundaries pins the inclusive length window [1, 64]
// that validReqID accepts. A one-character id is the smallest valid id and a
// 64-character id is the largest; a 65-character id is just past the upper
// bound. The logger echoes a valid inbound id verbatim, so either boundary
// shifting inward would reject an id it must preserve, and shifting the upper
// bound outward would echo an over-length id.
func TestValidReqID_lengthBoundaries(t *testing.T) {
	if !validReqID("a") {
		t.Errorf("validReqID(%q) = false, want true (len==1 lower boundary)", "a")
	}
	if !validReqID(strings.Repeat("a", 64)) {
		t.Errorf("validReqID(64 chars) = false, want true (len==64 upper boundary)")
	}
	if validReqID(strings.Repeat("a", 65)) {
		t.Errorf("validReqID(65 chars) = true, want false (just past upper boundary)")
	}
}
