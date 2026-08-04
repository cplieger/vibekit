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
func fakeRun(calls *int, out string, err error) func(context.Context, string) ([]byte, error) {
	return func(context.Context, string) ([]byte, error) {
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
		"nil resolver":   NewCLISource(nil),
		"empty resolver": NewCLISource(func() string { return "" }),
	} {
		if _, err := src.Token(context.Background()); !errors.Is(err, errCLIUnavailable) {
			t.Errorf("%s: err = %v, want errCLIUnavailable", name, err)
		}
	}
}

func TestToken_SuccessAndCache(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" })
	src.runCommand = fakeRun(&calls, envelope(time.Now().Add(time.Hour).Format(time.RFC3339Nano)), nil)

	res, err := src.Token(context.Background())
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
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("cached Token: %v", err)
	}
	if calls != 1 {
		t.Errorf("CLI invoked %d times, want 1 (cache)", calls)
	}
}

func TestToken_NearExpiryReinvokes(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" })
	// Expires in 1 minute — inside the 5-minute reuse leeway, so every
	// call must re-ask the CLI (which owns the refresh decision).
	src.runCommand = fakeRun(&calls, envelope(time.Now().Add(time.Minute).Format(time.RFC3339Nano)), nil)

	for range 2 {
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatalf("Token: %v", err)
		}
	}
	if calls != 2 {
		t.Errorf("CLI invoked %d times, want 2 (near-expiry token must not be reused)", calls)
	}
}

func TestToken_UnparseableExpiryVendsButNeverCaches(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" })
	src.runCommand = fakeRun(&calls, envelope("not-a-timestamp"), nil)

	res, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if res["expiresAt"] != "not-a-timestamp" {
		t.Errorf("expiresAt = %v, want verbatim pass-through", res["expiresAt"])
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if calls != 2 {
		t.Errorf("CLI invoked %d times, want 2 (unjudgeable expiry must not cache)", calls)
	}
}

func TestToken_CLIFailureWrapped(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" })
	src.runCommand = fakeRun(&calls, "", errors.New("exit status 1"))

	_, err := src.Token(context.Background())
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
