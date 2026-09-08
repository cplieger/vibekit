package push

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func (s *Service) keysPath() string { return filepath.Join(s.dir, "vapid-keys.json") }
func (s *Service) subsPath() string { return filepath.Join(s.dir, "push-subs.json") }

func (s *Service) loadKeys() {
	if s.adoptStoredKeys() {
		return
	}
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		slog.Error("push: generate VAPID keys", "error", err)
		s.healthy = false
		return
	}
	s.keys.PrivateKey = base64.RawURLEncoding.EncodeToString(priv.Bytes())
	s.keys.PublicKey = base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
	//nolint:gosec // VAPID private key is persisted to a 0600-perm file for cross-restart continuity; see keysPath().
	data, marshalErr := json.MarshalIndent(s.keys, "", "  ")
	if marshalErr != nil {
		slog.Error("push: marshal VAPID keys", "error", marshalErr)
		s.healthy = false
		return
	}
	if _, saveErr := atomicfile.WriteFile(context.Background(), s.keysPath(), data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); saveErr != nil {
		slog.Warn("push: persist VAPID keys failed", "error", saveErr)
	}
	ecdsaKey, decErr := s.decodeVAPIDPrivateKey()
	if decErr != nil {
		slog.Error("push: decode generated VAPID key", "error", decErr)
		s.healthy = false
		return
	}
	s.vapidPriv = ecdsaKey
	s.keysGenerated = true
	slog.Info("push: generated VAPID keys")
}

// adoptStoredKeys loads vapid-keys.json and reports whether the service now has
// its keypair settled. false means the caller must generate one.
//
// Every outcome short of a missing file is logged, because the contents of this
// file are the only explanation a reader gets for the stored subscriptions
// dying: a replacement keypair cannot sign for any of them.
func (s *Service) adoptStoredKeys() bool {
	data, err := os.ReadFile(s.keysPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Error("push: read VAPID keys", "error", err, "path", s.keysPath())
		}
		return false
	}
	if unusable := s.adoptKeyPair(data); unusable != nil {
		slog.Error("push: stored VAPID keys unusable, generating a replacement",
			"path", s.keysPath(), "error", unusable, "hint", pushResubscribeHint)
		return false
	}
	return true
}

// adoptKeyPair takes the keypair in data as the service's own, or reports why it
// cannot sign. s.vapidPriv is set only on success.
//
// A file present but unable to sign is reported as unusable, so adoptStoredKeys
// treats it exactly like a MISSING one and the caller's generate-and-persist path
// heals it. Adopting one left the service permanently unhealthy with no way back:
// a half-written {"publicKey":"…"} was accepted, every send was then dropped by
// preflightSend's health gate, and no later boot could repair it (invariant 6).
func (s *Service) adoptKeyPair(data []byte) error {
	if err := json.Unmarshal(data, &s.keys); err != nil {
		return err
	}
	switch {
	case s.keys.PublicKey == "":
		return errors.New("no public key")
	case s.keys.PrivateKey == "":
		return errors.New("no private key")
	}
	priv, err := s.decodeVAPIDPrivateKey()
	if err != nil {
		return fmt.Errorf("decode private key: %w", err)
	}
	s.vapidPriv = priv
	return nil
}

// reportOrphanedSubs states what a freshly generated keypair cost. Those
// subscriptions were signed against the key that just went away, so they answer
// 401/403 forever and nothing here can tell them from working ones — which is
// how push comes to be silently dead with no cause on the box.
//
// stored is the count loadSubs already read, so this costs no second read of
// push-subs.json. A read or parse failure reaches loadSubs' own warning instead.
func (s *Service) reportOrphanedSubs(stored int) {
	if !s.keysGenerated || stored == 0 {
		return
	}
	slog.Error("push: the new VAPID keypair orphaned every stored subscription",
		"count", stored, "hint", pushResubscribeHint)
}

