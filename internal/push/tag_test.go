package push

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/cplieger/webhttp/v3"
)

// TestTagOf_MatchesTheCrossLanguageGolden pins the derivation both halves compute:
// the same fixture is read by static-src/sse-tag.test.ts, so a drift on either
// side fails a test rather than silencing a device.
func TestTagOf_MatchesTheCrossLanguageGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/tag_golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var cases []struct {
		Endpoint string `json:"endpoint"`
		Tag      string `json:"tag"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("golden holds no case")
	}
	for _, tc := range cases {
		got := TagOf(tc.Endpoint)
		if got != tc.Tag {
			t.Errorf("TagOf(%q) = %q, want %q", tc.Endpoint, got, tc.Tag)
		}
		if len(got) != TagLen {
			t.Errorf("TagOf(%q) has length %d, want %d", tc.Endpoint, len(got), TagLen)
		}
		if !webhttp.ValidRequestID(got) {
			t.Errorf("TagOf(%q) = %q is outside the SSE-Client grammar", tc.Endpoint, got)
		}
	}
}

// TestTagOf_DistinctEndpointsDistinctTags pins that two endpoints differing in
// their token part fold to two tags: a truncation that collided them would let
// one device's presence silence another's pushes.
func TestTagOf_DistinctEndpointsDistinctTags(t *testing.T) {
	a := TagOf("https://fcm.googleapis.com/fcm/send/aaa")
	b := TagOf("https://fcm.googleapis.com/fcm/send/aab")
	if a == b {
		t.Errorf("TagOf collided two endpoints on %q", a)
	}
}
