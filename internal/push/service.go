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
	"slices"
	"sync"
	"time"

	"github.com/cplieger/ssrf/v4"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
	"golang.org/x/sync/singleflight"
)

// DefaultTitle is the notification title used for all Web Push messages.
const DefaultTitle = "Vibekit"

// pushDebounce is the per-subject quiet window; pushResponseCap bounds
// body drain for keep-alive-friendly reads; pushBodyCap caps the
// combined title+body payload so an accidental megabyte send doesn't
// get silently rejected by the push vendor.
const (
	pushDebounce    = 5 * time.Second
	pushResponseCap = 64 << 10 // 64 KiB — vendors return tiny bodies
	pushBodyCap     = 3000     // title+body ceiling; pre-pad room under 4096 record
	pushFanOutLimit = 3        // max concurrent push sends per notification

	pushMaxAttempts = 3 // total tries for a retryable delivery failure

	// debounceHighWater is when preflightSend prunes expired debounce entries.
	//
	// Keying the window by subject makes the map's size a function of how many
	// distinct things have been notified about rather than of how many kinds
	// exist, so a process running for weeks would otherwise hold one slot per
	// chat and pull request it ever sent about. An entry older than the window
	// can no longer suppress anything, so dropping it is free; the high-water
	// mark just keeps the sweep off the common path.
	debounceHighWater = 64
)

// pushSubjectGlobal is the debounce subject for a notification with nothing single
// behind it (an empty vibekit.PushSubject — see its doc comment).
//
// Named rather than left as the empty string so the workspace-global window is a
// stated member of the key space instead of an accident of a zero value. It is
// deliberately not a legal subject spelling: a chat id cannot contain a space and a
// PR key is prefixed `pr:`, so nothing real can land in this slot.
const pushSubjectGlobal = "<workspace global>"

// pushDebounceKey is what a quiet window belongs to: one KIND about one SUBJECT.
//
// Kind alone was wrong and silently lossy. The poller gives each pull request its
// own subject so two PRs settling together occupy their own tray slots, but a
// kind-only window dropped the second send inside five seconds — and the poller had
// already advanced its `seen` state for that PR, so the notification was never
// retried. Coalescing repeats of ONE subject is the behaviour worth having;
// coalescing two different subjects is data loss.
type pushDebounceKey struct {
	kind    vibekit.PushKind
	subject string
}

// debounceKey builds the window key for one send.
func debounceKey(kind vibekit.PushKind, subject vibekit.PushSubject) pushDebounceKey {
	switch {
	case subject.ChatID != "":
		return pushDebounceKey{kind: kind, subject: string(subject.ChatID)}
	case subject.Key != "":
		return pushDebounceKey{kind: kind, subject: subject.Key}
	default:
		return pushDebounceKey{kind: kind, subject: pushSubjectGlobal}
	}
}

// Retry timing for a retryable delivery failure (429 or 5xx). Vars, not
// consts, so a test can collapse the ladder instead of sleeping through it
// (the fleet's usual delay-override pattern).
//
// The budget is the notification's USEFULNESS window, not a generous transport
// allowance: a permission ask is moot once answered and "agent finished" is
// moot an hour later, so a delivery landing after this is worse than none.
var (
	pushRetryBudget = 60 * time.Second
	pushRetryBase   = 1 * time.Second
)

type vapidKeys struct {
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
}

// Service manages push subscriptions and sends notifications.
type Service struct {
	// lifetime is the service's OWN cancellable child of the context New
	// requires; it is the writeLoop-liveness signal, distinct from any
	// caller's ctx. Merging the two would reopen a shutdown hang: saveSubs and
	// flushSaves wait on `done` closing, which never happens if Close() raced
	// the send after writeLoop already exited. See persist.go's guarded waits.
	lifetime      context.Context
	prefsFlight   singleflight.Group
	saveCh        chan saveRequest
	writeLoopDone chan struct{}
	cancel        context.CancelFunc
	client        *http.Client
	lastPush      map[pushDebounceKey]time.Time
	subs          map[string]vibekit.PushSubscription
	prefs         map[vibekit.PushKind]bool
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
	subs []vibekit.PushSubscription
}

// New creates a Service, loads persisted subscriptions and preferences, and starts the write loop.
// subject is the VAPID subject (mailto: or https: URI identifying the sender).
//
// ctx is the service's lifetime and is required — it is passed straight to
// context.WithCancel, so a nil one is refused there, at the single construction
// site, rather than defaulted into a service nothing can stop.
func New(ctx context.Context, configDir, subject string) *Service {
	ctx, cancel := context.WithCancel(ctx)
	prefs := make(map[vibekit.PushKind]bool, len(kindRegistry))
	for _, kr := range kindRegistry {
		prefs[kr.Kind] = kr.DefaultOn
	}
	s := &Service{
		subs:          make(map[string]vibekit.PushSubscription),
		lastPush:      make(map[pushDebounceKey]time.Time),
		subject:       subject,
		dir:           configDir,
		lifetime:      ctx,
		cancel:        cancel,
		prefs:         prefs,
		saveCh:        make(chan saveRequest, 1),
		writeLoopDone: make(chan struct{}),
		healthy:       true,
	}
	// isAllowedPushEndpoint is the primary gate (name-based vendor allowlist,
	// https-only, no explicit ports); ssrf.SafeTransport is the IP-layer
	// backstop, re-validating the resolved/connected IP to close DNS-rebinding
	// vectors a name-based check alone leaves open. CheckRedirect re-checks
	// the allowlist on every hop.
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
	s.loadPreferences(s.lifetime)
	go s.writeLoop()
	s.mu.Lock()
	n := len(s.subs)
	s.mu.Unlock()
	slog.Info("push: ready", "subscribers", n, "healthy", s.healthy)
	return s
}

