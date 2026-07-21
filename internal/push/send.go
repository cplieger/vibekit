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
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/runesafe"
	"github.com/cplieger/vibekit/internal/api"
	"golang.org/x/sync/errgroup"
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
	slog.Debug("push: send", "kind", string(notifyType))
	// Trim against the *marshaled* size, not the raw title+body length. The
	// JSON envelope (~22 bytes for {"title":...,"body":...}) plus any
	// character escaping count toward pushBodyCap, so a naive title+body
	// check leaves the encoded payload over the cap and push() rejects —
	// drops — it. fitToCap guarantees the marshaled payload fits, so an
	// oversize notification is delivered truncated instead of vanishing.
	if t, b, truncated := fitToCap(title, body); truncated {
		slog.Warn("push: payload too large, truncating",
			"bytes", len(title)+len(body), "cap", pushBodyCap)
		title, body = t, b
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
					"endpoint", runesafe.SanitizeSingleLineBounded(sub.Endpoint, 60),
					"code", code,
					"error", err)
				return nil // best-effort: log and continue
			}
			if code == http.StatusGone || code == http.StatusNotFound {
				slog.Info("push: subscription invalidated",
					"endpoint", runesafe.SanitizeSingleLineBounded(sub.Endpoint, 60),
					"code", code)
				mu.Lock()
				stale = append(stale, sub.Endpoint)
				mu.Unlock()
			} else if code >= 400 {
				slog.Warn("push: unexpected status",
					"endpoint", runesafe.SanitizeSingleLineBounded(sub.Endpoint, 60),
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
	// `make([]byte, len(payload)+1)` allocation in encryptPayload
	// provably bounded and silences CodeQL's go/allocation-size-overflow
	// rule.
	if len(payload) > pushBodyCap {
		return 0, fmt.Errorf("payload too large: %d bytes (max %d)", len(payload), pushBodyCap)
	}
	body, err := encryptPayload(sub, payload)
	if err != nil {
		return 0, err
	}
	vapidAuth, err := s.vapidHeader(sub.Endpoint)
	if err != nil {
		return 0, err
	}

	// Derive the request context from BOTH the caller's ctx and the
	// service lifecycle, unconditionally. The previous fast path merged
	// only when s.ctx was ALREADY canceled, so a send started while
	// healthy never observed a later Service.Close and ran until the
	// client timeout. Three small allocations per subscriber per push is
	// noise at push frequency.
	reqCtx, mergeCleanup := mergeCtx(ctx, s.ctx)
	defer mergeCleanup()

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

// encryptPayload performs RFC 8291 (aes128gcm) content encryption of
// payload for the subscriber's keys and returns the wire body:
// salt(16) || rs(4) || idlen(1) || ephemeralPublicKey || ciphertext.
// The caller (push) bounds len(payload) to pushBodyCap before calling,
// so the len(payload)+1 allocation below is provably small.
func encryptPayload(sub api.PushSubscription, payload []byte) ([]byte, error) {
	clientPubBytes, err := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if err != nil {
		return nil, fmt.Errorf("decode p256dh: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil {
		return nil, fmt.Errorf("decode auth: %w", err)
	}
	clientPub, err := ecdh.P256().NewPublicKey(clientPubBytes)
	if err != nil {
		return nil, fmt.Errorf("import client key: %w", err)
	}
	ephPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := ephPriv.ECDH(clientPub)
	if err != nil {
		return nil, err
	}

	salt := make([]byte, 16)
	if _, saltErr := rand.Read(salt); saltErr != nil {
		return nil, saltErr
	}
	cek, nonce, err := deriveKeyNonce(shared, authSecret, clientPubBytes, ephPriv.PublicKey().Bytes(), salt)
	if err != nil {
		return nil, err
	}

	// RFC 8188 §2.1 single-record plaintext: payload followed by the 0x02
	// padding delimiter (0x02 = last record, no additional padding). This is
	// NOT a 2-byte zero prefix — that was the obsolete "aesgcm" draft scheme;
	// a conformant browser DISCARDS an aes128gcm record whose delimiter octet
	// isn't 0x02, so payloaded pushes silently failed before this fix.
	padded := make([]byte, len(payload)+1)
	copy(padded, payload)
	padded[len(payload)] = 0x02

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, padded, nil)

	ephPubBytes := ephPriv.PublicKey().Bytes()
	body := make([]byte, 0, 16+4+1+len(ephPubBytes)+len(ciphertext))
	body = append(body, salt...)
	body = binary.BigEndian.AppendUint32(body, 4096)
	body = append(body, byte(len(ephPubBytes))) //nolint:gosec // G115: value bounded by protocol
	body = append(body, ephPubBytes...)
	body = append(body, ciphertext...)
	return body, nil
}

// capMultiline sanitizes s for emission while keeping CR/LF (notification
// bodies are legitimately multi-line) and caps it at n bytes on a rune
// boundary, appending "..." when it shortened — the multi-line sibling of
// runesafe.SanitizeSingleLineBounded.
func capMultiline(s string, n int) string {
	s = runesafe.Sanitize(s)
	if len(s) <= n {
		return s
	}
	return runesafe.CapBytes(s, n) + "..."
}

// fitToCap trims the body — then, only if an empty body still overflows, the
// title — until the marshaled pushPayload is at most pushBodyCap bytes, and
// reports whether anything was trimmed. Sizing against the marshaled form
// (JSON envelope + escaping included) is what keeps the encoded payload under
// the vendor's record limit; push() rejects anything larger, so without this
// an oversize notification is dropped rather than delivered truncated.
// Trimming goes through runesafe so the byte cap never splits a multi-byte
// rune: the title through the single-line preset, the body through the
// CR/LF-keeping capMultiline composition. The loop terminates: each
// iteration strictly shortens the field being trimmed (a sanitized-then-
// capped field is at most len(field)-over bytes, and a within-cap sanitized
// field is at most len(field)-over-3), and an empty title+body marshals
// well under the cap.
func fitToCap(title, body string) (fitTitle, fitBody string, truncated bool) {
	if marshaledLen(title, body) <= pushBodyCap {
		return title, body, false
	}
	for marshaledLen(title, body) > pushBodyCap {
		over := marshaledLen(title, body) - pushBodyCap
		switch {
		case len(body) > over+len("..."):
			body = capMultiline(body, len(body)-over-len("..."))
		case body != "":
			body = "" // too small to absorb the overflow; drop it
		case len(title) > over+len("..."):
			title = runesafe.SanitizeSingleLineBounded(title, len(title)-over-len("..."))
		default:
			title = "" // pathological: cap below the JSON envelope size
		}
	}
	return title, body, true
}

// marshaledLen is the byte length of the JSON-encoded notification payload.
// Marshaling two strings cannot fail, so the error is intentionally dropped.
func marshaledLen(title, body string) int {
	p, _ := json.Marshal(pushPayload{Title: title, Body: body})
	return len(p)
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
