package push

// Tests for routes.go: the three HTTP endpoints exposed by RegisterRoutes.
// VAPID-key read, subscribe, unsubscribe — success and error paths.

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vibekit/internal/api"
)

func newRoutedService(t *testing.T) (*Service, *http.ServeMux) {
	t.Helper()
	s := New(context.Background(), t.TempDir(), "mailto:test@example.com")
	t.Cleanup(s.Close)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	return s, mux
}

func TestHandleVAPIDKey_ReturnsPublicKey(t *testing.T) {
	s, mux := newRoutedService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/push/vapid-key", http.NoBody)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	want := `"publicKey":"` + s.PublicKey() + `"`
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("body = %q, want substring %q", rec.Body.String(), want)
	}
}

func TestHandleSubscribe_PersistsSubscription(t *testing.T) {
	s, mux := newRoutedService(t)

	// Build RFC-compliant keys: p256dh is the 65-byte uncompressed
	// P-256 point (0x04 || X(32) || Y(32)); auth is 16 random bytes.
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	p256dh := base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	authRaw := make([]byte, 16)
	if _, err := rand.Read(authRaw); err != nil {
		t.Fatalf("auth: %v", err)
	}
	auth := base64.RawURLEncoding.EncodeToString(authRaw)
	body := `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc",` +
		`"keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"}}`

	req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !s.HasSubscribers() {
		t.Error("subscription not persisted")
	}
}

func TestHandleSubscribe_RejectsInvalidKeyMaterial(t *testing.T) {
	_, mux := newRoutedService(t)

	// Short p256dh (non-65-byte) — RFC 8291 §3.2 violation.
	body := `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc",` +
		`"keys":{"p256dh":"ABCDEF","auth":"XYZ"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("short p256dh: code = %d, want 400", rec.Code)
	}

	// Valid p256dh length but wrong auth length.
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	p256dh := base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	body2 := `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc",` +
		`"keys":{"p256dh":"` + p256dh + `","auth":"dG9vLXNob3J0"}}` // 8 bytes, want 16
	req2 := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(body2))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Errorf("short auth: code = %d, want 400", rec2.Code)
	}
}

func TestHandleSubscribe_RejectsNonPOST(t *testing.T) {
	_, mux := newRoutedService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/push/subscribe", http.NoBody)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", rec.Code)
	}
}

