package kiroauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/slogx/capture"
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
	// The reply carries NO region, and that is the contract rather than an
	// omission: KAS derives its region from segment 4 of the profileArn, matching
	// kiro-cli's own resolution in endpoints.rs, so a region sent here would be a
	// second answer to a question the ARN already settles — and the two can
	// disagree. Pinned HERE rather than at the responder because this function
	// decides the key set: respondKiroAccessToken forwards the map verbatim with
	// nothing added, so a responder-level assertion would need kiroToken made an
	// interface in production to reach a function that cannot add a key.
	if len(res2) != 5 {
		t.Errorf("result keys = %v, want exactly the five vend keys and no more", res2)
	}
	for _, d := range []*tokenData{d, d2} {
		if _, ok := d.result()["region"]; ok {
			t.Errorf("result = %v, must not carry a region: KAS derives it from the profileArn", d.result())
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

// A caller that was already waiting when the current token arrived adopts it
// instead of spending a second invocation on the same answer.
//
// The sibling above pins the SEQUENTIAL near-expiry case, which must still
// re-ask; this pins the concurrent one, which must not. Without it the mutex only
// serializes: each caller in turn finds the cache inside the leeway window and
// spends its own 30s-bounded subprocess on an answer the caller ahead of it
// already has, with the lock held throughout, so every chat's auth callback
// queues behind one question. N bridges spawning at once is the ordinary case
// this package's doc comment describes.
//
// Driven by setting `fetched` rather than by racing goroutines, deliberately.
// `arrived` is read as Token's first statement, so a test cannot observe or order
// it from outside: a goroutine slow to start records an `arrived` AFTER the fetch
// it was supposed to be waiting for, re-invokes correctly, and fails the
// assertion for a reason that is not the code's. A first attempt at this test
// raced exactly that way. What `fetched` in the future MEANS is "a caller that
// arrived before the current token landed", which is the branch's whole input.
func TestToken_ACallerAlreadyWaitingAdoptsTheFetchedToken(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	// One minute of life: inside the 5-minute leeway, so the ordinary cache check
	// cannot serve it and only the coalescing branch can.
	src.runCommand = fakeRun(&calls, envelope(time.Now().Add(time.Minute).Format(time.RFC3339Nano)), nil)
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("seeding Token: %v", err)
	}
	if calls != 1 {
		t.Fatalf("seed invoked the CLI %d times, want 1", calls)
	}

	src.fetched = time.Now().Add(time.Minute)

	res, err := src.Token(t.Context())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if calls != 1 {
		t.Errorf("CLI invoked %d times, want 1: a caller that arrived before the fetch must adopt it", calls)
	}
	if res["accessToken"] != "tok-abc" {
		t.Errorf("accessToken = %v, want the shared tok-abc", res["accessToken"])
	}
	if res["profileArn"] == nil {
		t.Errorf("result = %v, want the profileArn carried to the adopting caller too", res)
	}
}

// A HARD-EXPIRED token is re-asked however recently it arrived, so the
// coalescing branch cannot hand a waiter something KAS will reject.
func TestToken_AWaitingCallerNeverAdoptsAnExpiredToken(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	src.runCommand = fakeRun(&calls, envelope(time.Now().Add(-time.Minute).Format(time.RFC3339Nano)), nil)
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("seeding Token: %v", err)
	}

	src.fetched = time.Now().Add(time.Minute)

	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if calls != 2 {
		t.Errorf("CLI invoked %d times, want 2 (an expired token is re-asked however fresh its arrival)", calls)
	}
}

