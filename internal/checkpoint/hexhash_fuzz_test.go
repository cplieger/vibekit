package checkpoint

import (
	"encoding/hex"
	"testing"
)

// FuzzIsHexHash exercises the isHexHash validator with arbitrary strings
// and asserts agreement with the reference implementation (len==64 and
// valid hex). Never panics.
func FuzzIsHexHash(f *testing.F) {
	f.Add("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	f.Add("")
	f.Add("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	f.Add("abc")
	f.Add("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85") // 63 chars

	f.Fuzz(func(t *testing.T, s string) {
		got := isHexHash(s)

		// Reference: must be exactly 64 chars and valid hex.
		want := len(s) == 64 && isValidHex(s)

		if got != want {
			t.Errorf("isHexHash(%q) = %v, want %v", s, got, want)
		}
	})
}

func isValidHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}