func TestHandleSubscribe_RejectsInvalidJSON(t *testing.T) {
	_, mux := newRoutedService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestHandleSubscribe_RejectsEmptyEndpoint(t *testing.T) {
	_, mux := newRoutedService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(`{"endpoint":""}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestHandleUnsubscribe_RemovesSubscription(t *testing.T) {
	s, mux := newRoutedService(t)
	s.Subscribe(api.PushSubscription{Endpoint: "https://push.example.com/gone"})
	if !s.HasSubscribers() {
		t.Fatal("precondition: subscriber not added")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/push/unsubscribe",
		strings.NewReader(`{"endpoint":"https://push.example.com/gone"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if s.HasSubscribers() {
		t.Error("subscriber still present after unsubscribe")
	}
}

func TestHandleUnsubscribe_RejectsNonPOST(t *testing.T) {
	_, mux := newRoutedService(t)

	req := httptest.NewRequest(http.MethodGet, "/api/push/unsubscribe", http.NoBody)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", rec.Code)
	}
}

func TestHandleUnsubscribe_RejectsEmptyEndpoint(t *testing.T) {
	_, mux := newRoutedService(t)

	req := httptest.NewRequest(http.MethodPost, "/api/push/unsubscribe", strings.NewReader(`{"endpoint":""}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

// --- SSRF guardrails on the push endpoint allowlist ---

func TestHandleSubscribe_RejectsSSRFEndpoints(t *testing.T) {
	// The push service POSTs encrypted payloads to whatever endpoint
	// the subscriber claimed. Without an allowlist, an attacker could
	// point the server at internal services (localhost, cloud metadata,
	// RFC 1918 ranges) and turn every push event into an SSRF. Only
	// real browser push hosts should be accepted.
	_, mux := newRoutedService(t)
	bad := []string{
		"http://localhost:6379/SHUTDOWN",
		"https://169.254.169.254/latest/meta-data/",
		"https://10.0.0.1/internal",
		"https://evil.com/track",
		"file:///etc/passwd",
		"ftp://example.com/",
		"javascript:alert(1)",
		"https://fcm.googleapis.com.evil.com/fcm/send/x",
		// Suffix match guard: must start with '.' so it can't be
		// fooled by "evilnotify.windows.com".
		"https://fakenotify.windows.com/abc",
	}
	for _, endpoint := range bad {
		body := `{"endpoint":` + jsonQuote(endpoint) + `,"keys":{"p256dh":"x","auth":"y"}}`
		req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("endpoint %q accepted (code=%d)", endpoint, rec.Code)
		}
	}
}

func TestHandleSubscribe_AcceptsBrowserPushHosts(t *testing.T) {
	_, mux := newRoutedService(t)
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	p256dh := base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	authRaw := make([]byte, 16)
	if _, err := rand.Read(authRaw); err != nil {
		t.Fatalf("auth: %v", err)
	}
	auth := base64.RawURLEncoding.EncodeToString(authRaw)
	good := []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/xyz",
		"https://web.push.apple.com/QAf...",
		"https://sn1-wns.notify.windows.com/wnsapi/foo",
	}
	for _, endpoint := range good {
		body := `{"endpoint":` + jsonQuote(endpoint) +
			`,"keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"}}`
		req := httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("endpoint %q rejected (code=%d, body=%s)",
				endpoint, rec.Code, rec.Body.String())
		}
	}
}

func jsonQuote(s string) string {
	// Minimal JSON string quoter for tests. Handles the only escapes
	// we need (backslash + quote); test data uses no embedded
	// newlines or control characters.
	var out strings.Builder
	out.Grow(len(s) + 2)
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// --- isAllowedPushEndpoint (pure function, covers parse-failure and empty-host branches) ---

func TestIsAllowedPushEndpoint(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Allowed primary hosts.
		{in: "https://fcm.googleapis.com/fcm/send/abc", want: true},
		{in: "https://updates.push.services.mozilla.com/wpush/v2/xyz", want: true},
		{in: "https://web.push.apple.com/Q123", want: true},

		// Allowed suffix matches.
		{in: "https://sn1-wns.notify.windows.com/wnsapi/foo", want: true},
		{in: "https://somehost.push.apple.com/x", want: true},

		// Scheme rejections.
		{in: "http://fcm.googleapis.com/fcm/send/abc", want: false},
		{in: "ws://fcm.googleapis.com/fcm/send/abc", want: false},
		{in: "file:///etc/passwd", want: false},

		// SSRF vectors.
		{in: "https://localhost/push", want: false},
		{in: "https://127.0.0.1/push", want: false},
		{in: "https://169.254.169.254/latest/meta-data/", want: false},
		{in: "https://10.0.0.1/internal", want: false},

		// Suffix-forgery defences (must require leading dot).
		{in: "https://evilnotify.windows.com/abc", want: false},
		{in: "https://fcm.googleapis.com.evil.com/fcm/send/x", want: false},
		{in: "https://fakepush.apple.com/x", want: false},

		// Empty host.
		{in: "", want: false},
		{in: "https://", want: false},

		// url.Parse rejections (control characters, malformed escapes).
		{in: "https://\x00nul-byte", want: false},
		{in: "https://%zz-bad-escape", want: false},

		// Explicit port rejected (aud mismatch risk).
		{in: "https://fcm.googleapis.com:1234/send/abc", want: false},
		{in: "https://fcm.googleapis.com:443/send/abc", want: false},
	}
	for _, tc := range cases {
		if got := isAllowedPushEndpoint(tc.in); got != tc.want {
			t.Errorf("isAllowedPushEndpoint(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
