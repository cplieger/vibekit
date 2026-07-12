package kiroauth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCache seeds a token file + a registration file in dir and returns
// the token path.
func writeCache(t *testing.T, dir, accessToken, refreshToken string, expiresAt time.Time) string {
	t.Helper()
	tokPath := filepath.Join(dir, "kiro-auth-token.json")
	tf := tokenFile{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt.UTC().Format(time.RFC3339Nano),
		ClientIDHash: "reg",
		AuthMethod:   authMethodIDC,
		Region:       "us-east-1",
		Provider:     "iam-identity-center",
	}
	data, _ := json.MarshalIndent(tf, "", "  ")
	if err := os.WriteFile(tokPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reg, _ := json.Marshal(registration{ClientID: "cid", ClientSecret: "csecret"})
	if err := os.WriteFile(filepath.Join(dir, "reg.json"), reg, 0o600); err != nil {
		t.Fatal(err)
	}
	return tokPath
}

func TestNearExpiry(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	soon := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano)
	if NearExpiry(future, RefreshLeeway) {
		t.Error("token an hour out should not be near expiry")
	}
	if !NearExpiry(soon, RefreshLeeway) {
		t.Error("token 30s out should be near expiry")
	}
	if !NearExpiry("not-a-timestamp", RefreshLeeway) {
		t.Error("unparseable expiry should be treated as near-expiry")
	}
}

func TestToken_ValidTokenNoRefresh(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeCache(t, dir, "valid-access", "rt", time.Now().Add(time.Hour))
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()
	r := NewReader(tokPath)
	r.tokenURL = func(string) string { return srv.URL }
	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "valid-access" {
		t.Errorf("token = %q, want valid-access", tok)
	}
	if called {
		t.Error("refresh endpoint should not be hit for a valid token")
	}
}

func TestToken_RefreshesAndWritesBack(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeCache(t, dir, "stale-access", "old-rt", time.Now().Add(30*time.Second))
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewDecoder(req.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accessToken": "fresh-access", "refreshToken": "new-rt",
			"tokenType": "Bearer", "expiresIn": 3600,
		})
	}))
	defer srv.Close()
	r := NewReader(tokPath)
	r.tokenURL = func(string) string { return srv.URL }

	tok, exp, err := r.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "fresh-access" {
		t.Errorf("token = %q, want fresh-access", tok)
	}
	if NearExpiry(exp, RefreshLeeway) {
		t.Errorf("refreshed token should be far from expiry, got %q", exp)
	}
	if gotBody["grantType"] != "refresh_token" || gotBody["refreshToken"] != "old-rt" ||
		gotBody["clientId"] != "cid" || gotBody["clientSecret"] != "csecret" {
		t.Errorf("unexpected refresh request body: %v", gotBody)
	}
	// Write-back: file now carries the rotated token.
	data, _ := os.ReadFile(tokPath)
	var onDisk tokenFile
	_ = json.Unmarshal(data, &onDisk)
	if onDisk.AccessToken != "fresh-access" || onDisk.RefreshToken != "new-rt" {
		t.Errorf("write-back missing rotation: %+v", onDisk)
	}
	if onDisk.Region != "us-east-1" || onDisk.Provider != "iam-identity-center" {
		t.Errorf("write-back dropped sibling fields: %+v", onDisk)
	}
	// Mode preserved at 0600.
	fi, _ := os.Stat(tokPath)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
	}
}

// writeRawCache seeds a token file from an arbitrary key map (so a test can
// carry keys the tokenFile struct doesn't model) plus a matching
// registration file. Returns the token path.
func writeRawCache(t *testing.T, dir string, seed map[string]any) string {
	t.Helper()
	tokPath := filepath.Join(dir, "kiro-auth-token.json")
	data, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reg, _ := json.Marshal(registration{ClientID: "cid", ClientSecret: "csecret"})
	if err := os.WriteFile(filepath.Join(dir, "reg.json"), reg, 0o600); err != nil {
		t.Fatal(err)
	}
	return tokPath
}

