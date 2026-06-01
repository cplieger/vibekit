package types

import "testing"

func TestParseTag(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantTag Tag
		wantOK  bool
	}{
		{name: "single digit", input: "1", wantTag: "1", wantOK: true},
		{name: "multi digit", input: "10", wantTag: "10", wantOK: true},
		{name: "with tool index", input: "1.0", wantTag: "1.0", wantOK: true},
		{name: "large numbers", input: "10.5", wantTag: "10.5", wantOK: true},
		{name: "empty", input: "", wantTag: "", wantOK: false},
		{name: "letters", input: "abc", wantTag: "", wantOK: false},
		{name: "triple dot", input: "1.2.3", wantTag: "", wantOK: false},
		{name: "negative", input: "-1", wantTag: "", wantOK: false},
		{name: "trailing dot", input: "1.", wantTag: "", wantOK: false},
		{name: "leading dot", input: ".1", wantTag: "", wantOK: false},
		{name: "spaces", input: " 1", wantTag: "", wantOK: false},
		{name: "zero", input: "0", wantTag: "0", wantOK: true},
		{name: "zero.zero", input: "0.0", wantTag: "0.0", wantOK: true},
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
