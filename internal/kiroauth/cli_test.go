package kiroauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeRun returns a runCommand seam that counts invocations and returns
// the given output/error.
func fakeRun(calls *int, out string, err error) func(context.Context, string, []string) ([]byte, error) {
	return func(context.Context, string, []string) ([]byte, error) {
		*calls++
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	}
}

// envelope builds a getKasToken stdout envelope with the given expiry.
func envelope(expiresAt string) string {
	return fmt.Sprintf(`{"kind":"getKasToken","data":{"accessToken":"tok-abc","expiresAt":%q,"profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/X","provider":"Internal"}}`, expiresAt)
}

func TestParseTokenEnvelope(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr string // "" = success
		check   func(t *testing.T, d *tokenData)
	}{
		{
			name: "full success",
			in:   `{"kind":"getKasToken","data":{"accessToken":"a","expiresAt":"e","profileArn":"p","authMethod":"IdC","provider":"Internal"}}`,
			check: func(t *testing.T, d *tokenData) {
				if d.AccessToken != "a" || d.ExpiresAt != "e" || d.ProfileArn != "p" || d.AuthMethod != "IdC" || d.Provider != "Internal" {
					t.Errorf("fields = %+v", d)
				}
			},
		},
		{
			name: "minimal success",
			in:   `{"kind":"getKasToken","data":{"accessToken":"a","expiresAt":"e"}}`,
			check: func(t *testing.T, d *tokenData) {
				if d.ProfileArn != "" || d.AuthMethod != "" || d.Provider != "" {
					t.Errorf("optional fields not empty: %+v", d)
				}
			},
		},
		{
			name:    "error kind is the logged-out verdict",
			in:      `{"kind":"error","data":{"reason":"not logged in"}}`,
			wantErr: "log in again",
		},
		{
			name:    "unknown kind",
			in:      `{"kind":"somethingElse","data":{}}`,
			wantErr: "unexpected envelope kind",
		},
		{
			name:    "non-JSON output",
			in:      "kiro-cli: some human text",
			wantErr: "unparseable output",
		},
		{
			name:    "empty access token",
			in:      `{"kind":"getKasToken","data":{"accessToken":"","expiresAt":"e"}}`,
			wantErr: "empty access token",
		},
		{
			name:    "bad data payload",
			in:      `{"kind":"getKasToken","data":"not-an-object"}`,
			wantErr: "bad data payload",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := parseTokenEnvelope([]byte(tc.in))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, d)
		})
	}
}

