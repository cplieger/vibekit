package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/cplieger/ssrf/v3"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/settings"
	"golang.org/x/sync/singleflight"
)

// Compile-time interface assertion.
var _ api.PushService = (*Service)(nil)

// DefaultTitle is the notification title used for all Web Push messages.
const DefaultTitle = "Vibekit"

// pushDebounce is the per-kind quiet window; pushResponseCap bounds
// body drain for keep-alive-friendly reads; pushBodyCap caps the
// combined title+body payload so an accidental megabyte send doesn't
// get silently rejected by the push vendor.
const (
	pushDebounce    = 5 * time.Second
	pushResponseCap = 64 << 10 // 64 KiB — vendors return tiny bodies
	pushBodyCap     = 3000     // title+body ceiling; pre-pad room under 4096 record
	pushFanOutLimit = 3        // max concurrent push sends per notification
)

type vapidKeys struct {
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
}

// Service manages push subscriptions and sends notifications.
type Service struct {
	ctx           context.Context
	prefsFlight   singleflight.Group
	saveCh        chan saveRequest
	writeLoopDone chan struct{}
	cancel        context.CancelFunc
	client        *http.Client
	lastPush      map[api.PushKind]time.Time
	subs          map[string]api.PushSubscription
	prefs         map[api.PushKind]bool
	keys          vapidKeys
	vapidPriv     *ecdsa.PrivateKey
	subject       string
	dir           string
	mu            sync.Mutex
	healthy       bool
}

// saveRequest pairs a subscription snapshot with a done channel
// so the caller can wait for the write to complete.
type saveRequest struct {
	done chan struct{}
	subs []api.PushSubscription
}

// New creates a Service, loads persisted subscriptions and preferences, and starts the write loop.
// subject is the VAPID subject (mailto: or https: URI identifying the sender).
func New(ctx context.Context, configDir, subject string) *Service {
	ctx, cancel := context.WithCancel(ctx)
	prefs := make(map[api.PushKind]bool, len(kindRegistry))
	for _, kr := range kindRegistry {
		prefs[kr.Kind] = kr.DefaultOn
	}
	s := &Service{
		subs:          make(map[string]api.PushSubscription),
		lastPush:      make(map[api.PushKind]time.Time),
		subject:       subject,
		dir:           configDir,
		ctx:           ctx,
		cancel:        cancel,
		prefs:         prefs,
		saveCh:        make(chan saveRequest, 1),
		writeLoopDone: make(chan struct{}),
		healthy:       true,
	}
	// pushClient reuses one connection pool across sends. Small
	// idle pool — only 3 vendor hosts in practice. CheckRedirect
	// re-validates every hop against the allowlist so an
	// allowlisted push service can't redirect us to an
	// unallowlisted host (defence-in-depth; the vendors don't
	// redirect externally today). TLS MinVersion pinned to 1.2 so
	// a future stdlib default change can't silently weaken the
	// transport that carries VAPID-signed tokens.
	//
	// The transport is ssrf.SafeTransport: isAllowedPushEndpoint
	// remains the PRIMARY gate (name-based FCM/Mozilla/Apple/WNS
	// allowlist, https-only, no explicit ports), and SafeTransport
	// sits beneath it as an IP-layer backstop — it resolves DNS once
	// and validates every resolved IP, plus a net.Dialer Control hook
	// re-validates the actually-connected IP at socket creation. That
	// closes the residual DNS-rebinding / suffix-subdomain-to-internal
	// vectors a purely name-based check leaves open. Ports are pinned to
	// 443 — the dialer enforces this, and the allowlist already rejects
	// explicit ports so every legitimate vendor endpoint is default-443.
	// https is enforced by the allowlist and the redirect check, NOT the
	// transport (SafeTransport's dialer never sees the URL scheme), so no
	// scheme option is passed. SafeTransport carries
	// no TLSClientConfig or idle-pool size via options, so those two
	// original settings are applied to the returned *http.Transport.
	pushTransport := ssrf.SafeTransport(
		ssrf.WithAllowedPorts(443),
	)
	pushTransport.MaxIdleConnsPerHost = 2
	pushTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	s.client = &http.Client{
		Timeout:   10 * time.Second,
		Transport: pushTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("push: too many redirects")
			}
			if !isAllowedPushEndpoint(req.URL.String()) {
				return errors.New("push: redirect to non-allowed host")
			}
			return nil
		},
	}
	s.loadKeys()
	s.loadSubs()
	s.loadPreferences(s.ctx)
	go s.writeLoop()
	s.mu.Lock()
	n := len(s.subs)
	s.mu.Unlock()
	slog.Info("push: ready", "subscribers", n, "healthy", s.healthy)
	return s
}

