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
	mrand "math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/vibekit/internal/vibekit"
	"golang.org/x/sync/errgroup"
)

// pushPayload is the typed wire shape for Web Push notification payloads.
//
// vibekit.PushSubject is EMBEDDED so fitToCap's size check (which marshals this
// struct) automatically charges every subject field against the cap; a
// separate subject copy could under-count the payload by exactly the amount
// that makes the vendor reject it.
type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	vibekit.PushSubject
}

// Send delivers a push notification to all subscribers, debounced per KIND AND
// SUBJECT (so one pull request settling does not suppress another's verdict).
//
// subject names what the notification is about; see vibekit.PushSubject. Pass a
// zero value for a workspace-global notification with nothing single behind it.
//
// preflightSend returns nil to mean DO NOT SEND (a gate refused), vs a non-nil
// EMPTY slice meaning every gate passed but nobody is subscribed — only the
// former is a return here; the latter still fans out to zero endpoints because
// preflightSend stamps the debounce timestamp before snapshotting subscribers.
func (s *Service) Send(ctx context.Context, title, body string, notifyType vibekit.PushKind, subject vibekit.PushSubject) {
	slog.Debug("push: send", "kind", string(notifyType))
	// Trim against the *marshaled* size, not the raw title+body length. The
	// JSON envelope (~22 bytes for {"title":...,"body":...}) plus any
	// character escaping count toward pushBodyCap, so a naive title+body
	// check leaves the encoded payload over the cap and push() rejects —
	// drops — it. fitToCap guarantees the marshaled payload fits, so an
	// oversize notification is delivered truncated instead of vanishing.
	if t, b, truncated := fitToCap(title, body, subject); truncated {
		slog.Warn("push: payload too large, truncating",
			"bytes", len(title)+len(body), "cap", pushBodyCap)
		title, body = t, b
	}
	subs := s.preflightSend(notifyType, subject)
	if subs == nil {
		return
	}
	payload, err := json.Marshal(pushPayload{Title: title, Body: body, PushSubject: subject})
	if err != nil {
		slog.Error("push: marshal payload", "error", err)
		return
	}

	// Bounded-concurrency fan-out: up to pushFanOutLimit concurrent sends.
	var (
		mu      sync.Mutex
		outcome fanOut
	)
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(pushFanOutLimit)
	for _, sub := range subs {
		g.Go(func() error {
			d := s.deliver(ctx, sub, payload, notifyType)
			mu.Lock()
			outcome.record(d, sub.Endpoint)
			mu.Unlock()
			return nil // best-effort: never fail the group
		})
	}
	if err := g.Wait(); err != nil {
		slog.Error("push: fan-out wait", "error", err)
	}
	s.pruneStale(outcome.prunable())
}

// fanOut is what one notification's deliveries came to. It exists because
// whether an AUTHORIZATION refusal may be pruned is not a property of that
// subscriber's own answer: see prunable.
//
// record is called under the fan-out's mutex; prunable only after it joins.
type fanOut struct {
	gone         []string // 404/410: over, whatever this server's keys are
	authRejected []string // 401/403: either their key is stale or ours is wrong
	delivered    int
}

func (f *fanOut) record(d disposition, endpoint string) {
	switch d {
	case dispDelivered:
		f.delivered++
	case dispPrune:
		f.gone = append(f.gone, endpoint)
	case dispAuthReject:
		f.authRejected = append(f.authRejected, endpoint)
	case dispPermanent, dispRetry:
		// Keep. A malformed request, an oversize record and a service outage are
		// all reasons to fix something here, never to forget a subscriber.
	}
}

// prunable is the endpoint set this fan-out earned the right to delete, and it
// logs the verdict because only a human re-subscribing undoes a prune.
//
// A 404/410 is the service saying the subscription is gone, so it goes on its
// own answer. A 401/403 equally means "your keypair or clock is wrong", so it
// goes only when another subscriber accepted this same notification: that
// delivery is the proof this server's credentials work, and a server-side
// mistake refuses everyone at once, so nothing is deleted.
func (f *fanOut) prunable() []string {
	switch {
	case len(f.authRejected) == 0:
		return f.gone
	case f.delivered == 0:
		slog.Error("push: every subscriber refused the VAPID authorization",
			"refused", len(f.authRejected), "hint", pushResubscribeHint)
		return f.gone
	default:
		slog.Warn("push: pruning subscriptions this server has no key for",
			"pruned", len(f.authRejected), "delivered", f.delivered,
			"hint", pushResubscribeHint)
		return append(f.gone, f.authRejected...)
	}
}

