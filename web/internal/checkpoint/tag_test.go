package checkpoint

import "testing"

func TestParseTag(t *testing.T) {
	cases := []struct {
		tag      string
		wantTurn int
		wantTool int
	}{
		{"0", 0, 0},
		{"1", 1, 0},
		{"10", 10, 0},
		{"1.0", 1, 0},
		{"1.2", 1, 2},
		{"1.10", 1, 10},
		{"10.5", 10, 5},
		{"0.0", 0, 0},
		{"99.99", 99, 99},
		{"", 0, 0},      // empty string: atoiSafe returns 0
		{"abc", 0, 0},   // non-numeric: atoiSafe returns 0
		{"1.abc", 1, 0}, // valid turn, non-numeric tool
		{"abc.1", 0, 1}, // non-numeric turn, valid tool
		{"1.2.3", 1, 0}, // extra dot: Cut gives toolStr="2.3", atoiSafe rejects '.'
	}
	for _, tc := range cases {
		turn, tool := parseTag(tc.tag)
		if turn != tc.wantTurn || tool != tc.wantTool {
			t.Errorf("parseTag(%q) = (%d, %d), want (%d, %d)",
				tc.tag, turn, tool, tc.wantTurn, tc.wantTool)
		}
	}
}

func TestFindSorted(t *testing.T) {
	tags := []string{"0", "1", "1.2", "2", "3", "10", "10.5"}

	cases := []struct {
		tag       string
		wantIdx   int
		wantFound bool
	}{
		{"0", 0, true},
		{"1", 1, true},
		{"1.2", 2, true},
		{"2", 3, true},
		{"3", 4, true},
		{"10", 5, true},
		{"10.5", 6, true},
		// Not present — insertion points.
		{"0.5", 1, false},  // between "0" and "1"
		{"1.1", 2, false},  // between "1" and "1.2"
		{"1.5", 3, false},  // between "1.2" and "2"
		{"5", 5, false},    // between "3" and "10"
		{"11", 7, false},   // past end
		{"10.3", 6, false}, // between "10" and "10.5"
	}
	for _, tc := range cases {
		idx, found := findSorted(tags, tc.tag)
		if idx != tc.wantIdx || found != tc.wantFound {
			t.Errorf("findSorted(tags, %q) = (%d, %v), want (%d, %v)",
				tc.tag, idx, found, tc.wantIdx, tc.wantFound)
		}
	}
}

func TestFindSortedEmpty(t *testing.T) {
	idx, found := findSorted(nil, "1")
	if idx != 0 || found != false {
		t.Errorf("findSorted(nil, %q) = (%d, %v), want (0, false)", "1", idx, found)
	}
}