// TestToken_PreservesUnknownFieldsOnWriteBack proves the rotated write-back
// keeps fields the tokenFile struct does not model. A struct-narrowing
// write-back would drop them and could corrupt the user's real kiro-cli
// login; this test fails on that regression.
func TestToken_PreservesUnknownFieldsOnWriteBack(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeRawCache(t, dir, map[string]any{
		"accessToken":  "stale-access",
		"refreshToken": "old-rt",
		"expiresAt":    time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano),
		"clientIdHash": "reg",
		"authMethod":   authMethodIDC,
		"region":       "us-east-1",
		"provider":     "Internal",
		// Fields NOT in tokenFile — must survive the rotation verbatim.
		"ssoStartUrl":       "https://example.awsapps.com/start",
		"registrationExtra": map[string]any{"nested": true, "count": 42},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accessToken": "fresh-access", "refreshToken": "new-rt",
			"tokenType": "Bearer", "expiresIn": 3600,
		})
	}))
	defer srv.Close()
	r := NewReader(tokPath)
	r.tokenURL = func(string) string { return srv.URL }

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(tokPath)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	// Rotated fields updated.
	if onDisk["accessToken"] != "fresh-access" || onDisk["refreshToken"] != "new-rt" {
		t.Errorf("rotated fields not written: access=%v refresh=%v", onDisk["accessToken"], onDisk["refreshToken"])
	}
	// Unknown scalar preserved verbatim.
	if onDisk["ssoStartUrl"] != "https://example.awsapps.com/start" {
		t.Errorf("unknown scalar field dropped on write-back: %v", onDisk["ssoStartUrl"])
	}
	// Unknown nested object preserved verbatim.
	extra, ok := onDisk["registrationExtra"].(map[string]any)
	if !ok || extra["nested"] != true || extra["count"] != float64(42) {
		t.Errorf("unknown nested field dropped/mangled on write-back: %v", onDisk["registrationExtra"])
	}
	// Known siblings preserved too.
	if onDisk["provider"] != "Internal" || onDisk["region"] != "us-east-1" || onDisk["authMethod"] != authMethodIDC {
		t.Errorf("sibling fields dropped on write-back: %v", onDisk)
	}
}

// TestToken_NonIdCAuthMethodVendsStale proves a near-expiry token whose
// authMethod is anything other than "IdC" is NOT sent through the SSO-OIDC
// (IdC-only) refresh — it is vended stale best-effort and the file is left
// untouched. Posting the IdC refresh body for a social/external token would
// be a wrong refresh that could invalidate the user's login.
func TestToken_NonIdCAuthMethodVendsStale(t *testing.T) {
	for _, method := range []string{"social", "external_idp", "builder-id", ""} {
		name := method
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			tokPath := writeRawCache(t, dir, map[string]any{
				"accessToken":  "stale-access",
				"refreshToken": "old-rt",
				"expiresAt":    time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano),
				"clientIdHash": "reg",
				"authMethod":   method,
				"region":       "us-east-1",
			})
			before, err := os.ReadFile(tokPath)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
			}))
			defer srv.Close()
			r := NewReader(tokPath)
			r.tokenURL = func(string) string { return srv.URL }

			tok, _, err := r.Token(context.Background())
			if err != nil {
				t.Fatalf("non-IdC near-expiry should vend stale best-effort, got err: %v", err)
			}
			if tok != "stale-access" {
				t.Errorf("token = %q, want stale-access (non-IdC vends stale)", tok)
			}
			if called {
				t.Error("SSO-OIDC refresh endpoint must NOT be hit for a non-IdC auth method")
			}
			after, err := os.ReadFile(tokPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("token file was modified for a non-IdC token; must be left untouched")
			}
		})
	}
}

func TestToken_RefreshFailureVendsStale(t *testing.T) {
	dir := t.TempDir()
	tokPath := writeCache(t, dir, "stale-access", "old-rt", time.Now().Add(30*time.Second))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	r := NewReader(tokPath)
	r.tokenURL = func(string) string { return srv.URL }
	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("refresh failure should be best-effort, got err: %v", err)
	}
	if tok != "stale-access" {
		t.Errorf("token = %q, want stale-access (best-effort vend)", tok)
	}
}
