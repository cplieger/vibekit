package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/cplieger/vibekit/internal/metrics"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cplieger/vibekit/internal/api"
)

// pushPayload is the typed wire shape for Web Push notification payloads.
// Using a struct instead of map[string]string gives compile-time key safety
// and enables json's cached struct encoder.
type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Send delivers a push notification to all subscribers.
// notifyType is KindAgentFinished or KindPermission; the service
// checks per-type preferences and debounces rapid notifications on a
// per-type basis (so a permission push doesn't suppress the agent-
// finished push that follows within 5s). Oversize payloads are
// truncated with a Warn breadcrumb so an accidentally-chatty caller
// doesn't get silently rejected by the push vendor.
func (s *Service) Send(ctx context.Context, title, body string, notifyType api.PushKind) {
	metrics.PushSends.Inc()
	if total := len(title) + len(body); total > pushBodyCap {
		slog.Warn("push: payload too large, truncating",
			"bytes", total, "cap", pushBodyCap)
		// Leave room for ellipsis + the full title.
		room := max(pushBodyCap-len(title)-3, 0)
		body = truncate(body, room)
	}
	subs := s.preflightSend(notifyType)
	if subs == nil {
		return
	}
	payload, err := json.Marshal(pushPayload{Title: title, Body: body})
	if err != nil {
		slog.Error("push: marshal payload", "error", err)
		return
	}

	// Bounded-concurrency fan-out: up to pushFanOutLimit concurrent sends.
	var (
		mu    sync.Mutex
		stale []string
	)
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(pushFanOutLimit)
	for _, sub := range subs {
		g.Go(func() error {
			code, err := s.push(ctx, sub, payload)
			if err != nil {
				slog.Warn("push: send failed",
					"endpoint", truncate(sub.Endpoint, 60),
					"code", code,
					"error", err)
				return nil // best-effort: log and continue
			}
			if code == http.StatusGone || code == http.StatusNotFound {
				slog.Info("push: subscription invalidated",
					"endpoint", truncate(sub.Endpoint, 60),
					"code", code)
				mu.Lock()
				stale = append(stale, sub.Endpoint)
				mu.Unlock()
			} else if code >= 400 {
				slog.Warn("push: unexpected status",
					"endpoint", truncate(sub.Endpoint, 60),
					"code", code)
			}
			return nil // best-effort: never fail the group
		})
	}
	if err := g.Wait(); err != nil {
		slog.Error("push: fan-out wait", "error", err)
	}
	s.pruneStale(stale)
}

// preflightSend evaluates every pre-send gate (healthy, preference,
// unknown-kind, per-type debounce) under a single mu hold, records
// the new debounce timestamp, and returns the subscriber snapshot to
// POST to — or nil if the send should be dropped. Holding mu across
// the decision + stamp closes the TOCTOU between "should send" and
// "record last-push".
func (s *Service) preflightSend(notifyType api.PushKind) []api.PushSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.healthy {
		return nil
	}
	if !notifyType.Valid() {
		slog.Warn("push: unknown notification kind", "kind", string(notifyType))
		return nil
	}
	enabled, known := s.prefs[notifyType]
	if !known {
		return nil
	}
	if !enabled {
		return nil
	}
	if last, ok := s.lastPush[notifyType]; ok && time.Since(last) < pushDebounce {
		slog.Debug("push: debounced", "kind", string(notifyType))
		return nil
	}
	s.lastPush[notifyType] = time.Now()
	subs := make([]api.PushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	return subs
}

// pruneStale deletes the listed endpoints from s.subs and persists
// the updated set. No-op on empty input so callers don't need a guard.
func (s *Service) pruneStale(stale []string) {
	if len(stale) == 0 {
		return
	}
	s.mu.Lock()
	for _, ep := range stale {
		delete(s.subs, ep)
	}
	s.mu.Unlock()
	s.saveSubs(s.ctx)
}

