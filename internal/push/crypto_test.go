package push

// Tests for crypto.go: the VAPID header builder, the RFC 8291 key/nonce
// derivation, and a full encrypt/decrypt round-trip.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestVAPIDHeader(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, testSubject)

	header, err := s.vapidHeader("https://fcm.googleapis.com/fcm/send/abc123")
	if err != nil {
		t.Fatalf("vapidHeader: %v", err)
	}
	if !strings.HasPrefix(header, "vapid t=") {
		t.Errorf("header should start with 'vapid t=', got %q", header[:20])
	}
	if !strings.Contains(header, ", k=") {
		t.Error("header missing ', k=' public key")
	}

	// Verify the JWT signature.
	parts := strings.SplitN(strings.TrimPrefix(header, "vapid t="), ", k=", 2)
	token := parts[0]
	segments := strings.SplitN(token, ".", 3)
	if len(segments) != 3 {
		t.Fatalf("JWT has %d segments, want 3", len(segments))
	}

	// Decode claims and check audience.
	claimsJSON, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	if claims.Aud != "https://fcm.googleapis.com" {
		t.Errorf("aud = %q, want https://fcm.googleapis.com", claims.Aud)
	}
	if claims.Sub != testSubject {
		t.Errorf("sub = %q", claims.Sub)
	}

	// Verify ECDSA signature.
	sigBytes, err := base64.RawURLEncoding.DecodeString(segments[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if len(sigBytes) != 64 {
		t.Fatalf("sig length = %d, want 64", len(sigBytes))
	}
	priv, _ := s.decodeVAPIDPrivateKey()
	hash := sha256.Sum256([]byte(segments[0] + "." + segments[1]))
	if !ecdsa.Verify(&priv.PublicKey, hash[:], decodeBigInt(sigBytes[:32]), decodeBigInt(sigBytes[32:])) {
		t.Fatal("signature verification failed")
	}
}

func decodeBigInt(b []byte) *big.Int {
	return new(big.Int).SetBytes(b)
}

// TestVAPIDHeader_ExpWithin12h pins the 12h window so a future tweak
// back toward RFC's 24h ceiling doesn't slip past review.
func TestVAPIDHeader_ExpWithin12h(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, testSubject)
	before := time.Now().Unix()
	header, err := s.vapidHeader("https://fcm.googleapis.com/fcm/send/x")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.SplitN(strings.TrimPrefix(header, "vapid t="), ", k=", 2)[0]
	seg := strings.Split(token, ".")[1]
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(seg)
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	delta := claims.Exp - before
	if delta <= 0 || delta > 12*3600+5 {
		t.Errorf("exp delta = %ds, want (0, 12h+5s] for 12h window", delta)
	}
}

func TestDeriveKeyNonce_Deterministic(t *testing.T) {
	shared := make([]byte, 32)
	auth := make([]byte, 16)
	clientPub := make([]byte, 65)
	serverPub := make([]byte, 65)
	salt := make([]byte, 16)

	// Same inputs should produce same outputs.
	cek1, nonce1, err := deriveKeyNonce(keyMaterial{Shared: shared, AuthSecret: auth, ClientPub: clientPub, ServerPub: serverPub, Salt: salt})
	if err != nil {
		t.Fatal(err)
	}
	cek2, nonce2, err := deriveKeyNonce(keyMaterial{Shared: shared, AuthSecret: auth, ClientPub: clientPub, ServerPub: serverPub, Salt: salt})
	if err != nil {
		t.Fatal(err)
	}
	if string(cek1) != string(cek2) {
		t.Error("CEK not deterministic")
	}
	if string(nonce1) != string(nonce2) {
		t.Error("nonce not deterministic")
	}
	if len(cek1) != 16 {
		t.Errorf("CEK length = %d, want 16", len(cek1))
	}
	if len(nonce1) != 12 {
		t.Errorf("nonce length = %d, want 12", len(nonce1))
	}
}

