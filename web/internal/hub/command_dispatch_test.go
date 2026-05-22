package hub

import (
	"regexp"
	"testing"
	"unicode"
)

var safeRe = regexp.MustCompile(`^[A-Za-z0-9_.\-:]+$`)

func FuzzValidMessageID(f *testing.F) {
	f.Add("abc123")
	f.Add("msg_id:001")
	f.Add("")
	f.Add("a")
	f.Add(string(make([]byte, 129)))
	f.Add("has\nnewline")
	f.Add("has\x00null")

	f.Fuzz(func(t *testing.T, id string) {
		result := validMessageID(id)
		if result {
			// Invariant 1: accepted IDs have length in [1, 128].
			if len(id) == 0 || len(id) > 128 {
				t.Errorf("accepted id with len %d", len(id))
			}
			// Invariant 2: no control characters or newlines.
			for _, r := range id {
				if unicode.IsControl(r) {
					t.Errorf("accepted id with control char %U", r)
				}
			}
			// Invariant 3: matches the safe character set.
			if !safeRe.MatchString(id) {
				t.Errorf("accepted id not matching safe regex: %q", id)
			}
		}
		// Empty string always rejected.
		if id == "" && result {
			t.Error("empty string accepted")
		}
		// Idempotent.
		if validMessageID(id) != result {
			t.Error("non-idempotent result")
		}
	})
}

func FuzzValidRequestID(f *testing.F) {
	f.Add("")
	f.Add("req-001")
	f.Add("a:b.c_d-e")
	f.Add(string(make([]byte, 129)))
	f.Add("has\nnewline")

	f.Fuzz(func(t *testing.T, id string) {
		result := validRequestID(id)
		if result && id != "" {
			// Non-empty accepted IDs must be <= 128 and match safe regex.
			if len(id) > 128 {
				t.Errorf("accepted request id with len %d", len(id))
			}
			if !safeRe.MatchString(id) {
				t.Errorf("accepted request id not matching safe regex: %q", id)
			}
		}
		// Empty is always accepted for request IDs.
		if id == "" && !result {
			t.Error("empty string rejected for request id")
		}
		// Idempotent.
		if validRequestID(id) != result {
			t.Error("non-idempotent result")
		}
	})
}
