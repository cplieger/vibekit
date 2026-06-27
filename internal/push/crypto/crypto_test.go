package crypto

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

// flipFirstByte returns a copy of b with its first byte inverted, used to
// confirm a derivation actually depends on a given input.
func flipFirstByte(b []byte) []byte {
	c := slices.Clone(b)
	c[0] ^= 0xff
	return c
}

// DeriveKeyNonce must yield a 16-byte AES-128-GCM key and a 12-byte GCM
// nonce (RFC 8291 lengths) and be deterministic for fixed inputs.
func TestDeriveKeyNonce_lengthsAndDeterminism(t *testing.T) {
	shared := bytes.Repeat([]byte{0x01}, 32)
	authSecret := bytes.Repeat([]byte{0x02}, 16)
	clientPub := bytes.Repeat([]byte{0x03}, 65)
	serverPub := bytes.Repeat([]byte{0x04}, 65)
	salt := bytes.Repeat([]byte{0x05}, 16)

	cek, nonce, err := DeriveKeyNonce(shared, authSecret, clientPub, serverPub, salt)
	if err != nil {
		t.Fatalf("DeriveKeyNonce: %v", err)
	}
	if len(cek) != 16 {
		t.Errorf("len(cek) = %d, want 16 (AES-128-GCM key)", len(cek))
	}
	if len(nonce) != 12 {
		t.Errorf("len(nonce) = %d, want 12 (GCM nonce)", len(nonce))
	}

	cek2, nonce2, err := DeriveKeyNonce(shared, authSecret, clientPub, serverPub, salt)
	if err != nil {
		t.Fatalf("DeriveKeyNonce (second call): %v", err)
	}
	if !slices.Equal(cek, cek2) {
		t.Errorf("cek not deterministic: %x vs %x", cek, cek2)
	}
	if !slices.Equal(nonce, nonce2) {
		t.Errorf("nonce not deterministic: %x vs %x", nonce, nonce2)
	}
}

// Every input must feed the derivation: flipping any one of the five
// arguments must change the derived key+nonce. Catches a derivation that
// silently drops an input (e.g. omitting the client/server public key from
// the RFC 8291 info string, or ignoring the salt).
func TestDeriveKeyNonce_sensitiveToInputs(t *testing.T) {
	shared := bytes.Repeat([]byte{0x01}, 32)
	authSecret := bytes.Repeat([]byte{0x02}, 16)
	clientPub := bytes.Repeat([]byte{0x03}, 65)
	serverPub := bytes.Repeat([]byte{0x04}, 65)
	salt := bytes.Repeat([]byte{0x05}, 16)

	baseCEK, baseNonce, err := DeriveKeyNonce(shared, authSecret, clientPub, serverPub, salt)
	if err != nil {
		t.Fatalf("base DeriveKeyNonce: %v", err)
	}

	tests := []struct {
		name                          string
		shared, authSecret, clientPub []byte
		serverPub, salt               []byte
	}{
		{name: "shared", shared: flipFirstByte(shared), authSecret: authSecret, clientPub: clientPub, serverPub: serverPub, salt: salt},
		{name: "authSecret", shared: shared, authSecret: flipFirstByte(authSecret), clientPub: clientPub, serverPub: serverPub, salt: salt},
		{name: "clientPub", shared: shared, authSecret: authSecret, clientPub: flipFirstByte(clientPub), serverPub: serverPub, salt: salt},
		{name: "serverPub", shared: shared, authSecret: authSecret, clientPub: clientPub, serverPub: flipFirstByte(serverPub), salt: salt},
		{name: "salt", shared: shared, authSecret: authSecret, clientPub: clientPub, serverPub: serverPub, salt: flipFirstByte(salt)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cek, nonce, err := DeriveKeyNonce(tt.shared, tt.authSecret, tt.clientPub, tt.serverPub, tt.salt)
			if err != nil {
				t.Fatalf("DeriveKeyNonce: %v", err)
			}
			if slices.Equal(cek, baseCEK) && slices.Equal(nonce, baseNonce) {
				t.Errorf("changing %s left both cek and nonce unchanged", tt.name)
			}
		})
	}
}

// BuildVAPIDHeader produces a "vapid t=<jwt>, k=<pub>" header whose JWT
// carries the ES256 header, the endpoint-derived audience, the subject, a
// future expiry, and a signature that verifies against the signing key.
// The signature round-trip is the real contract a push gateway enforces.
func TestBuildVAPIDHeader_verifies(t *testing.T) {
	ecdhKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	priv, err := ECDHToECDSA(ecdhKey)
	if err != nil {
		t.Fatalf("ECDHToECDSA: %v", err)
	}

	const pubB64 = "BPublicKeyPlaceholderBase64UrlValue"
	const subject = "mailto:admin@example.com"
	const endpoint = "https://fcm.googleapis.com/fcm/send/abc123"

	hdr, err := BuildVAPIDHeader(priv, pubB64, subject, endpoint)
	if err != nil {
		t.Fatalf("BuildVAPIDHeader: %v", err)
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
	if claims.Exp > now+VAPIDExpWindow+60 {
		t.Errorf("exp = %d, want <= now+window (%d)", claims.Exp, now+VAPIDExpWindow+60)
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
