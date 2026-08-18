package push

// crypto_derive_test.go: the RFC 8291 derivation contract. These tests came
// from the deleted internal/push/crypto sub-package, whose suite was stronger
// than this package's on the derivation itself (per-input sensitivity, full
// VAPID signature verification) while overlapping on determinism. Carried over
// rather than dropped with the package.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"
)

// baseKeyMaterial is a fixed, structurally valid input set. Values are
// distinguishable per field so a transposition shows up as a changed digest.
func baseKeyMaterial() keyMaterial {
	return keyMaterial{
		Shared:     bytes.Repeat([]byte{0x01}, 32),
		AuthSecret: bytes.Repeat([]byte{0x02}, 16),
		ClientPub:  bytes.Repeat([]byte{0x03}, 65),
		ServerPub:  bytes.Repeat([]byte{0x04}, 65),
		Salt:       bytes.Repeat([]byte{0x05}, 16),
	}
}

// flipFirstByte returns a copy of b with its first byte inverted, used to
// confirm a derivation actually depends on a given input.
func flipFirstByte(b []byte) []byte {
	c := slices.Clone(b)
	c[0] ^= 0xff
	return c
}

// deriveKeyNonce must yield a 16-byte AES-128-GCM key and a 12-byte GCM
// nonce (RFC 8291 lengths) and be deterministic for fixed inputs.
func TestDeriveKeyNonce_lengthsAndDeterminism(t *testing.T) {
	t.Parallel()

	km := baseKeyMaterial()
	cek, nonce, err := deriveKeyNonce(km)
	if err != nil {
		t.Fatalf("deriveKeyNonce: %v", err)
	}
	if len(cek) != 16 {
		t.Errorf("len(cek) = %d, want 16 (AES-128-GCM key)", len(cek))
	}
	if len(nonce) != 12 {
		t.Errorf("len(nonce) = %d, want 12 (GCM nonce)", len(nonce))
	}

	cek2, nonce2, err := deriveKeyNonce(km)
	if err != nil {
		t.Fatalf("deriveKeyNonce (second call): %v", err)
	}
	if !slices.Equal(cek, cek2) {
		t.Errorf("cek not deterministic: %x vs %x", cek, cek2)
	}
	if !slices.Equal(nonce, nonce2) {
		t.Errorf("nonce not deterministic: %x vs %x", nonce, nonce2)
	}
}

// Every field must feed the derivation: flipping any one of the five must
// change the derived key+nonce. Catches a derivation that silently drops an
// input (e.g. omitting a public key from the RFC 8291 info string, or ignoring
// the salt).
func TestDeriveKeyNonce_sensitiveToEveryField(t *testing.T) {
	t.Parallel()

	baseCEK, baseNonce, err := deriveKeyNonce(baseKeyMaterial())
	if err != nil {
		t.Fatalf("base deriveKeyNonce: %v", err)
	}

	tests := []struct {
		name    string
		mutated func(*keyMaterial)
	}{
		{"Shared", func(k *keyMaterial) { k.Shared = flipFirstByte(k.Shared) }},
		{"AuthSecret", func(k *keyMaterial) { k.AuthSecret = flipFirstByte(k.AuthSecret) }},
		{"ClientPub", func(k *keyMaterial) { k.ClientPub = flipFirstByte(k.ClientPub) }},
		{"ServerPub", func(k *keyMaterial) { k.ServerPub = flipFirstByte(k.ServerPub) }},
		{"Salt", func(k *keyMaterial) { k.Salt = flipFirstByte(k.Salt) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			km := baseKeyMaterial()
			tt.mutated(&km)
			cek, nonce, err := deriveKeyNonce(km)
			if err != nil {
				t.Fatalf("deriveKeyNonce: %v", err)
			}
			if slices.Equal(cek, baseCEK) && slices.Equal(nonce, baseNonce) {
				t.Errorf("changing %s left both cek and nonce unchanged", tt.name)
			}
		})
	}
}

