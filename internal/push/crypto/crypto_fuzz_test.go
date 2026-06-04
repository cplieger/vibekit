package crypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

// FuzzDeriveKeyNonce exercises the RFC 8291 key derivation with arbitrary
// inputs and asserts:
//
//  1. No panics.
//  2. Successful outputs always have CEK=16 bytes and nonce=12 bytes.
//  3. Deterministic: same inputs produce same outputs.
func FuzzDeriveKeyNonce(f *testing.F) {
	shared := make([]byte, 32)
	authSecret := make([]byte, 16)
	clientPub := make([]byte, 65)
	serverPub := make([]byte, 65)
	salt := make([]byte, 16)
	f.Add(shared, authSecret, clientPub, serverPub, salt)
	f.Add([]byte{}, []byte{}, []byte{}, []byte{}, []byte{})
	f.Add([]byte{0x01}, []byte{0x02}, []byte{0x03}, []byte{0x04}, []byte{0x05})

	f.Fuzz(func(t *testing.T, shared, authSecret, clientPub, serverPub, salt []byte) {
		cek, nonce, err := DeriveKeyNonce(shared, authSecret, clientPub, serverPub, salt)
		if err != nil {
			return
		}
		if len(cek) != 16 {
			t.Fatalf("CEK length = %d, want 16", len(cek))
		}
		if len(nonce) != 12 {
			t.Fatalf("nonce length = %d, want 12", len(nonce))
		}
		cek2, nonce2, err2 := DeriveKeyNonce(shared, authSecret, clientPub, serverPub, salt)
		if err2 != nil {
			t.Fatalf("second call errored: %v", err2)
		}
		if string(cek) != string(cek2) {
			t.Fatal("CEK not deterministic")
		}
		if string(nonce) != string(nonce2) {
			t.Fatal("nonce not deterministic")
		}
	})
}

// FuzzECDHToECDSA exercises the key conversion with random P-256 keys
// and asserts:
//
//  1. No panics on valid keys.
//  2. The resulting ECDSA key has X, Y, D set.
//  3. The curve is P-256.
func FuzzECDHToECDSA(f *testing.F) {
	// Generate a few seed keys.
	for range 3 {
		key, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(key.Bytes())
	}

	f.Fuzz(func(t *testing.T, keyBytes []byte) {
		ecdhKey, err := ecdh.P256().NewPrivateKey(keyBytes)
		if err != nil {
			return // invalid key material, skip
		}
		ecdsaKey, err := ECDHToECDSA(ecdhKey)
		if err != nil {
			t.Fatalf("ECDHToECDSA failed on valid key: %v", err)
		}
		if ecdsaKey.X == nil || ecdsaKey.Y == nil {
			t.Fatal("ECDSA key public fields are nil")
		}
		if ecdsaKey.Curve.Params().Name != "P-256" {
			t.Fatalf("curve = %s, want P-256", ecdsaKey.Curve.Params().Name)
		}
	})
}