// Close cancels any in-flight pushes and waits for the write loop to
// drain pending saves. Call from the runtime's shutdown path so pending
// sends don't hold the shutdown up to 10s each.
//
// It SIGNALS and WAITS, which is the half of the rule the lifetime field serves:
// cancelling s.lifetime is the signal, and <-s.writeLoopDone is the proof the
// loop went quiet.
func (s *Service) Close() {
	s.cancel()
	<-s.writeLoopDone
}

// PublicKey returns the VAPID public key used for push subscription registration.
func (s *Service) PublicKey() string { return s.keys.PublicKey }

// SetPreferences updates the per-kind notification enabled flags.
func (s *Service) SetPreferences(prefs map[vibekit.PushKind]bool) {
	s.mu.Lock()
	maps.Copy(s.prefs, prefs)
	s.mu.Unlock()
}

// Subscribe registers a push subscription endpoint. Duplicate endpoints are silently overwritten.
func (s *Service) Subscribe(sub vibekit.PushSubscription) {
	s.mu.Lock()
	s.subs[sub.Endpoint] = sub
	s.mu.Unlock()
	s.saveSubsAsync(s.lifetime)
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
	s.saveSubsAsync(s.lifetime)
}

// HasSubscribers reports whether any push subscriptions are currently registered.
func (s *Service) HasSubscribers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0
}

// kindRegistry is the single source of truth for push notification kinds.
// init() below validates every entry against vibekit.PushKind.Valid() so the
// two cannot drift.
//
// An EMPTY SettingsKey means the kind has no writable preference: it is a
// floor, always DefaultOn. PushKindPermission is the one such kind — see the
// "no notify_permission key" note in internal/settings/defaults.go.
var kindRegistry = []KindPref{
	{vibekit.PushKindAgentFinished, settings.KeyNotifyAgentFinished, true},
	{vibekit.PushKindPRStatus, settings.KeyNotifyPRStatus, true},
	{vibekit.PushKindPermission, "", true},
}

// KindPref is one registered kind. Exported so the settings write path can
// derive its preference map from this registry instead of keeping a third
// hand-maintained copy beside vibekit.pushKinds.
type KindPref struct {
	Kind        vibekit.PushKind
	SettingsKey string
	DefaultOn   bool
}

// Kinds returns the registered kinds and their settings keys. A caller
// wanting only the configurable ones filters on a non-empty SettingsKey.
// Returns a copy so no consumer can reorder or extend the registry.
func Kinds() []KindPref { return slices.Clone(kindRegistry) }

func init() {
	if err := validateKindRegistry(kindRegistry); err != nil {
		panic("push: " + err.Error())
	}
}

// validateKindRegistry enforces the two rules the registry's types cannot: the
// registry must agree with vibekit.PushKind.Valid(), and an empty SettingsKey
// (an unconfigurable floor) is legal only for the permission kind, and only
// when DefaultOn — otherwise a forgotten key would silently ship a
// permanently-on, unwritable toggle.
func validateKindRegistry(entries []KindPref) error {
	for _, kr := range entries {
		if !kr.Kind.Valid() {
			return errors.New("kindRegistry contains invalid PushKind: " + string(kr.Kind))
		}
		if kr.SettingsKey != "" {
			continue
		}
		if kr.Kind != vibekit.PushKindPermission {
			return errors.New("kindRegistry entry " + string(kr.Kind) +
				" declares no settings key; only the permission floor may omit one")
		}
		if !kr.DefaultOn {
			return errors.New("the keyless permission floor must be DefaultOn: " +
				"an unanswered ask blocks the turn")
		}
	}
	return nil
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
		case <-s.lifetime.Done():
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
// in New via kindRegistry.
func (s *Service) loadPreferences(ctx context.Context) {
	// Build local prefs map without holding mu — settings.Field does disk I/O.
	local := make(map[vibekit.PushKind]bool, len(kindRegistry))
	for _, kr := range kindRegistry {
		// A keyless kind is a floor: no disk read can turn it off.
		if kr.SettingsKey == "" {
			local[kr.Kind] = kr.DefaultOn
			continue
		}
		if v, ok := settings.Field[bool](ctx, s.dir, kr.SettingsKey); ok {
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