// push performs RFC 8291 encryption and delivers the payload to a
// single subscriber endpoint via HTTP POST with VAPID authentication.
func (s *Service) push(ctx context.Context, sub api.PushSubscription, payload []byte) (int, error) {
	// Defense-in-depth: bound payload size before any allocation. The
	// IETF web-push spec caps record size at 4096 bytes; pushBodyCap=3000
	// is the project's pre-pad ceiling. This early check makes the
	// `make([]byte, 2+len(payload))` allocation below provably bounded
	// and silences CodeQL's go/allocation-size-overflow rule.
	if len(payload) > pushBodyCap {
		return 0, fmt.Errorf("payload too large: %d bytes (max %d)", len(payload), pushBodyCap)
	}
	clientPubBytes, err := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if err != nil {
		return 0, fmt.Errorf("decode p256dh: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil {
		return 0, fmt.Errorf("decode auth: %w", err)
	}
	clientPub, err := ecdh.P256().NewPublicKey(clientPubBytes)
	if err != nil {
		return 0, fmt.Errorf("import client key: %w", err)
	}
	ephPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return 0, err
	}
	shared, err := ephPriv.ECDH(clientPub)
	if err != nil {
		return 0, err
	}

	salt := make([]byte, 16)
	if _, saltErr := rand.Read(salt); saltErr != nil {
		return 0, saltErr
	}
	cek, nonce, err := deriveKeyNonce(shared, authSecret, clientPubBytes, ephPriv.PublicKey().Bytes(), salt)
	if err != nil {
		return 0, err
	}

	padded := make([]byte, 2+len(payload))
	copy(padded[2:], payload)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return 0, err
	}
	ciphertext := gcm.Seal(nil, nonce, padded, nil)

	ephPubBytes := ephPriv.PublicKey().Bytes()
	body := make([]byte, 0, 16+4+1+len(ephPubBytes)+len(ciphertext))
	body = append(body, salt...)
	body = binary.BigEndian.AppendUint32(body, 4096)
	body = append(body, byte(len(ephPubBytes))) //nolint:gosec // G115: value bounded by protocol
	body = append(body, ephPubBytes...)
	body = append(body, ciphertext...)

	vapidAuth, err := s.vapidHeader(sub.Endpoint)
	if err != nil {
		return 0, err
	}

	// Fast path: when the service context is still active (common case),
	// use the caller's ctx directly — avoids 3 allocations (context +
	// cancel + AfterFunc) per subscriber per push.
	reqCtx := ctx
	var mergeCleanup func()
	if s.ctx.Err() != nil {
		reqCtx, mergeCleanup = mergeCtx(ctx, s.ctx)
	}
	if mergeCleanup != nil {
		defer mergeCleanup()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Authorization", vapidAuth)
	req.Header.Set("TTL", "86400")
	req.ContentLength = int64(len(body))

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	// Drain + close so HTTP/1.1 keep-alive can reuse the connection
	// for the next push to the same vendor host. Cap via LimitReader
	// (vendor bodies are tiny; 64 KiB is ample for any legitimate
	// response) and ignore errors — a failed drain closes the
	// response anyway and the next push opens a fresh connection.
	if _, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, pushResponseCap)); copyErr != nil {
		// Already at debug level: drain failures are expected when
		// the push service closes the connection immediately.
		slog.Debug("push: drain response body", "error", copyErr)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// mergeCtx returns a context derived from primary that is also
// cancelled when secondary is done. This lets the push HTTP request
// respect both the caller's per-send cancellation and the service's
// lifecycle shutdown signal.
func mergeCtx(primary, secondary context.Context) (ctx context.Context, cleanup func()) {
	ctx, cancel := context.WithCancel(primary)
	stop := context.AfterFunc(secondary, func() { cancel() })
	cleanup = func() {
		stop()
		cancel()
	}
	return ctx, cleanup
}
