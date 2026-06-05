package types

import "testing"

// FuzzParseTagIdempotent verifies that a successfully parsed tag's
// String() representation re-parses to the same value.
func FuzzParseTagIdempotent(f *testing.F) {
	f.Add("1")
	f.Add("1.0")
	f.Add("42.7")
	f.Add("0")
	f.Add("999.999")
	f.Add("")
	f.Add("abc")
	f.Add("1.2.3")

	f.Fuzz(func(t *testing.T, s string) {
		tag, err := ParseTag(s)
		if err != nil {
			return
		}
		// String() must equal original input.
		if tag.String() != s {
			t.Errorf("ParseTag(%q).String() = %q", s, tag.String())
		}
		// Re-parse must succeed.
		tag2, err2 := ParseTag(tag.String())
		if err2 != nil {
			t.Fatalf("re-parse of %q failed: %v", tag.String(), err2)
		}
		if tag2 != tag {
			t.Errorf("idempotency broken: %q != %q", tag2, tag)
		}
	})
}