// pushResubscribeHint is the remedy for a subscription no key on this server can
// sign for. RFC 8292 section 4.2 requires the USER AGENT to create the
// replacement, so nothing server-side repairs one.
const pushResubscribeHint = "re-subscribe from a browser tab; a subscription bound to a replaced VAPID key cannot be repaired server-side"

// disposition is what to do about a push service's answer.
type disposition int

const (
	dispDelivered  disposition = iota // 2xx: the service accepted it
	dispPrune                         // the subscription is gone; forget it
	dispAuthReject                    // VAPID refused; whose key is wrong is not stated
	dispPermanent                     // retrying cannot help; this one is ours to fix
	dispRetry                         // transient; try again
)

// classify maps a push service's HTTP status onto a disposition.
//
//	2xx          accepted
//	400 413      malformed request, or a payload over the vendor's record limit
//	401 403      VAPID authorization refused (RFC 8292 section 4.2)
//	404 410      subscription expired or unsubscribed (RFC 8030 section 7.3)
//	429 5xx      rate limited (honour Retry-After) or a service outage
func classify(code int) disposition {
	switch {
	case code >= 200 && code < 300:
		return dispDelivered
	case code == http.StatusNotFound, code == http.StatusGone:
		return dispPrune
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return dispAuthReject
	case code == http.StatusTooManyRequests:
		return dispRetry
	case code >= 500:
		return dispRetry
	default:
		return dispPermanent
	}
}

// deliver sends one payload to one subscriber, retrying the retryable, and
// reports the disposition it ended on (a give-up reports dispRetry: keep).
//
// No queue and no dead-letter store, deliberately: nothing here is durable
// work — an unanswered permission is replayed on reconnect by the runtime's
// pending-permission tracker, and a finished turn is already in the transcript.
// An undelivered notification costs a nudge, not state, so the retry budget is
// wall-time (how long the notification stays meaningful) rather than an
// attempt count.
func (s *Service) deliver(
	ctx context.Context,
	sub vibekit.PushSubscription,
	payload []byte,
	kind vibekit.PushKind,
) disposition {
	ep := runesafe.SanitizeSingleLineBounded(sub.Endpoint, 60)
	deadline := time.Now().Add(pushRetryBudget)
	backoff := pushRetryBase

	for attempt := 1; ; attempt++ {
		code, retryAfter, err := s.push(ctx, sub, payload, kind)
		if err != nil {
			// A transport failure has no status to classify. Treat it as
			// transient (a dropped connection is the commonest cause) and let
			// the budget decide whether it is worth another try.
			slog.Warn("push: send failed", "endpoint", ep, "code", code,
				"attempt", attempt, "error", err)
			if !s.waitRetry(ctx, attempt, deadline, 0, &backoff, ep) {
				return dispRetry
			}
			continue
		}

		switch classify(code) {
		case dispDelivered:
			if attempt > 1 {
				slog.Info("push: delivered after retry", "endpoint", ep,
					"code", code, "attempts", attempt)
			}
			return dispDelivered
		case dispPrune:
			slog.Info("push: subscription invalidated", "endpoint", ep, "code", code)
			return dispPrune
		case dispAuthReject:
			slog.Warn("push: subscription refused as unauthorized", "endpoint", ep,
				"code", code)
			return dispAuthReject
		case dispPermanent:
			slog.Error("push: permanent delivery failure", "endpoint", ep,
				"code", code, "hint", permanentHint(code))
			return dispPermanent
		case dispRetry:
			slog.Warn("push: retryable status", "endpoint", ep, "code", code,
				"attempt", attempt, "retry_after", retryAfter)
			if !s.waitRetry(ctx, attempt, deadline, retryAfter, &backoff, ep) {
				return dispRetry
			}
		}
	}
}