func TestDeriveKeyNonce_DifferentSalts(t *testing.T) {
	shared := make([]byte, 32)
	auth := make([]byte, 16)
	clientPub := make([]byte, 65)
	serverPub := make([]byte, 65)

	salt1 := make([]byte, 16)
	salt1[0] = 1
	salt2 := make([]byte, 16)
	salt2[0] = 2

	cek1, _, _ := deriveKeyNonce(keyMaterial{Shared: shared, AuthSecret: auth, ClientPub: clientPub, ServerPub: serverPub, Salt: salt1})
	cek2, _, _ := deriveKeyNonce(keyMaterial{Shared: shared, AuthSecret: auth, ClientPub: clientPub, ServerPub: serverPub, Salt: salt2})
	if string(cek1) == string(cek2) {
		t.Error("different salts should produce different CEKs")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	// Simulate a full encrypt/decrypt cycle using the same key derivation.
	clientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	salt := make([]byte, 16)
	rand.Read(salt)

	// Server encrypts.
	shared, _ := serverPriv.ECDH(clientPriv.PublicKey())
	cek, nonce, err := deriveKeyNonce(keyMaterial{
		Shared:     shared,
		AuthSecret: authSecret,
		ClientPub:  clientPriv.PublicKey().Bytes(),
		ServerPub:  serverPriv.PublicKey().Bytes(),
		Salt:       salt,
	})
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("hello push")
	// RFC 8188 §2.1: payload || 0x02 padding-delimiter (single last record).
	padded := make([]byte, len(plaintext)+1)
	copy(padded, plaintext)
	padded[len(plaintext)] = 0x02

	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	ciphertext := gcm.Seal(nil, nonce, padded, nil)

	// Client decrypts (same shared secret from the other direction).
	shared2, _ := clientPriv.ECDH(serverPriv.PublicKey())
	cek2, nonce2, _ := deriveKeyNonce(keyMaterial{
		Shared:     shared2,
		AuthSecret: authSecret,
		ClientPub:  clientPriv.PublicKey().Bytes(),
		ServerPub:  serverPriv.PublicKey().Bytes(),
		Salt:       salt,
	})

	if string(cek) != string(cek2) {
		t.Fatal("CEK mismatch between encrypt and decrypt sides")
	}
	if string(nonce) != string(nonce2) {
		t.Fatal("nonce mismatch")
	}

	block2, _ := aes.NewCipher(cek2)
	gcm2, _ := cipher.NewGCM(block2)
	decrypted, err := gcm2.Open(nil, nonce2, ciphertext, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	// Strip the trailing 0x02 padding-delimiter (RFC 8188 §2.1).
	if len(decrypted) == 0 || decrypted[len(decrypted)-1] != 0x02 {
		t.Fatalf("decrypted missing 0x02 delimiter: % x", decrypted)
	}
	result := decrypted[:len(decrypted)-1]
	if string(result) != "hello push" {
		t.Errorf("decrypted = %q, want %q", result, "hello push")
	}
}

func BenchmarkPushEncrypt(b *testing.B) {
	// Generate realistic key material once.
	clientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	clientPubBytes := clientPriv.PublicKey().Bytes()
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		b.Fatal(err)
	}
	payload := []byte(`{"title":"Agent finished","body":"Task completed"}`)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ephPriv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			b.Fatal(err)
		}
		shared, err := ephPriv.ECDH(clientPriv.PublicKey())
		if err != nil {
			b.Fatal(err)
		}
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			b.Fatal(err)
		}
		cek, nonce, err := deriveKeyNonce(keyMaterial{Shared: shared, AuthSecret: authSecret, ClientPub: clientPubBytes, ServerPub: ephPriv.PublicKey().Bytes(), Salt: salt})
		if err != nil {
			b.Fatal(err)
		}
		padded := make([]byte, len(payload)+1)
		copy(padded, payload)
		padded[len(payload)] = 0x02
		block, err := aes.NewCipher(cek)
		if err != nil {
			b.Fatal(err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			b.Fatal(err)
		}
		gcm.Seal(nil, nonce, padded, nil)
	}
}