// readPersistedSubs decodes push-subs.json. A missing file is the first-boot
// state, reported as no subscriptions and no error.
func (s *Service) readPersistedSubs() ([]vibekit.PushSubscription, error) {
	data, err := os.ReadFile(s.subsPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var subs []vibekit.PushSubscription
	if err := json.Unmarshal(data, &subs); err != nil {
		return nil, fmt.Errorf("parse %d bytes: %w", len(data), err)
	}
	return subs, nil
}

func (s *Service) loadSubs() {
	subs, err := s.readPersistedSubs()
	if err != nil {
		slog.Warn("push: read subs", "error", err)
		return
	}
	// Re-run the allowlist at load time so a prior looser ruleset
	// (or a manual edit of push-subs.json while the container was
	// stopped) can't resurrect an endpoint today's code would
	// reject at ingress.
	s.mu.Lock()
	for _, sub := range subs {
		if !isAllowedPushEndpoint(sub.Endpoint) {
			host := "unknown"
			if u, err := url.Parse(sub.Endpoint); err == nil && u.Host != "" {
				host = u.Host
			}
			slog.Warn("push: dropping subscription with disallowed endpoint", "host", host)
			continue
		}
		s.subs[sub.Endpoint] = sub
	}
	s.mu.Unlock()
	// Last, so the count comes off the read above rather than a second one. The
	// count is what was STORED, not what survived the allowlist: a replaced
	// keypair orphaned every one of them either way.
	s.reportOrphanedSubs(len(subs))
}

func (s *Service) saveSubsAsync(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// Snapshot current subs under mu, then send to the writer goroutine.
	s.mu.Lock()
	subs := make([]vibekit.PushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	// Fire-and-forget: send to the writer goroutine without waiting.
	done := make(chan struct{})
	select {
	case s.saveCh <- saveRequest{subs: subs, done: done}:
	case <-s.lifetime.Done():
	}
}

// saveSubs sends the current subscription snapshot to the write loop
// and blocks until the write completes. Used by pruneStale where
// durability confirmation is needed before the next push cycle.
func (s *Service) saveSubs(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	subs := make([]vibekit.PushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	done := make(chan struct{})
	select {
	case s.saveCh <- saveRequest{subs: subs, done: done}:
		// Guard the completion wait with the service LIFETIME as well as the
		// send: if Close() raced this send and writeLoop already exited,
		// nothing will ever close `done` — without this guard the goroutine (an
		// inflight.Go member) would block forever and hang inflight.Wait() at
		// shutdown. This is the second signal, and it is why the lifetime stays
		// a field rather than becoming a parameter: a caller's ctx cannot say
		// whether the write loop is still alive.
		select {
		case <-done:
		case <-s.lifetime.Done():
		}
	case <-s.lifetime.Done():
	}
}

// flushSaves blocks until any pending async save completes by sending
// a synchronous no-op through the write loop. Exported for tests that
// need to verify persistence after Subscribe/Unsubscribe.
func (s *Service) flushSaves() {
	done := make(chan struct{})
	s.mu.Lock()
	subs := make([]vibekit.PushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	select {
	case s.saveCh <- saveRequest{subs: subs, done: done}:
		// Guard the completion wait with the service LIFETIME as well as the
		// send: if Close() raced this send and writeLoop already exited,
		// nothing will ever close `done` — without this guard the goroutine (an
		// inflight.Go member) would block forever and hang inflight.Wait() at
		// shutdown. This is the second signal, and it is why the lifetime stays
		// a field rather than becoming a parameter: a caller's ctx cannot say
		// whether the write loop is still alive.
		select {
		case <-done:
		case <-s.lifetime.Done():
		}
	case <-s.lifetime.Done():
	}
}

// writeSubsSnapshot marshals and persists a subscription snapshot to disk.
func (s *Service) writeSubsSnapshot(subs []vibekit.PushSubscription) {
	data, err := json.MarshalIndent(subs, "", "  ")
	if err != nil {
		slog.Error("push: marshal subscriptions", "error", err)
		return
	}
	if _, saveErr := atomicfile.WriteFile(context.Background(), s.subsPath(), data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); saveErr != nil {
		slog.Warn("push: persist subscriptions failed", "error", saveErr)
	}
}

func (s *Service) decodeVAPIDPrivateKey() (*ecdsa.PrivateKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s.keys.PrivateKey)
	if err != nil {
		return nil, err
	}
	ecdhKey, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return nil, err
	}
	// Convert ecdh.PrivateKey to ecdsa.PrivateKey for JWT signing.
	ecdsaKey, err := ecdhToECDSA(ecdhKey)
	if err != nil {
		return nil, err
	}
	return ecdsaKey, nil
}