// waitRetry sleeps before the next attempt and reports whether to make one.
// Refuses when the attempt cap is reached, the budget is spent, or a
// Retry-After would land past the notification's usefulness window.
//
// Backoff is exponential with FULL jitter (uniform over [0, backoff)) so a
// vendor outage recovering does not land every subscriber's retry at once.
func (s *Service) waitRetry(
	ctx context.Context,
	attempt int,
	deadline time.Time,
	retryAfter time.Duration,
	backoff *time.Duration,
	endpoint string,
) bool {
	if attempt >= pushMaxAttempts {
		slog.Warn("push: giving up, attempts exhausted",
			"endpoint", endpoint, "attempts", attempt)
		return false
	}
	//nolint:gosec // G404: retry jitter, not a secret — an attacker who could
	// predict it would learn when a push retry fires and nothing else.
	wait := time.Duration(mrand.Int64N(int64(*backoff)))
	if retryAfter > 0 {
		wait = retryAfter
	}
	*backoff *= 2
	if time.Now().Add(wait).After(deadline) {
		slog.Warn("push: giving up, retry would land past the notification's usefulness",
			"endpoint", endpoint, "wait", wait, "budget", pushRetryBudget)
		return false
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// permanentHint names what a permanent failure means, because the status alone
// does not say whose bug it is and these are all vibekit's. An authorization
// refusal is NOT here: it has its own disposition, because whose key is wrong
// decides whether the subscription may be deleted.
func permanentHint(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "malformed push request or headers"
	case http.StatusRequestEntityTooLarge:
		return "payload over the vendor record limit; pushBodyCap is too high"
	default:
		return "unexpected status, not retryable"
	}
}

// parseRetryAfter reads a Retry-After header in either legal form: a
// delay in seconds, or an HTTP-date. An absent, malformed or past value
// yields 0, which leaves the caller on its own backoff schedule.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(h); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// preflightSend evaluates every pre-send gate (healthy, preference,
// unknown-kind, per-subject debounce) under a single mu hold, records
// the new debounce timestamp, and returns the subscriber snapshot to
// POST to — or nil if the send should be dropped. Holding mu across
// the decision + stamp closes the TOCTOU between "should send" and
// "record last-push". See Send's doc comment for the nil-vs-empty contract.
func (s *Service) preflightSend(notifyType vibekit.PushKind, subject vibekit.PushSubject) []vibekit.PushSubscription {
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
	key := debounceKey(notifyType, subject)
	if last, ok := s.lastPush[key]; ok && time.Since(last) < pushDebounce {
		slog.Debug("push: debounced", "kind", string(notifyType), "subject", key.subject)
		return nil
	}
	s.pruneDebounceLocked()
	s.lastPush[key] = time.Now()
	subs := make([]vibekit.PushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	return subs
}

// pruneDebounceLocked drops entries whose window has expired. Caller holds mu.
// An expired entry cannot suppress anything, so this only bounds map growth.
func (s *Service) pruneDebounceLocked() {
	if len(s.lastPush) < debounceHighWater {
		return
	}
	for k, at := range s.lastPush {
		if time.Since(at) >= pushDebounce {
			delete(s.lastPush, k)
		}
	}
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
	s.saveSubs(s.lifetime)
}

// RFC 8030 section 5.3 urgencies. Only these two are used: nothing vibekit
// sends is an advertisement or a topic update, which are what the other two
// rungs describe.
const (
	urgencyNormal = "normal"
	urgencyHigh   = "high"
)

// urgencyFor maps a notification kind onto its RFC 8030 section 5.3 urgency, so
// a push service holding messages back on a low-battery device holds back the
// right ones. A permission ask blocks the turn until it is answered, which is
// the spec's "time-sensitive alert" row; everything else is a "chat or calendar
// message". normal is also the spec's default, and is sent anyway so every
// request states its own urgency.
func urgencyFor(kind vibekit.PushKind) string {
	if kind == vibekit.PushKindPermission {
		return urgencyHigh
	}
	return urgencyNormal
}

// RFC 8030 section 5.2 TTLs: how long a push service may hold a notification for
// a device that is OFFLINE. That is a different question from pushRetryBudget,
// which bounds how long THIS server keeps retrying a failing service — do not
// derive one from the other.
//
// Each value is how long the notification is still worth showing. Past it the
// service drops the message, which is the wanted outcome: an alert about
// something that has stopped being true costs the reader more than silence.
const (
	// A permission ask blocks the turn, and the interaction dock replays every
	// unanswered one on reconnect — so opening the app shows it whatever this TTL
	// says, and all the notification itself buys is promptness. Ten minutes past
	// the ask the container may have restarted (which cancels it) or another
	// device may have answered, so a high-urgency alert landing later is noise
	// about a turn that no longer exists.
	ttlPermission = 10 * time.Minute
	// "The agent finished" is moot an hour later: by then the reader has either
	// come back to the chat or stopped waiting. Same window pushRetryBudget's own
	// doc comment reasons from.
	ttlAgentFinished = time.Hour
	// A pull request's verdict does not expire — it is still true tomorrow — so
	// this is the one kind worth delivering to a device that was away all day.
	ttlPRStatus = 24 * time.Hour
)

// ttlFor maps a notification kind onto the TTL header value it travels with.
//
// An unrecognised kind takes the longest window: Send's preflight refuses every
// invalid kind, so reaching the default means the wire grew a kind this build
// does not know, and delivering that late is a smaller harm than dropping it.
func ttlFor(kind vibekit.PushKind) string {
	switch kind {
	case vibekit.PushKindPermission:
		return ttlSeconds(ttlPermission)
	case vibekit.PushKindAgentFinished:
		return ttlSeconds(ttlAgentFinished)
	default:
		return ttlSeconds(ttlPRStatus)
	}
}

// ttlSeconds renders a TTL as the decimal seconds the header carries.
func ttlSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

// push performs RFC 8291 encryption and delivers the payload to a
// single subscriber endpoint via HTTP POST with VAPID authentication.
// It returns the status code and, for a rate-limited or unavailable
// service, the Retry-After delay it asked for (0 when absent).
func (s *Service) push(
	ctx context.Context,
	sub vibekit.PushSubscription,
	payload []byte,
	kind vibekit.PushKind,
) (int, time.Duration, error) {
	// Defense-in-depth: bound payload size before any allocation. The
	// IETF web-push spec caps record size at 4096 bytes; pushBodyCap=3000
	// is the project's pre-pad ceiling. This early check makes the
	// `make([]byte, len(payload)+1)` allocation in encryptPayload
	// provably bounded and silences CodeQL's go/allocation-size-overflow
	// rule.
	if len(payload) > pushBodyCap {
		return 0, 0, fmt.Errorf("payload too large: %d bytes (max %d)", len(payload), pushBodyCap)
	}
	body, err := encryptPayload(sub, payload)
	if err != nil {
		return 0, 0, err
	}
	vapidAuth, err := s.vapidHeader(sub.Endpoint)
	if err != nil {
		return 0, 0, err
	}

	// Derive the request context from BOTH the caller's ctx and the
	// service lifecycle, unconditionally. The previous fast path merged
	// only when s.lifetime was ALREADY canceled, so a send started while
	// healthy never observed a later Service.Close and ran until the
	// client timeout. Three small allocations per subscriber per push is
	// noise at push frequency.
	reqCtx, mergeCleanup := mergeCtx(ctx, s.lifetime)
	defer mergeCleanup()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Authorization", vapidAuth)
	req.Header.Set("TTL", ttlFor(kind))
	req.Header.Set("Urgency", urgencyFor(kind))
	req.ContentLength = int64(len(body))

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, 0, err
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
	return resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

// encryptPayload performs RFC 8291 (aes128gcm) content encryption of
// payload for the subscriber's keys and returns the wire body:
// salt(16) || rs(4) || idlen(1) || ephemeralPublicKey || ciphertext.
// The caller (push) bounds len(payload) to pushBodyCap before calling,
// so the len(payload)+1 allocation below is provably small.
func encryptPayload(sub vibekit.PushSubscription, payload []byte) ([]byte, error) {
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
	cek, nonce, err := deriveKeyNonce(keyMaterial{
		Shared:     shared,
		AuthSecret: authSecret,
		ClientPub:  clientPubBytes,
		ServerPub:  ephPriv.PublicKey().Bytes(),
		Salt:       salt,
	})
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

// pushTruncMarker marks a trimmed title or body. runesafe's Capped pair charges
// it INSIDE the byte cap, so a trimmed field's total — marker included — never
// exceeds the budget fitToCap hands it.
const pushTruncMarker = "..."

// fitToCap trims the body — then, only if an empty body still overflows, the
// title — until the marshaled pushPayload is at most pushBodyCap bytes, and
// reports whether anything was trimmed. Sizing against the marshaled form
// (JSON envelope + escaping included) is required because push() rejects
// anything over the cap outright.
//
// Trimming goes through runesafe's Capped pair so the byte cap never splits a
// multi-byte rune and the truncation marker is charged inside that cap (body
// uses the CR/LF-keeping variant since notification bodies are legitimately
// multi-line). The loop terminates because each pass strictly shrinks the
// field being trimmed to a cap below its current length.
func fitToCap(title, body string, subject vibekit.PushSubject) (fitTitle, fitBody string, truncated bool) {
	if marshaledLen(title, body, subject) <= pushBodyCap {
		return title, body, false
	}
	for marshaledLen(title, body, subject) > pushBodyCap {
		over := marshaledLen(title, body, subject) - pushBodyCap
		switch {
		case len(body) > over:
			body, _ = runesafe.SanitizeCapped(body, len(body)-over, pushTruncMarker)
		case body != "":
			body = "" // too small to absorb the overflow; drop it
		case len(title) > over:
			title, _ = runesafe.SanitizeSingleLineCapped(title, len(title)-over, pushTruncMarker)
		default:
			title = "" // pathological: cap below the JSON envelope size
		}
	}
	return title, body, true
}

// marshaledLen is the byte length of the JSON-encoded notification payload.
// Marshaling three strings cannot fail, so the error is intentionally dropped.
func marshaledLen(title, body string, subject vibekit.PushSubject) int {
	p, _ := json.Marshal(pushPayload{Title: title, Body: body, PushSubject: subject})
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
