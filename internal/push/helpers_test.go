package push

// Shared test infrastructure for the push package: a slog capture
// handler (the only observable for the host/log assertions), a few
// small builders, and an always-erroring RoundTripper.

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/slogx/capture"
	"github.com/cplieger/vibekit/internal/api"
)

const testSubject = "mailto:test@example.com"

// logRec is one captured slog record (message + flattened attrs + level).
type logRec struct {
	attrs map[string]any
	msg   string
	level slog.Level
}

// asLogRec flattens a captured slog record into the logRec shape the
// assertions read. Several push paths (Subscribe, loadSubs, Send) write only
// a local variable straight to slog with no return value or exported state,
// so the log line is the sole observable distinguishing correct from broken
// behaviour.
func asLogRec(r slog.Record) logRec {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	return logRec{attrs: attrs, msg: r.Message, level: r.Level}
}

// findLogRec returns the most recent captured record with the given message.
func findLogRec(rec *capture.Recorder, msg string) (logRec, bool) {
	records := rec.Records()
	for _, r := range slices.Backward(records) {
		if r.Message == msg {
			return asLogRec(r), true
		}
	}
	return logRec{}, false
}

// asInt64 normalises slog's numeric attr representation (int or int64).
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

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
