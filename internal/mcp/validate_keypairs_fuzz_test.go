package mcp

import (
	"testing"
)

func FuzzValidateKeyPairs(f *testing.F) {
	f.Add("env", "FOO", "bar", "BAZ", "qux", false)
	f.Add("headers", "Content-Type", "application/json", "content-type", "text/plain", true)
	f.Add("env", "A\x00B", "val", "C", "v\x01l", false)
	f.Add("env", "", "", "OK", "x", false)

	f.Fuzz(func(t *testing.T, kind, name1, val1, name2, val2 string, caseInsensitive bool) {
		pairs := []KeyPair{
			{Name: name1, Value: val1},
			{Name: name2, Value: val2},
		}
		// Must not panic.
		validateKeyPairs(kind, pairs, 10, 1024, caseInsensitive)
	})
}
