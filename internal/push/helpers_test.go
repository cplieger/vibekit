package push

// Shared test infrastructure for the push package: a few small builders
// and an always-erroring RoundTripper. Log assertions ride slogx/capture
// directly (see the note above errRoundTripper).

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

const testSubject = "mailto:test@example.com"

// Log-line assertions go through slogx/capture's attr helpers (AttrValue /
// HasAttr): several push paths (Subscribe, loadSubs, Send) write only a
// local variable straight to slog with no return value or exported state,
// so the log line is the sole observable distinguishing correct from broken
// behaviour.

// errRoundTripper always fails, so push() can reach its post-encryption
// HTTP body-assembly code without any real network.
type errRoundTripper struct{}

func (errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("forced transport error")
}

// pushSubscriptionWithValidKeys builds a subscription whose P256dh +
// Auth survive push()'s decode/import steps so the HTTP request
// actually fires against the test server.
func pushSubscriptionWithValidKeys(t *testing.T, endpoint string) api.PushSubscription {
	t.Helper()
	clientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen client key: %v", err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("auth secret: %v", err)
	}
	sub := api.PushSubscription{Endpoint: endpoint}
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(clientPriv.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(authSecret)
	return sub
}
