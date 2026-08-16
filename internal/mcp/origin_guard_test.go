package mcp

// A PUT may change `url` while masked secret rows survive. mergeSecrets keys its
// index on the header NAME alone, so the bearer issued for the old origin used
// to be re-attached, persisted, and rendered into KAS's config file — whose
// watcher hands it to the new origin. These tests pin the refusal, and pin that
// an edit which does NOT change the origin still round-trips.

import (
	"net/http"
	"strings"
	"testing"
)

// newRemoteServer stores one remote server carrying a bearer and an OAuth
// client secret, and returns its stored (masked) record.
func newRemoteServer(t *testing.T, s *Store, transport Transport, rawURL string) *Server {
	t.Helper()
	got, err := s.Create(t.Context(), &Server{
		Transport:         transport,
		Name:              "hosted",
		URL:               rawURL,
		Headers:           []KeyPair{{Name: "Authorization", Value: "Bearer old-origin-token"}},
		OAuthClientSecret: "old-origin-secret",
		Enabled:           true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return got
}

func TestUpdate_URLOriginChangeRefusesAPreservedHeader(t *testing.T) {
	for _, transport := range []Transport{TransportHTTP, TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			s := newTestStore(t)
			stored := newRemoteServer(t, s, transport, "https://old.example.com/mcp")

			_, err := s.Update(t.Context(), stored.ID, &Server{
				Transport: transport,
				Name:      "hosted",
				URL:       "https://new.example.com/mcp",
				// What the edit modal re-submits for a row the user did not touch.
				Headers: []KeyPair{{Name: "Authorization", Value: SecretMask}},
				Enabled: true,
			})
			if err == nil {
				t.Fatal("expected a refusal when the origin changes under a preserved header")
			}
			for _, want := range []string{"Authorization", "new.example.com"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should name %q", err, want)
				}
			}

			// Nothing may have been written: the old token stays attached to the
			// old origin.
			raw := s.EnabledRaw(t.Context())
			if len(raw) != 1 {
				t.Fatalf("stored = %d", len(raw))
			}
			if raw[0].URL != "https://old.example.com/mcp" {
				t.Errorf("url = %q, want the refusal to leave the record alone", raw[0].URL)
			}
			if raw[0].Headers[0].Value != "Bearer old-origin-token" {
				t.Errorf("header value = %q, want it untouched", raw[0].Headers[0].Value)
			}
		})
	}
}

func TestUpdate_URLOriginChangeRefusesAPreservedOAuthSecret(t *testing.T) {
	s := newTestStore(t)
	stored := newRemoteServer(t, s, TransportHTTP, "https://old.example.com/mcp")

	_, err := s.Update(t.Context(), stored.ID, &Server{
		Transport:         TransportHTTP,
		Name:              "hosted",
		URL:               "https://new.example.com/mcp",
		OAuthClientSecret: SecretMask,
		Enabled:           true,
	})
	if err == nil {
		t.Fatal("expected a refusal when the origin changes under a preserved oauth secret")
	}
	if !strings.Contains(err.Error(), "oauth_client_secret") {
		t.Errorf("error %q should name the field", err)
	}
	if got := s.EnabledRaw(t.Context())[0].OAuthClientSecret; got != "old-origin-secret" {
		t.Errorf("stored secret = %q, want it untouched", got)
	}
}

// Re-entering the value for the new origin is the way through. The guard refuses
// a PRESERVED secret, never a supplied one.
func TestUpdate_URLOriginChangeAcceptsARetypedSecret(t *testing.T) {
	s := newTestStore(t)
	stored := newRemoteServer(t, s, TransportHTTP, "https://old.example.com/mcp")

	if _, err := s.Update(t.Context(), stored.ID, &Server{
		Transport:         TransportHTTP,
		Name:              "hosted",
		URL:               "https://new.example.com/mcp",
		Headers:           []KeyPair{{Name: "Authorization", Value: "Bearer new-origin-token"}},
		OAuthClientSecret: "new-origin-secret",
		Enabled:           true,
	}); err != nil {
		t.Fatalf("a retyped secret must be accepted: %v", err)
	}
	raw := s.EnabledRaw(t.Context())
	if raw[0].Headers[0].Value != "Bearer new-origin-token" {
		t.Errorf("header = %q", raw[0].Headers[0].Value)
	}
	if raw[0].OAuthClientSecret != "new-origin-secret" {
		t.Errorf("oauth secret = %q", raw[0].OAuthClientSecret)
	}
}

