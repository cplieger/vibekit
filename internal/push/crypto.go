// Package push manages Web Push subscriptions and delivers VAPID-signed
// notifications.
package push

// crypto.go: RFC 8291 content encryption and RFC 8292 VAPID JWT construction.
//
// These were an internal/push/crypto SUB-PACKAGE until the go-rulebook pass,
// reached through three forwarding wrappers in this file. It shadowed stdlib
// `crypto`, so its one importer had to alias it `pushcrypto` beside
// `crypto/ecdh` and `crypto/ecdsa`, and a directory named `crypto` sat next to
// a file named `crypto.go` in the same package. Two of the three wrappers
// forwarded their arguments unchanged, so the indirection bought nothing that
// the package boundary did not already give. Rolled up: the shadow is gone, the
// alias is gone, and the pure helpers are unexported because nothing outside
// this package ever called them.

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"time"
)

// vapidExpWindow is the JWT expiry window in seconds (12 hours).
const vapidExpWindow = 12 * 60 * 60

// vapidHeader constructs the VAPID Authorization header (RFC 8292) for
// the given endpoint using the service's cached VAPID key pair.
func (s *Service) vapidHeader(endpoint string) (string, error) {
	return buildVAPIDHeader(s.vapidPriv, s.keys.PublicKey, s.subject, endpoint)
}

// ecdhToECDSA converts an ECDH P-256 private key to an ECDSA private key, so a
// key generated for RFC 8291 encryption can also sign the RFC 8292 VAPID JWT.
//
// ecdh.PrivateKey.Bytes returns the raw big-endian scalar, which is exactly
// ParseRawPrivateKey's input format; the parser recomputes the public point
// itself. This used to assemble an ecdsa.PrivateKey by hand out of big.Ints
// sliced from the uncompressed public point — the ecdsa X/Y/D fields that Go
// 1.26 deprecated, because assigning them can build an invalid key and
// big.Int's methods are not constant-time on secret values. The parser also
// REJECTS a scalar of zero or one not reduced modulo the curve order, which the
// hand-rolled version accepted silently.
func ecdhToECDSA(key *ecdh.PrivateKey) (*ecdsa.PrivateKey, error) {
	return ecdsa.ParseRawPrivateKey(elliptic.P256(), key.Bytes())
}

// keyMaterial carries the five byte slices RFC 8291's key derivation consumes.
//
// A struct rather than five positional []byte parameters, and this is the one
// signature in the fleet where that distinction is load-bearing rather than
// stylistic. Every input is a []byte, so the compiler accepts any permutation;
// HKDF has no notion of which slice was meant to be which, so a transposition
// does not fail — it derives a DIFFERENT key, successfully, and returns no
// error. The push payload is then encrypted with a key the subscriber's browser
// cannot reconstruct, so it is discarded by the user agent with nothing logged
// on either side. There is no assertion that could catch it and no test short
// of a live round trip that would notice, which is why the field names are the
// guard.
type keyMaterial struct {
	// Shared is the ECDH shared secret between our ephemeral key and the
	// subscription's public key.
	Shared []byte
	// AuthSecret is the subscription's auth secret (the `auth` key).
	AuthSecret []byte
	// ClientPub is the subscription's public key (the `p256dh` key), and it
	// must precede ServerPub in the auth info string.
	ClientPub []byte
	// ServerPub is our ephemeral public key.
	ServerPub []byte
	// Salt is the per-message random salt, also sent in the payload header.
	Salt []byte
}

// deriveKeyNonce derives the content-encryption key and nonce per RFC 8291.
//
// km is taken by value. The 120 bytes gocritic counts are five slice HEADERS,
// not the key material they point at, so the copy is a handful of words once per
// notification and moves no secret. By value is also the safer shape: the callee
// has no business mutating the caller's key material, and a pointer would add a
// nil case to a function whose failure mode is a silently undeliverable payload.
//
//nolint:gocritic // hugeParam: five slice headers, not the keys; see above.
func deriveKeyNonce(km keyMaterial) (cek, nonce []byte, err error) {
	// Order is the RFC's: the auth info string is the literal, then the CLIENT
	// key, then the SERVER key. Swapping the two produces a valid-looking key
	// the subscriber cannot derive.
	authInfo := string(append(append([]byte("WebPush: info\x00"), km.ClientPub...), km.ServerPub...))
	authPRK, err := hkdf.Extract(sha256.New, km.Shared, km.AuthSecret)
	if err != nil {
		return nil, nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, authPRK, authInfo, 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, km.Salt)
	if err != nil {
		return nil, nil, err
	}
	cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, nil, err
	}
	nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, nil, err
	}
	return cek, nonce, nil
}

// buildVAPIDHeader constructs a VAPID Authorization header (RFC 8292)
// from a pre-decoded ECDSA private key.
func buildVAPIDHeader(priv *ecdsa.PrivateKey, pubKeyB64, subject, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims := struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}{
		Aud: u.Scheme + "://" + u.Host,
		Sub: subject,
		Exp: now + vapidExpWindow,
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := hdr + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)

	hash := sha256.Sum256([]byte(unsigned))
	r, ss, err := ecdsa.Sign(rand.Reader, priv, hash[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	rBytes, sBytes := r.Bytes(), ss.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)

	return "vapid t=" + unsigned + "." + base64.RawURLEncoding.EncodeToString(sig) + ", k=" + pubKeyB64, nil
}
