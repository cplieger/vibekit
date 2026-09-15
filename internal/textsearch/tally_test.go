package textsearch

import (
	"encoding/json"
	"testing"
)

// TestTally_JSONSpelling pins the three wire keys every embedding reply
// inherits, and that none is omitted when zero: a client must be able to read
// scanned=0 and truncated=false as facts rather than as absent fields.
func TestTally_JSONSpelling(t *testing.T) {
	cases := []struct {
		name string
		in   Tally
		want string
	}{
		{"populated", Tally{Scanned: 173, Matched: 340, Truncated: true}, `{"scanned":173,"matched":340,"truncated":true}`},
		{"zero", Tally{}, `{"scanned":0,"matched":0,"truncated":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("Marshal(%+v): %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Errorf("Marshal(%+v) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}