// The negative half: without it the guard could be a blanket refusal of every
// remote edit and still pass the tests above.
func TestUpdate_SameOriginStillPreservesSecrets(t *testing.T) {
	cases := map[string]string{
		"identical url":     "https://old.example.com/mcp",
		"path changed only": "https://old.example.com/mcp/v2",
		"case of host":      "https://OLD.example.com/mcp",
		"query added":       "https://old.example.com/mcp?tenant=a",
	}
	for name, next := range cases {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			stored := newRemoteServer(t, s, TransportHTTP, "https://old.example.com/mcp")

			if _, err := s.Update(t.Context(), stored.ID, &Server{
				Transport:         TransportHTTP,
				Name:              "hosted",
				URL:               next,
				Headers:           []KeyPair{{Name: "Authorization", Value: SecretMask}},
				OAuthClientSecret: SecretMask,
				Enabled:           true,
			}); err != nil {
				t.Fatalf("same-origin edit must preserve secrets: %v", err)
			}
			raw := s.EnabledRaw(t.Context())
			if raw[0].Headers[0].Value != "Bearer old-origin-token" {
				t.Errorf("header = %q, want the stored value preserved",
					raw[0].Headers[0].Value)
			}
			if raw[0].OAuthClientSecret != "old-origin-secret" {
				t.Errorf("oauth secret = %q, want it preserved", raw[0].OAuthClientSecret)
			}
			if raw[0].URL != next {
				t.Errorf("url = %q, want %q", raw[0].URL, next)
			}
		})
	}
}

// A masked row with no stored counterpart preserves nothing (mergeSecrets sets
// it to ""), so there is nothing to refuse and the origin change goes through.
func TestUpdate_OriginChangeWithNoStoredSecretIsAllowed(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(t.Context(), &Server{
		Transport: TransportHTTP, Name: "hosted",
		URL: "https://old.example.com/mcp", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Update(t.Context(), created.ID, &Server{
		Transport: TransportHTTP, Name: "hosted",
		URL:     "https://new.example.com/mcp",
		Headers: []KeyPair{{Name: "X-Api-Key", Value: SecretMask}},
		Enabled: true,
	}); err != nil {
		t.Fatalf("no stored secret means nothing to protect: %v", err)
	}
	if got := s.EnabledRaw(t.Context())[0].URL; got != "https://new.example.com/mcp" {
		t.Errorf("url = %q", got)
	}
}

// End to end: the refusal reaches the browser as a 400 the panel renders inline,
// rather than a 500 or a silent success.
func TestHandleOne_PUT_URLOriginChangeIs400(t *testing.T) {
	s, mux := newRoutedStore(t)
	stored := newRemoteServer(t, s, TransportHTTP, "https://old.example.com/mcp")

	rec := doJSON(t, mux, http.MethodPut, "/api/mcp/"+stored.ID.String(), &Server{
		Transport: TransportHTTP,
		Name:      "hosted",
		URL:       "https://new.example.com/mcp",
		Headers:   []KeyPair{{Name: "Authorization", Value: SecretMask}},
		Enabled:   true,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Authorization") {
		t.Errorf("body = %s, want the header named", rec.Body.String())
	}
	// The refusal must not leak the secret it declined to send.
	if strings.Contains(rec.Body.String(), "old-origin-token") {
		t.Errorf("the error body carries the secret: %s", rec.Body.String())
	}
}
