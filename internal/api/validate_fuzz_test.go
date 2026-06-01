package api

import "testing"

func FuzzValidMessageID(f *testing.F) {
	f.Add("")
	f.Add("msg-123")
	f.Add("a.b-c:d_e")
	f.Add("msg/bad")
	f.Add("\x00null")

	f.Fuzz(func(t *testing.T, id string) {
		// Must not panic.
		_ = ValidMessageID(id)
	})
}

func FuzzValidRequestID(f *testing.F) {
	f.Add("")
	f.Add("req-abc.123")
	f.Add("req/bad")
	f.Add("\x00null")

	f.Fuzz(func(t *testing.T, id string) {
		// Must not panic.
		_ = ValidRequestID(id)
	})
}