// TestDeriveKeyNonce_transpositionDerivesADifferentKeySilently is the reason
// keyMaterial is a struct rather than five positional []byte parameters.
//
// It asserts the FAILURE MODE, not a guard: swapping two same-typed inputs does
// not error, it derives a different key. Nothing downstream can detect that —
// the payload encrypts fine and the subscriber's browser silently discards it,
// with no log on either side. So the field names are the only thing standing
// between a transposition and undeliverable push notifications, and this test
// exists so a future change back to positional arguments has to delete an
// explicit statement of what that would cost.
func TestDeriveKeyNonce_transpositionDerivesADifferentKeySilently(t *testing.T) {
	t.Parallel()

	want, wantNonce, err := deriveKeyNonce(baseKeyMaterial())
	if err != nil {
		t.Fatalf("deriveKeyNonce: %v", err)
	}

	// The two public keys are the most transposable pair: same type, same
	// length, adjacent, and the RFC's info string is the only thing that
	// distinguishes them.
	km := baseKeyMaterial()
	km.ClientPub, km.ServerPub = km.ServerPub, km.ClientPub

	got, gotNonce, err := deriveKeyNonce(km)
	if err != nil {
		t.Fatalf("transposed deriveKeyNonce returned an error, so the swap is "+
			"detectable after all and this test's premise is stale: %v", err)
	}
	if slices.Equal(got, want) && slices.Equal(gotNonce, wantNonce) {
		t.Fatal("swapping ClientPub and ServerPub derived the SAME key, so the " +
			"RFC 8291 info string is not ordering them; the auth info must be " +
			"clientPub then serverPub")
	}
}

// buildVAPIDHeader produces a "vapid t=<jwt>, k=<pub>" header whose JWT
// carries the ES256 header, the endpoint-derived audience, the subject, a
// future expiry, and a signature that verifies against the signing key.
// The signature round-trip is the real contract a push gateway enforces.
func TestBuildVAPIDHeader_verifies(t *testing.T) {
	t.Parallel()

	ecdhKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	priv, err := ecdhToECDSA(ecdhKey)
	if err != nil {
		t.Fatalf("ecdhToECDSA: %v", err)
	}

	const pubB64 = "BPublicKeyPlaceholderBase64UrlValue"
	const subject = "mailto:admin@example.com"
	const endpoint = "https://fcm.googleapis.com/fcm/send/abc123"

	hdr, err := buildVAPIDHeader(priv, pubB64, subject, endpoint)
	if err != nil {
		t.Fatalf("buildVAPIDHeader: %v", err)
	}

	if !strings.HasPrefix(hdr, "vapid t=") {
		t.Fatalf("header = %q, want prefix %q", hdr, "vapid t=")
	}
	if !strings.HasSuffix(hdr, ", k="+pubB64) {
		t.Fatalf("header = %q, want suffix %q", hdr, ", k="+pubB64)
	}
	jwt := strings.TrimSuffix(strings.TrimPrefix(hdr, "vapid t="), ", k="+pubB64)

	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d segments, want 3", len(parts))
	}

	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode jwt header: %v", err)
	}
	if string(hdrJSON) != `{"typ":"JWT","alg":"ES256"}` {
		t.Errorf("jwt header = %s, want ES256 JWT header", hdrJSON)
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode jwt claims: %v", err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.Aud != "https://fcm.googleapis.com" {
		t.Errorf("aud = %q, want %q", claims.Aud, "https://fcm.googleapis.com")
	}
	if claims.Sub != subject {
		t.Errorf("sub = %q, want %q", claims.Sub, subject)
	}
	now := time.Now().Unix()
	if claims.Exp <= now {
		t.Errorf("exp = %d, want a future time (now = %d)", claims.Exp, now)
	}
	if claims.Exp > now+vapidExpWindow+60 {
		t.Errorf("exp = %d, want <= now+window (%d)", claims.Exp, now+vapidExpWindow+60)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode jwt signature: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature length = %d, want 64 (r||s)", len(sig))
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&priv.PublicKey, hash[:], r, s) {
		t.Error("VAPID JWT signature failed to verify against the signing key")
	}
}
