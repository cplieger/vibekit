package types

import "testing"

func TestParseTag(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantTag Tag
	}{
		{"single digit", "1", true, "1"},
		{"multi digit", "10", true, "10"},
		{"with tool index", "1.0", true, "1.0"},
		{"large numbers", "10.5", true, "10.5"},
		{"empty", "", false, ""},
		{"letters", "abc", false, ""},
		{"triple dot", "1.2.3", false, ""},
		{"negative", "-1", false, ""},
		{"trailing dot", "1.", false, ""},
		{"leading dot", ".1", false, ""},
		{"spaces", " 1", false, ""},
		{"zero", "0", true, "0"},
		{"zero.zero", "0.0", true, "0.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tag, err := ParseTag(tc.input)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("ParseTag(%q) unexpected error: %v", tc.input, err)
				}
				if tag != tc.wantTag {
					t.Errorf("ParseTag(%q) = %q, want %q", tc.input, tag, tc.wantTag)
				}
			} else {
				if err == nil {
					t.Errorf("ParseTag(%q) = %q, want error", tc.input, tag)
				}
			}
		})
	}
}

func FuzzParseTag(f *testing.F) {
	f.Add("1")
	f.Add("1.0")
	f.Add("10.5")
	f.Add("")
	f.Add("abc")
	f.Add("1.2.3")
	f.Add("-1")

	f.Fuzz(func(t *testing.T, s string) {
		tag, err := ParseTag(s)
		if err == nil {
			// Round-trip: valid tag's String() must equal input.
			if tag.String() != s {
				t.Errorf("round-trip failed: ParseTag(%q).String() = %q", s, tag.String())
			}
		}
	})
}

func TestFileStatus_Valid(t *testing.T) {
	tests := []struct {
		status FileStatus
		want   bool
	}{
		{FileAdded, true},
		{FileModified, true},
		{FileDeleted, true},
		{"X", false},
		{"", false},
		{"AM", false},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			if got := tc.status.Valid(); got != tc.want {
				t.Errorf("FileStatus(%q).Valid() = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}