// TestParseTokenEnvelope_ErrorTextIsSafeForALog covers the two properties of
// the quoted upstream text that the fuzz target cannot state: what replaces
// a masked credential, and that the text is safe in a log line at all. The
// error reaches slog (runtime's "v3 auth: token unavailable") and a JSON-RPC
// frame, so a raw newline in it forges a log record and a C0 introducer
// writes terminal escapes.
func TestParseTokenEnvelope_ErrorTextIsSafeForALog(t *testing.T) {
	const tok = "secret-tok-value"
	cases := []struct {
		name       string
		in         string
		wantSubstr string // must appear
		denySubstr string // must not appear ("" = no deny check)
	}{
		{
			name:       "error payload carrying token material is masked",
			in:         `{"kind":"error","data":{"reason":"refresh failed","accessToken":"` + tok + `"}}`,
			wantSubstr: "[redacted]",
			denySubstr: tok,
		},
		{
			name:       "error payload reason survives the mask",
			in:         `{"kind":"error","data":{"reason":"refresh failed","accessToken":"` + tok + `"}}`,
			wantSubstr: "refresh failed",
		},
		{
			name:       "unexpected kind equal to the token is masked",
			in:         `{"kind":"` + tok + `","data":{"accessToken":"` + tok + `"}}`,
			wantSubstr: "[redacted]",
			denySubstr: tok,
		},
		{
			name:       "an ordinary unexpected kind is still reported",
			in:         `{"kind":"somethingElse","data":{}}`,
			wantSubstr: "somethingElse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTokenEnvelope([]byte(tc.in))
			if err == nil {
				t.Fatalf("want an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("err = %q, want containing %q", err, tc.wantSubstr)
			}
			if tc.denySubstr != "" && strings.Contains(err.Error(), tc.denySubstr) {
				t.Errorf("err = %q, must not contain %q", err, tc.denySubstr)
			}
		})
	}

	t.Run("control characters cannot forge a log record", func(t *testing.T) {
		// A raw newline plus an ESC introducer inside the kind.
		_, err := parseTokenEnvelope([]byte(`{"kind":"a\nb\u001b[2Jc","data":{}}`))
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		// %q would escape a surviving control rune, so assert on the
		// unquoted verdict: runesafe replaced each one with a space.
		for _, bad := range []string{"\n", "\x1b"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("err = %q, still carries %q", err, bad)
			}
		}
		if !strings.Contains(err.Error(), "a b [2Jc") {
			t.Errorf("err = %q, want the sanitized kind", err)
		}
	})

	t.Run("an oversize kind is bounded", func(t *testing.T) {
		_, err := parseTokenEnvelope([]byte(`{"kind":"` + strings.Repeat("k", 4096) + `","data":{}}`))
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		// The whole message, not just the quoted part: this is what reaches
		// the log attribute. diagCap plus this package's own prefix, the two
		// quotes, and the elision marker outside the cap.
		if n := len(err.Error()); n > diagCap+64 {
			t.Errorf("error is %d bytes for a 4096-byte kind, want <= %d", n, diagCap+64)
		}
		if !strings.Contains(err.Error(), "...") {
			t.Errorf("err = %q, want the elision marker proving truncation", err)
		}
	})

	// The marker MEANS truncation, so text that fits must not carry one. A
	// diagnostic measuring exactly the cap fits: it is the largest text that
	// reaches the log whole. An elision marker on it would tell whoever is
	// reading that upstream said more than this, and there is nothing more.
	t.Run("a kind exactly at the cap keeps every byte and no marker", func(t *testing.T) {
		kind := strings.Repeat("k", diagCap)
		_, err := parseTokenEnvelope([]byte(`{"kind":"` + kind + `","data":{}}`))
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("err = %q, want the whole %d-byte kind", err, diagCap)
		}
		if strings.Contains(err.Error(), "...") {
			t.Errorf("err = %q, carries an elision marker for text that was not elided", err)
		}
	})

	// The ordering guard. Sanitizing must run BEFORE the mask, not after:
	// encoding/json reads an invalid UTF-8 byte as U+FFFD, and the sanitizer
	// normalizes the raw byte the same way, so a mask applied first finds
	// nothing and sanitizing then assembles the decoded token out of raw
	// bytes that never contained it. Field names are deliberately mis-cased
	// because encoding/json matches them case-insensitively, which is how
	// the fuzzer reached this shape. Collapsing diagnostic back onto
	// runesafe.SanitizeSingleLineBounded reopens it.
	t.Run("invalid UTF-8 cannot reconstruct the token past the mask", func(t *testing.T) {
		raw := []byte("{\"kind\":\"error\",\"dAtA\":{\"ACCessToken\":\"\xd2000000\"}}")
		var probe struct {
			Data struct {
				AccessToken string `json:"accessToken"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("fixture no longer decodes, so it pins nothing: %v", err)
		}
		tok := probe.Data.AccessToken
		if !strings.HasPrefix(tok, "\uFFFD") {
			t.Fatalf("fixture decoded to %q, want a U+FFFD prefix — the point of the case", tok)
		}
		_, err := parseTokenEnvelope(raw)
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		if strings.Contains(err.Error(), tok) {
			t.Errorf("err = %q reconstructs the decoded token %q", err, tok)
		}
	})
}

func TestTokenResult_OmitsEmptyOptionalFields(t *testing.T) {
	d := &tokenData{AccessToken: "a", ExpiresAt: "e"}
	res := d.result()
	if len(res) != 2 {
		t.Errorf("result keys = %v, want only accessToken+expiresAt", res)
	}
	d2 := &tokenData{AccessToken: "a", ExpiresAt: "e", ProfileArn: "p", AuthMethod: "IdC", Provider: "Internal"}
	res2 := d2.result()
	for _, k := range []string{"accessToken", "expiresAt", "profileArn", "authMethod", "provider"} {
		if _, ok := res2[k]; !ok {
			t.Errorf("missing key %s in %v", k, res2)
		}
	}
}

func TestToken_NoResolver(t *testing.T) {
	for name, src := range map[string]*CLISource{
		"nil resolver":   NewCLISource(nil, nil),
		"empty resolver": NewCLISource(func() string { return "" }, nil),
	} {
		if _, err := src.Token(t.Context()); !errors.Is(err, errCLIUnavailable) {
			t.Errorf("%s: err = %v, want errCLIUnavailable", name, err)
		}
	}
}

func TestToken_SuccessAndCache(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	src.runCommand = fakeRun(&calls, envelope(time.Now().Add(time.Hour).Format(time.RFC3339Nano)), nil)

	res, err := src.Token(t.Context())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if res["accessToken"] != "tok-abc" || res["provider"] != "Internal" {
		t.Errorf("result = %v", res)
	}
	if _, ok := res["authMethod"]; ok {
		t.Errorf("authMethod present despite empty: %v", res)
	}

	// Second call within the leeway window: served from cache.
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("cached Token: %v", err)
	}
	if calls != 1 {
		t.Errorf("CLI invoked %d times, want 1 (cache)", calls)
	}
}

func TestToken_NearExpiryReinvokes(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	// Expires in 1 minute — inside the 5-minute reuse leeway, so every
	// call must re-ask the CLI (which owns the refresh decision).
	src.runCommand = fakeRun(&calls, envelope(time.Now().Add(time.Minute).Format(time.RFC3339Nano)), nil)

	for range 2 {
		if _, err := src.Token(t.Context()); err != nil {
			t.Fatalf("Token: %v", err)
		}
	}
	if calls != 2 {
		t.Errorf("CLI invoked %d times, want 2 (near-expiry token must not be reused)", calls)
	}
}

func TestToken_UnparseableExpiryVendsButNeverCaches(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	src.runCommand = fakeRun(&calls, envelope("not-a-timestamp"), nil)

	res, err := src.Token(t.Context())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if res["expiresAt"] != "not-a-timestamp" {
		t.Errorf("expiresAt = %v, want verbatim pass-through", res["expiresAt"])
	}
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if calls != 2 {
		t.Errorf("CLI invoked %d times, want 2 (unjudgeable expiry must not cache)", calls)
	}
}

func TestToken_CLIFailureWrapped(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	src.runCommand = fakeRun(&calls, "", errors.New("exit status 1"))

	_, err := src.Token(t.Context())
	if err == nil || !strings.Contains(err.Error(), "get-kas-token") {
		t.Fatalf("err = %v, want wrapped get-kas-token failure", err)
	}
}

// FuzzParseTokenEnvelope pins two invariants over arbitrary CLI output:
// (1) success implies a non-empty access token; (2) an error NEVER echoes
// the access-token value — CLI output is the one place a credential
// transits this package, and error strings land in logs.
func FuzzParseTokenEnvelope(f *testing.F) {
	f.Add([]byte(`{"kind":"getKasToken","data":{"accessToken":"secret-tok","expiresAt":"e"}}`))
	f.Add([]byte(`{"kind":"error","data":{"reason":"not logged in"}}`))
	f.Add([]byte(`{"kind":"getKasToken","data":"x"}`))
	f.Add([]byte(`plain text`))
	f.Add([]byte(`{}`))
	// The two shapes that put a credential in the quoted text. Both were
	// live leaks: the error payload is echoed whole, and the kind
	// discriminator is echoed verbatim.
	f.Add([]byte(`{"kind":"error","data":{"reason":"nope","accessToken":"secret-tok-value"}}`))
	f.Add([]byte(`{"kind":"secret-tok-value","data":{"accessToken":"secret-tok-value"}}`))
	f.Fuzz(func(t *testing.T, out []byte) {
		d, err := parseTokenEnvelope(out)
		if err == nil {
			if d == nil || d.AccessToken == "" {
				t.Fatalf("nil error with empty token: %+v", d)
			}
			return
		}
		// Extract the accessToken value if the input carried one; the
		// error text must not contain it.
		var env struct {
			Data struct {
				AccessToken string `json:"accessToken"`
			} `json:"data"`
		}
		if json.Unmarshal(out, &env) == nil && len(env.Data.AccessToken) > 8 {
			if strings.Contains(err.Error(), env.Data.AccessToken) {
				t.Fatalf("error text echoes the access token")
			}
		}
	})
}