// Close cancels any in-flight pushes and waits for the write loop to
// drain pending saves. Call from the hub's shutdown path so pending
// sends don't hold the shutdown up to 10s each.
func (s *Service) Close() {
	s.cancel()
	<-s.writeLoopDone
}

// PublicKey returns the VAPID public key used for push subscription registration.
func (s *Service) PublicKey() string { return s.keys.PublicKey }

// SetPreferences updates the per-kind notification enabled flags.
func (s *Service) SetPreferences(prefs map[api.PushKind]bool) {
	s.mu.Lock()
	maps.Copy(s.prefs, prefs)
	s.mu.Unlock()
}

// Subscribe registers a push subscription endpoint. Duplicate endpoints are silently overwritten.
func (s *Service) Subscribe(sub api.PushSubscription) {
	s.mu.Lock()
	s.subs[sub.Endpoint] = sub
	s.mu.Unlock()
	s.saveSubsAsync(s.ctx)
	// Log only the host so the per-subscriber token in the URL
	// path doesn't leak into Loki. Host alone is enough to
	// distinguish Chrome/Firefox/Safari subscribers for debugging.
	host := "unknown"
	if u, err := url.Parse(sub.Endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	slog.Info("push: subscribed", "host", host)
}

// Unsubscribe removes the subscription for the given push endpoint.
func (s *Service) Unsubscribe(endpoint string) {
	s.mu.Lock()
	delete(s.subs, endpoint)
	s.mu.Unlock()
	s.saveSubsAsync(s.ctx)
}

// HasSubscribers reports whether any push subscriptions are currently registered.
func (s *Service) HasSubscribers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0
}

// kindRegistry is the single source of truth for push notification kinds.
// Adding a new kind requires only a new entry here — the default, settings
// key, and kind constant are co-located. The init() below validates that
// every registered kind passes api.PushKind.Valid(), ensuring the registry
// and the api-level Valid() switch cannot drift.
var kindRegistry = []struct {
	Kind        api.PushKind
	SettingsKey string
	DefaultOn   bool
}{
	{api.PushKindAgentFinished, settings.KeyNotifyAgentFinished, true},
	{api.PushKindPermission, settings.KeyNotifyPermission, true},
}

func init() {
	for _, kr := range kindRegistry {
		if !kr.Kind.Valid() {
			panic("push: kindRegistry contains invalid PushKind: " + string(kr.Kind))
		}
	}
}

// ReloadPreferences deduplicates concurrent preference reloads via
// singleflight so N simultaneous SSE reconnects produce only one
// disk read.
func (s *Service) ReloadPreferences(ctx context.Context) {
	// loadPreferences handles its own errors internally (logs and
	// swallows), so the closure returns (nil, nil) unconditionally.
	// We still surface any unexpected singleflight error at Debug so
	// future closure changes that add a real error path aren't lost.
	if _, err, _ := s.prefsFlight.Do("prefs", func() (any, error) {
		s.loadPreferences(ctx)
		return nil, nil
	}); err != nil {
		slog.Debug("push: reload preferences singleflight returned error", "error", err)
	}
}

// writeLoop drains saveCh and writes the latest snapshot to disk.
// Serialises all disk writes through a single goroutine, eliminating
// the need for writeMu.
func (s *Service) writeLoop() {
	defer close(s.writeLoopDone)
	for {
		select {
		case req, ok := <-s.saveCh:
			if !ok {
				return
			}
			s.writeSubsSnapshot(req.subs)
			close(req.done)
		case <-s.ctx.Done():
			// Drain remaining on shutdown.
			for {
				select {
				case req := <-s.saveCh:
					s.writeSubsSnapshot(req.subs)
					close(req.done)
				default:
					return
				}
			}
		}
	}
}

// loadPreferences reads per-kind notification toggles from
// <configDir>/config.json and applies them. Missing file, missing
// keys, or parse failures fall through to the default state set
// in New via kindRegistry. Parse failures are logged at Warn level so
// a corrupted config.json silently reverting user toggles leaves a
// diagnostic trail.
func (s *Service) loadPreferences(ctx context.Context) {
	// Build local prefs map without holding mu — settings.Field does disk I/O.
	local := make(map[api.PushKind]bool, len(kindRegistry))
	for _, kr := range kindRegistry {
		if v, ok := settings.Field[bool](ctx, s.dir, kr.SettingsKey, kr.SettingsKey); ok {
			local[kr.Kind] = v
		} else {
			local[kr.Kind] = kr.DefaultOn
		}
	}
	// Single swap under mu — narrows critical section to one map assignment.
	s.mu.Lock()
	s.prefs = local
	s.mu.Unlock()
}
