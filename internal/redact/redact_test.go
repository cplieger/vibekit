package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// assertRedaction checks that every value in gone was masked and every value in
// kept survived.
func assertRedaction(t *testing.T, out string, gone, kept []string) {
	t.Helper()
	for _, g := range gone {
		if strings.Contains(out, g) {
			t.Errorf("secret %q survived redaction: %q", g, out)
		}
	}
	for _, k := range kept {
		if !strings.Contains(out, k) {
			t.Errorf("expected %q to survive, got %q", k, out)
		}
	}
}

// tokenCases hold for BOTH Output and Report: an issuer-prefixed token dies
// under either, and a benign value survives both (the over-redaction guard).
var tokenCases = []struct {
	name string
	in   string
	gone []string
	kept []string
}{
	{
		name: "aws access key id",
		in:   "id is AKIAIOSFODNN7EXAMPLE here",
		gone: []string{"AKIAIOSFODNN7EXAMPLE"},
		kept: []string{Placeholder},
	},
	{
		name: "github pat",
		in:   "token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345 ok",
		gone: []string{"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"},
	},
	{
		name: "gitlab pat",
		in:   "GITLAB_TOKEN=glpat-ABCDEFGHIJKLMNOP1234",
		gone: []string{"glpat-ABCDEFGHIJKLMNOP1234"},
	},
	{
		name: "slack token",
		in:   "xoxb-1234567890-abcdefghij is the hook",
		gone: []string{"xoxb-1234567890-abcdefghij"},
	},
	{
		name: "bearer header",
		in:   "Authorization: Bearer abcDEF012345_and-more.tokenvalue",
		gone: []string{"abcDEF012345_and-more.tokenvalue"},
	},
	{
		name: "benign build hash not redacted",
		in:   `"commit": "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"`,
		kept: []string{"1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"},
	},
	{
		name: "prose left alone",
		in:   "the deploy finished in 12s with 3 warnings",
		kept: []string{"the deploy finished in 12s with 3 warnings"},
	},
}

func TestOutput_MasksTokenShapes(t *testing.T) {
	for _, tt := range tokenCases {
		t.Run(tt.name, func(t *testing.T) {
			assertRedaction(t, Output(tt.in), tt.gone, tt.kept)
		})
	}
}

func TestReport_MasksTokenShapes(t *testing.T) {
	for _, tt := range tokenCases {
		t.Run(tt.name, func(t *testing.T) {
			assertRedaction(t, Report(tt.in), tt.gone, tt.kept)
		})
	}
}

// TestReport_MasksSecretNamedFields covers the key-driven rule, which is
// Report's alone.
func TestReport_MasksSecretNamedFields(t *testing.T) {
	in := `"client_secret": "s3cr3tVALUE123456"`
	assertRedaction(t, Report(in),
		[]string{"s3cr3tVALUE123456"},
		[]string{`"client_secret"`, Placeholder})
}

// TestOutput_KeepsSecretNamedFieldValues pins the deliberate difference from
// Report and is the reason this package exposes two functions. Agent output
// legitimately contains JSON whose values the operator needs to read, so a
// value is masked here only when it LOOKS like a credential, never merely
// because of the key it sits under.
func TestOutput_KeepsSecretNamedFieldValues(t *testing.T) {
	in := `"api_key_name": "production-readonly"`
	if got := Output(in); got != in {
		t.Errorf("Output must not blank a value by key name:\n got %q\nwant %q", got, in)
	}
}

// TestOutput_KeepsJSONValid backs the doc comment's claim that a replacement
// cannot unbalance a structured document: every pattern matches a run of
// characters that cannot include a quote.
func TestOutput_KeepsJSONValid(t *testing.T) {
	in := `{"env":{"AWS_ACCESS_KEY_ID":"AKIAIOSFODNN7EXAMPLE"},"ok":true}`
	out := Output(in)
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("secret survived: %q", out)
	}
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Errorf("redaction broke the JSON document: %v\n%s", err, out)
	}
}

// FuzzOutput_DelimitedSecretNeverSurvives asserts the security invariant that
// matters: wherever a delimited credential lands in arbitrary tool output, it
// does not come back out. Also checks idempotence, since the projection path
// may redact text that was already redacted.
func FuzzOutput_DelimitedSecretNeverSurvives(f *testing.F) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	f.Add("prefix", "suffix")
	f.Add("", "")
	f.Add("{\"json\":\"", "\"}")
	f.Add("\x1b[31m", "\x00\ufeff")
	f.Add(strings.Repeat("a", 4096), "\n\n")
	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		// A copy of the secret in the fuzz-supplied text may legitimately be
		// undelimited and so survive; that is not what this asserts.
		if strings.Contains(prefix, secret) || strings.Contains(suffix, secret) {
			t.Skip("fuzz input carries its own copy of the secret")
		}
		out := Output(prefix + " " + secret + " " + suffix)
		if strings.Contains(out, secret) {
			t.Errorf("delimited secret survived: %q", out)
		}
		if again := Output(out); again != out {
			t.Errorf("Output is not idempotent:\n first %q\nsecond %q", out, again)
		}
	})
}

// TestRawJSON_MasksAndStaysParseable is the pair of guarantees a tool call's
// input needs: the credential is gone, and the document the UI decodes is still
// valid JSON.
func TestRawJSON_MasksAndStaysParseable(t *testing.T) {
	in := json.RawMessage(`{"command":"curl -H \"Authorization: Bearer abcDEF012345_tokenvalue\" https://x"}`)
	out := RawJSON(in)
	if strings.Contains(string(out), "abcDEF012345_tokenvalue") {
		t.Errorf("secret survived in tool input: %s", out)
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Errorf("redaction broke the input document: %v\n%s", err, out)
	}
}

// TestRawJSON_PassesThroughUnchanged covers the common case: most tool inputs
// carry no credential, and the original slice is handed back rather than copied.
func TestRawJSON_PassesThroughUnchanged(t *testing.T) {
	in := json.RawMessage(`{"path":"main.go","limit":100}`)
	if got := RawJSON(in); string(got) != string(in) {
		t.Errorf("clean input was altered:\n got %s\nwant %s", got, in)
	}
	if got := RawJSON(nil); got != nil {
		t.Errorf("nil input must pass through, got %v", got)
	}
	if got := RawJSON(json.RawMessage{}); len(got) != 0 {
		t.Errorf("empty input must pass through, got %v", got)
	}
}

// TestRawJSON_UsesTheSameVocabularyAsOutput pins the "same code" contract: input
// and output must never drift apart in what they mask, so a value redacted in one
// is redacted in the other.
func TestRawJSON_UsesTheSameVocabularyAsOutput(t *testing.T) {
	for _, tt := range tokenCases {
		t.Run(tt.name, func(t *testing.T) {
			// Wrapped as a JSON string value, the way a real tool input carries it.
			enc, err := json.Marshal(map[string]string{"arg": tt.in})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			assertRedaction(t, string(RawJSON(enc)), tt.gone, nil)
		})
	}
}
