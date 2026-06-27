package crypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

// FuzzECDHToECDSA verifies the ECDH→ECDSA P-256 key conversion preserves
// the X/Y coordinates of the public key and yields a valid ECDSA private
// key on the P-256 curve.
//
// Bug class: silent key corruption when porting an ECDH key into the
// ECDSA structure used to sign VAPID JWTs. A coordinate truncation,
// off-by-one on the uncompressed-point split, or wrong curve constant
// would generate JWTs that fail verification at the push gateway.
func FuzzECDHToECDSA(f *testing.F) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		f.Fatalf("seed: GenerateKey: %v", err)
	}
	f.Add(priv.Bytes())

	f.Fuzz(func(t *testing.T, dBytes []byte) {
		key, err := ecdh.P256().NewPrivateKey(dBytes)
		if err != nil {
			t.Skip()
		}

		ec, err := ECDHToECDSA(key)
		if err != nil {
			t.Fatalf("ECDHToECDSA: %v", err)
		}

		if ec.Curve == nil || ec.Curve.Params().Name != "P-256" {
			t.Fatalf("curve = %v; want P-256", ec.Curve)
		}
		if ec.X == nil || ec.Y == nil {
			t.Fatalf("nil component: X=%v Y=%v", ec.X, ec.Y)
		}

		raw := key.PublicKey().Bytes()
		if len(raw) != 65 || raw[0] != 0x04 {
			t.Fatalf("unexpected public key format: len=%d head=%x", len(raw), raw[0])
		}
		if got, want := ec.X.Bytes(), trimLeadingZeros(raw[1:33]); !bytesEqual(got, want) {
			t.Fatalf("X mismatch: got %x want %x", got, want)
		}
		if got, want := ec.Y.Bytes(), trimLeadingZeros(raw[33:65]); !bytesEqual(got, want) {
			t.Fatalf("Y mismatch: got %x want %x", got, want)
		}
	})
}

func trimLeadingZeros(b []byte) []byte {
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	return b
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// FuzzDeriveKeyNonce feeds arbitrary byte inputs: clientPub and authSecret
// originate from the browser's push subscription, so they are external,
// attacker-influenced input. Invariants: never panics; on success the CEK
// is 16 bytes (AES-128-GCM) and the nonce is 12 bytes (GCM); and the
// derivation is deterministic for identical inputs.
func FuzzDeriveKeyNonce(f *testing.F) {
	f.Add(make([]byte, 32), make([]byte, 16), make([]byte, 65), make([]byte, 65), make([]byte, 16))
	f.Add([]byte("shared"), []byte("auth"), []byte("client"), []byte("server"), []byte("salt"))
	f.Add([]byte{}, []byte{}, []byte{}, []byte{}, []byte{})

	f.Fuzz(func(t *testing.T, shared, authSecret, clientPub, serverPub, salt []byte) {
		cek, nonce, err := DeriveKeyNonce(shared, authSecret, clientPub, serverPub, salt)
		if err != nil {
			return
		}
		if len(cek) != 16 {
			t.Fatalf("len(cek) = %d, want 16", len(cek))
		}
		if len(nonce) != 12 {
			t.Fatalf("len(nonce) = %d, want 12", len(nonce))
		}
		cek2, nonce2, err := DeriveKeyNonce(shared, authSecret, clientPub, serverPub, salt)
		if err != nil {
			t.Fatalf("second DeriveKeyNonce errored: %v", err)
		}
		if !bytesEqual(cek, cek2) || !bytesEqual(nonce, nonce2) {
			t.Fatal("DeriveKeyNonce not deterministic for identical inputs")
		}
	})
}