// A token whose expiry cannot be judged is never handed to a waiting caller
// either, so adding the coalescing branch widened nothing.
//
// What ENFORCES that is `s.cached = nil` on the unparseable-expiry path, which
// fails the branch's first condition — not the `fetched` reset beside it, which is
// redundant while that holds (a mutation removing it survives, checked). The reset
// stays because the three fields are one state, "no usable cache", and a later
// relaxation of the nil check would otherwise inherit a stamp saying the absent
// token was fresh. This test pins the PROPERTY; the comment names the guard so
// nobody reads the assertion as covering the reset.
func TestToken_UnjudgeableExpiryIsNotSharedWithWaiters(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	src.runCommand = fakeRun(&calls, envelope("not-a-timestamp"), nil)

	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	// Simulate a caller that arrived before that fetch landed. With the cache
	// cleared there is nothing for it to adopt, so it must re-ask.
	src.fetched = time.Now().Add(time.Hour)
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if calls != 2 {
		t.Errorf("CLI invoked %d times, want 2 (an unjudgeable token is shared with nobody)", calls)
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

// A failure inside the near-expiry window vends the cached token rather than
// reporting an auth failure, because that token is still valid.
//
// The window is not narrow in practice: the cache is read in exactly ONE place,
// gated on more than reuseLeeway remaining, so for the last five minutes of every
// token's life each callback re-invokes the CLI. Any blip there — a hiccup during
// the CLI's own refresh, or cliTimeout firing — used to become a hard auth
// failure: the sign-in banner, a failed turn on a session that was working, and
// readiness flipping unready, while a token good for minutes sat in the struct.
// It mirrors the reference host, which swallows a background refresh failure for
// exactly this reason.
func TestToken_TransientFailureVendsTheStillValidCache(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	// One minute of life: inside the 5-minute leeway, so every call re-invokes.
	src.runCommand = fakeRun(&calls, envelope(time.Now().Add(time.Minute).Format(time.RFC3339Nano)), nil)
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("seeding Token: %v", err)
	}

	for name, run := range map[string]func(context.Context, string, []string) ([]byte, error){
		"the CLI failed":       fakeRun(&calls, "", errors.New("exit status 1")),
		"its output was junk":  fakeRun(&calls, "kiro-cli: some human text", nil),
		"it reported no login": fakeRun(&calls, `{"kind":"error","data":{"reason":"not logged in"}}`, nil),
	} {
		t.Run(name, func(t *testing.T) {
			src.runCommand = run
			res, err := src.Token(t.Context())
			if err != nil {
				t.Fatalf("Token returned %v, want the cached token vended instead", err)
			}
			if res["accessToken"] != "tok-abc" {
				t.Errorf("accessToken = %v, want the cached tok-abc", res["accessToken"])
			}
			if res["profileArn"] == nil {
				t.Errorf("result = %v, want the cached profileArn carried too: a token vended without it fails the first turn", res)
			}
		})
	}
}

// A HARD-expired cache is not vended: there is nothing valid to fall back to, so
// the failure is the honest answer. This is the other side of the guard, and
// without it the fallback would hand KAS a token it has already rejected.
func TestToken_TransientFailureWithAnExpiredCacheStillFails(t *testing.T) {
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	src.runCommand = fakeRun(&calls, envelope(time.Now().Add(-time.Minute).Format(time.RFC3339Nano)), nil)
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("seeding Token: %v", err)
	}

	src.runCommand = fakeRun(&calls, "", errors.New("exit status 1"))
	if _, err := src.Token(t.Context()); err == nil {
		t.Fatal("Token returned nil error on a failure with an expired cache, want the failure reported")
	}
}

// A token with no CodeWhisperer profile ARN is vended AND logged.
//
// Without the log this is a silent hard turn failure that reads as a service
// outage: KAS derives its region from segment 4 of that ARN, so initialize and
// session/new both succeed and the FIRST prompt dies with -32000
// ModelRegistryUnavailableError, user-facing text "Kiro could not load the
// available models. Please try again." — which invites retrying forever with
// nothing in the logs pointing at the profile.
func TestToken_WarnsWhenTheProfileArnIsMissing(t *testing.T) {
	c := capture.Default(t)
	calls := 0
	src := NewCLISource(func() string { return "/fake/kiro-cli" }, nil)
	src.runCommand = fakeRun(&calls, fmt.Sprintf(
		`{"kind":"getKasToken","data":{"accessToken":"tok-abc","expiresAt":%q}}`,
		time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	), nil)

	res, err := src.Token(t.Context())
	if err != nil {
		t.Fatalf("Token returned %v, want the token vended anyway", err)
	}
	if res["accessToken"] != "tok-abc" {
		t.Errorf("accessToken = %v, want it vended: the token is valid and refusing it would be worse", res["accessToken"])
	}
	if _, ok := res["profileArn"]; ok {
		t.Errorf("result = %v, want no profileArn key at all rather than an empty one", res)
	}
	if c.CountExact("kiro-cli vended a token with no CodeWhisperer profile ARN; "+
		"the model registry and account usage will fail for this session") == 0 {
		t.Error("no warn logged for a token with no profile ARN; want the failure named rather than inferred")
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
