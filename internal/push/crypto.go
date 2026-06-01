package push

// crypto.go: thin wrappers delegating to internal/push/crypto sub-package.

import (
	"crypto/ecdh"
	"crypto/ecdsa"

	pushcrypto "vibekit/internal/push/crypto"
)

// vapidHeader constructs the VAPID Authorization header (RFC 8292) for
// the given endpoint using the service's cached VAPID key pair.
func (s *Service) vapidHeader(endpoint string) (string, error) {
	return pushcrypto.BuildVAPIDHeader(s.vapidPriv, s.keys.PublicKey, s.subject, endpoint)
}

// ecdhToECDSA converts an ECDH P-256 private key to an ECDSA private key.
func ecdhToECDSA(key *ecdh.PrivateKey) (*ecdsa.PrivateKey, error) {
	return pushcrypto.ECDHToECDSA(key)
}

// deriveKeyNonce derives the content-encryption key and nonce per RFC 8291.
func deriveKeyNonce(shared, authSecret, clientPub, serverPub, salt []byte) (cek, nonce []byte, err error) {
	return pushcrypto.DeriveKeyNonce(shared, authSecret, clientPub, serverPub, salt)
}
