package push

// Shared test infrastructure for the push package: a slog capture
// handler (the only observable for the host/log assertions), a few
// small builders, and an always-erroring RoundTripper.

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

const testSubject = "mailto:test@example.com"

// logRec is one captured slog record (message + flattened attrs + level).
type logRec struct {
	attrs map[string]any
	msg   string
	level slog.Level
}

// logCapture is a slog.Handler that records every log line so a test
// can assert on the message and attributes a code path emits. Several
// push paths (Subscribe, loadSubs, Send) write only a local variable
// straight to slog with no return value or exported state, so the log
// line is the sole observable distinguishing correct from broken
// behaviour.
type logCapture struct {
	recs []logRec
	mu   sync.Mutex
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	c.mu.Lock()
	c.recs = append(c.recs, logRec{attrs: attrs, msg: r.Message, level: r.Level})
	c.mu.Unlock()
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// has reports whether any captured record carries the given message.
func (c *logCapture) has(msg string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.recs {
		if r.msg == msg {
			return true
		}
	}
	return false
}

// find returns the most recent captured record with the given message.
func (c *logCapture) find(msg string) (logRec, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range slices.Backward(c.recs) {
		if r.msg == msg {
			return r, true
		}
	}
	return logRec{}, false
}

// installLogCapture swaps the global slog default for a capturing
// handler (all levels) and restores it via t.Cleanup. Callers must NOT
// use t.Parallel — the slog default is process-global.
func installLogCapture(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
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
