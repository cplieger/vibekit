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

	"github.com/cplieger/vibekit/internal/vibekit"
)

func newRoutedService(t *testing.T) (*Service, *http.ServeMux) {
	t.Helper()
	// Not t.Context(): the service context must outlive the t.Cleanup(s.Close)
	// teardown, and t.Context() is already cancelled when cleanup funcs run.
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
	s.Subscribe(vibekit.PushSubscription{Endpoint: "https://push.example.com/gone"})
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

// TestIsAllowedPushEndpointIsByteExactOnHost states the case decision the gate
// makes silently, and it is a DELIBERATE refusal rather than an oversight.
//
// url.Parse preserves host case (measured on go1.27.0: Hostname() of
// https://FCM.GOOGLEAPIS.COM/… is "FCM.GOOGLEAPIS.COM"), and both comparisons in
// isAllowedPushEndpoint are byte-exact, so an upper- or mixed-case spelling of an
// allowed vendor is REFUSED. RFC 3986 §3.2.2 makes the host case-insensitive, so
// that is a spec deviation — and the direction of failure is what makes it the
// safe one: this is an ALLOW-LIST, so a refusal costs the subscriber their
// notifications at subscribe time, while a widening costs the process an SSRF
// primitive. Every browser emits its own service host lowercase, so nothing real
// is refused today.
//
// The reason to pin it is the FIX someone will reach for. Neither standard
// case-insensitive spelling is safe here, and they do not even agree with each
// other. Measured exhaustively over the whole rune space on go1.27.0, one
// substituted rune at a time: strings.EqualFold admits 17 / 33 / 17 / 13 distinct
// single-rune aliases of the four allow-list hosts and strings.ToLower admits
// 17 / 31 / 18 / 12. The differences are non-ASCII and go BOTH ways — EqualFold
// admits U+017F LATIN SMALL LETTER LONG S for the "s" in "services", which
// ToLower does not; ToLower admits U+0130 LATIN CAPITAL LETTER I WITH DOT ABOVE
// for the "i" in "notify", which EqualFold does not. Each such alias is a
// DIFFERENT DNS name, so either spelling turns the allow-list into a list of
// hosts plus their homoglyphs. Unicode 17 widened SimpleFold by 116 runes with
// zero removals, so a fold-based gate can only ever get more permissive on a
// later toolchain, never less: that is the fail-open direction, and it is the
// defect that shipped in a sibling app's Host allow-list.
//
// If case-insensitivity is ever wanted, the fix is ASCII-only normalisation of
// the STORED endpoint — not a fold in this predicate — because vapidHeader
// derives the RFC 8292 `aud` from u.Host, so the gate, the POST target and the
// JWT audience all have to agree on one spelling.
//
// The byte-exact form is also the reason this whole predicate is Unicode-version
// independent: == and strings.HasSuffix consult no table, so no Unicode upgrade
// can move it in either direction.
func TestIsAllowedPushEndpointIsByteExactOnHost(t *testing.T) {
	refused := []string{
		// ASCII case, which RFC 3986 says is equivalent and this gate does not.
		"https://FCM.GOOGLEAPIS.COM/fcm/send/abc",
		"https://Fcm.GoogleAPIs.Com/fcm/send/abc",
		"https://WEB.PUSH.APPLE.COM/Q123",
		"https://SN1-WNS.NOTIFY.WINDOWS.COM/wnsapi/foo",
		// The two fold aliases a case-insensitive rewrite would let in. Both are
		// distinct DNS names; neither may ever be accepted.
		"https://updates.push.\u017Fervices.mozilla.com/wpush/v2/xyz", // EqualFold-only
		"https://sn1-wns.not\u0130fy.windows.com/wnsapi/foo",          // ToLower-only
	}
	for _, in := range refused {
		if isAllowedPushEndpoint(in) {
			t.Errorf("isAllowedPushEndpoint(%q) = true; the host match must stay byte-exact", in)
		}
	}
}
