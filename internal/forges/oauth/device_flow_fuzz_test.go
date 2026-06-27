package oauth

import "testing"

// FuzzInterpretPollResponse feeds arbitrary bytes, since a GitHub token-poll
// response body is external, attacker-influenced input. Security invariants:
// never panics; on success Status is one of the known values; a "complete"
// result ALWAYS carries a non-empty token; and any non-complete result
// carries none. The last two together pin the rule that an empty
// access_token must never be reported as a completed login.
func FuzzInterpretPollResponse(f *testing.F) {
	f.Add([]byte(`{"error":"authorization_pending"}`))
	f.Add([]byte(`{"error":"slow_down"}`))
	f.Add([]byte(`{"error":"expired_token"}`))
	f.Add([]byte(`{"error":"access_denied"}`))
	f.Add([]byte(`{"error":"weird","error_description":"d"}`))
	f.Add([]byte(`{"access_token":"gho_x"}`))
	f.Add([]byte(`{"access_token":""}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, body []byte) {
		res, err := interpretPollResponse(body)
		if err != nil {
			return
		}
		switch res.Status {
		case "pending", "expired", "error", "complete":
		default:
			t.Fatalf("unexpected status %q for body %q", res.Status, body)
		}
		if res.Status == "complete" {
			if res.Token == "" {
				t.Fatalf("complete result with empty token for body %q", body)
			}
		} else if res.Token != "" {
			t.Fatalf("non-complete result %q carried a token for body %q", res.Status, body)
		}
	})
}
