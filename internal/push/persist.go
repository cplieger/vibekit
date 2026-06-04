package push

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"vibekit/internal/api"
	"github.com/cplieger/atomicfile"
)

func (s *Service) keysPath() string { return filepath.Join(s.dir, "vapid-keys.json") }
func (s *Service) subsPath() string { return filepath.Join(s.dir, "push-subs.json") }

func (s *Service) loadKeys() {
	data, err := os.ReadFile(s.keysPath())
	if err == nil && json.Unmarshal(data, &s.keys) == nil && s.keys.PublicKey != "" {
		priv, decErr := s.decodeVAPIDPrivateKey()
		if decErr != nil {
			slog.Error("push: decode VAPID private key at startup", "error", decErr)
			s.healthy = false
			return
		}
		s.vapidPriv = priv
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
	if saveErr := atomicfile.SaveBytes(s.keysPath(), data, 0o600); saveErr != nil {
		slog.Warn("push: persist VAPID keys failed", "error", saveErr)
	}
	ecdsaKey, decErr := s.decodeVAPIDPrivateKey()
	if decErr != nil {
		slog.Error("push: decode generated VAPID key", "error", decErr)
		s.healthy = false
		return
	}
	s.vapidPriv = ecdsaKey
	slog.Info("push: generated VAPID keys")
}

func (s *Service) loadSubs() {
	data, err := os.ReadFile(s.subsPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("push: read subs", "error", err)
		}
		return
	}
	var subs []api.PushSubscription
	if err := json.Unmarshal(data, &subs); err != nil {
		slog.Warn("push: parse subs", "error", err, "size", len(data))
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
}

func (s *Service) saveSubsAsync(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// Snapshot current subs under mu, then send to the writer goroutine.
	s.mu.Lock()
	subs := make([]api.PushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	// Fire-and-forget: send to the writer goroutine without waiting.
	done := make(chan struct{})
	select {
	case s.saveCh <- saveRequest{subs: subs, done: done}:
	case <-s.ctx.Done():
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
	subs := make([]api.PushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	done := make(chan struct{})
	select {
	case s.saveCh <- saveRequest{subs: subs, done: done}:
		<-done
	case <-s.ctx.Done():
	}
}

// flushSaves blocks until any pending async save completes by sending
// a synchronous no-op through the write loop. Exported for tests that
// need to verify persistence after Subscribe/Unsubscribe.
func (s *Service) flushSaves() {
	done := make(chan struct{})
	s.mu.Lock()
	subs := make([]api.PushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	select {
	case s.saveCh <- saveRequest{subs: subs, done: done}:
		<-done
	case <-s.ctx.Done():
	}
}

// writeSubsSnapshot marshals and persists a subscription snapshot to disk.
func (s *Service) writeSubsSnapshot(subs []api.PushSubscription) {
	data, err := json.MarshalIndent(subs, "", "  ")
	if err != nil {
		slog.Error("push: marshal subscriptions", "error", err)
		return
	}
	if saveErr := atomicfile.SaveBytes(s.subsPath(), data, 0o600); saveErr != nil {
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
