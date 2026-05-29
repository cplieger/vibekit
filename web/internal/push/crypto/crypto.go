// Package crypto provides RFC 8291 encryption helpers and VAPID JWT
// construction for Web Push. These are pure cryptographic functions
// with no dependency on the push Service's state.
package crypto

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"time"
)

// VAPIDExpWindow is the JWT expiry window in seconds (12 hours).
const VAPIDExpWindow = 12 * 60 * 60

// ECDHToECDSA converts an ECDH P-256 private key to an ECDSA private key.
func ECDHToECDSA(key *ecdh.PrivateKey) (*ecdsa.PrivateKey, error) {
	rawPub := key.PublicKey().Bytes()
	// Uncompressed P-256 point: 0x04 || X(32) || Y(32)
	if len(rawPub) != 65 || rawPub[0] != 0x04 {
		return nil, errors.New("unexpected public key format")
	}
	x := new(big.Int).SetBytes(rawPub[1:33])
	y := new(big.Int).SetBytes(rawPub[33:65])
	d := new(big.Int).SetBytes(key.Bytes())
	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y},
		D:         d,
	}, nil
}

// DeriveKeyNonce derives the content-encryption key and nonce per RFC 8291.
func DeriveKeyNonce(shared, authSecret, clientPub, serverPub, salt []byte) (cek, nonce []byte, err error) {
	authInfo := string(append(append([]byte("WebPush: info\x00"), clientPub...), serverPub...))
	authPRK, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		return nil, nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, authPRK, authInfo, 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
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

// BuildVAPIDHeader constructs a VAPID Authorization header (RFC 8292)
// from a pre-decoded ECDSA private key.
func BuildVAPIDHeader(priv *ecdsa.PrivateKey, pubKeyB64, subject, endpoint string) (string, error) {
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
		Exp: now + VAPIDExpWindow,
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
