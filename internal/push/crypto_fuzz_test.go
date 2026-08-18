package push

import (
	"encoding/base64"
	"strings"
	"testing"
)

// FuzzVAPIDHeader explores the endpoint URL grammar space to verify that
// vapidHeader never panics on arbitrary input and that successful outputs
// conform to the RFC 8292 header format.
func FuzzVAPIDHeader(f *testing.F) {
	f.Add("https://fcm.googleapis.com/fcm/send/abc123")
	f.Add("https://updates.push.services.mozilla.com/wpush/v2/xyz")
	f.Add("https://web.push.apple.com/Q123")
	f.Add("")
	f.Add("not-a-url")
	f.Add("://missing-scheme")
	f.Add("https://")
	f.Add("https://host:8080/path")
	f.Add("file:///etc/passwd")
	f.Add("https://example.com/path?query=1#frag")

	dir := f.TempDir()
	s := New(f.Context(), dir, "mailto:fuzz@example.com")

	f.Fuzz(func(t *testing.T, endpoint string) {
		hdr, err := s.vapidHeader(endpoint)
		if err != nil {
			return // errors are acceptable for malformed input
		}
		// Invariant 1: header starts with "vapid t="
		if !strings.HasPrefix(hdr, "vapid t=") {
			t.Fatalf("header missing 'vapid t=' prefix: %q", hdr)
		}
		// Invariant 2: header contains ", k="
		if !strings.Contains(hdr, ", k=") {
			t.Fatalf("header missing ', k=' segment: %q", hdr)
		}
		// Invariant 3: the JWT (between "vapid t=" and ", k=") has 3 dot-separated segments
		kIdx := strings.Index(hdr, ", k=")
		jwt := hdr[len("vapid t="):kIdx]
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Fatalf("JWT has %d segments, want 3: %q", len(parts), jwt)
		}
	})
}

// FuzzDeriveKeyNonce explores the input space of the RFC 8291 key derivation
// to verify that deriveKeyNonce never panics on arbitrary-length inputs and
// that successful outputs always have the correct lengths (16-byte CEK,
// 12-byte nonce) and are deterministic.
func FuzzDeriveKeyNonce(f *testing.F) {
	// Seed with realistic P-256 key sizes.
	shared := make([]byte, 32)
	authSecret := make([]byte, 16)
	clientPub := make([]byte, 65)
	serverPub := make([]byte, 65)
	salt := make([]byte, 16)
	f.Add(shared, authSecret, clientPub, serverPub, salt)

	// Seed with empty inputs.
	f.Add([]byte{}, []byte{}, []byte{}, []byte{}, []byte{})

	// Seed with single-byte inputs.
	f.Add([]byte{0x01}, []byte{0x02}, []byte{0x03}, []byte{0x04}, []byte{0x05})

	// Seed with base64-decoded real VAPID key material.
	realPub, _ := base64.RawURLEncoding.DecodeString("BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8p2l5qN2A")
	f.Add(shared, authSecret, realPub, serverPub, salt)

	f.Fuzz(func(t *testing.T, shared, authSecret, clientPub, serverPub, salt []byte) {
		cek, nonce, err := deriveKeyNonce(keyMaterial{Shared: shared, AuthSecret: authSecret, ClientPub: clientPub, ServerPub: serverPub, Salt: salt})
		if err != nil {
			return // errors are acceptable
		}
		// Invariant 1: CEK is always 16 bytes.
		if len(cek) != 16 {
			t.Fatalf("CEK length = %d, want 16", len(cek))
		}
		// Invariant 2: nonce is always 12 bytes.
		if len(nonce) != 12 {
			t.Fatalf("nonce length = %d, want 12", len(nonce))
		}
		// Invariant 3: deterministic — same inputs produce same outputs.
		cek2, nonce2, err2 := deriveKeyNonce(keyMaterial{Shared: shared, AuthSecret: authSecret, ClientPub: clientPub, ServerPub: serverPub, Salt: salt})
		if err2 != nil {
			t.Fatalf("second call errored but first succeeded: %v", err2)
		}
		if string(cek) != string(cek2) {
			t.Fatal("CEK not deterministic")
		}
		if string(nonce) != string(nonce2) {
			t.Fatal("nonce not deterministic")
		}
	})
}
