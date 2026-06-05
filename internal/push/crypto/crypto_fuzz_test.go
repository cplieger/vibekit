package crypto

import (
	"crypto/ecdh"
	"crypto/elliptic"
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
	// Single seed: a randomly generated valid key.
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		f.Fatalf("seed: GenerateKey: %v", err)
	}
	f.Add(priv.Bytes())

	f.Fuzz(func(t *testing.T, dBytes []byte) {
		// Only proceed for byte strings that are valid P-256 scalars.
		// NewPrivateKey enforces this; if it errors, skip.
		key, err := ecdh.P256().NewPrivateKey(dBytes)
		if err != nil {
			t.Skip()
		}

		ec, err := ECDHToECDSA(key)
		if err != nil {
			t.Fatalf("ECDHToECDSA: %v", err)
		}

		if ec.Curve != elliptic.P256() {
			t.Fatalf("curve = %v; want P-256", ec.Curve)
		}
		if ec.X == nil || ec.Y == nil || ec.D == nil {
			t.Fatalf("nil component: X=%v Y=%v D=%v", ec.X, ec.Y, ec.D)
		}
		// Public point must be on P-256.
		if !elliptic.P256().IsOnCurve(ec.X, ec.Y) {
			t.Fatalf("point (%v, %v) not on P-256", ec.X, ec.Y)
		}

		// X/Y derived from the original ECDH public key bytes must match.
		raw := key.PublicKey().Bytes()
		if len(raw) != 65 || raw[0] != 0x04 {
			t.Fatalf("unexpected public key format: len=%d head=%x", len(raw), raw[0])
		}
		// The 32-byte X/Y are bigint-equivalent.
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
