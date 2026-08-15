package push

// Tests for service.go: New (key generation, client timeout),
// Subscribe/Unsubscribe (including host logging), SetPreferences,
// loadPreferences, and the Close contract.

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/slogx/capture"
	"github.com/cplieger/vibekit/internal/api"
)

func TestNew_GeneratesKeys(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")

	if s.PublicKey() == "" {
		t.Fatal("public key is empty")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s.PublicKey())
	if err != nil {
		t.Fatalf("decode public key: %v", err)
	}
	if len(raw) != 65 {
		t.Errorf("public key length = %d, want 65 (uncompressed P-256)", len(raw))
	}
	if raw[0] != 0x04 {
		t.Errorf("public key prefix = 0x%02x, want 0x04", raw[0])
	}
}

// TestNew_ClientTimeout pins the 10-second HTTP client timeout New
// configures (an integer-division slip would zero it out).
func TestNew_ClientTimeout(t *testing.T) {
	s := New(t.Context(), t.TempDir(), testSubject)
	defer s.Close()

	if s.client.Timeout != 10*time.Second {
		t.Errorf("client.Timeout = %v, want %v", s.client.Timeout, 10*time.Second)
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	defer s.Close()

	sub := api.PushSubscription{Endpoint: "https://push.example.com/1"}
	sub.Keys.P256dh = "dGVzdA"
	sub.Keys.Auth = "YXV0aA"

	s.Subscribe(sub)
	if !s.HasSubscribers() {
		t.Error("expected subscribers after subscribe")
	}

	s.Unsubscribe("https://push.example.com/1")
	if s.HasSubscribers() {
		t.Error("expected no subscribers after unsubscribe")
	}
}

func TestSubscribe_OverwritesDuplicate(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	defer s.Close()

	sub1 := api.PushSubscription{Endpoint: "https://push.example.com/1"}
	sub1.Keys.Auth = "old"
	s.Subscribe(sub1)

	sub2 := api.PushSubscription{Endpoint: "https://push.example.com/1"}
	sub2.Keys.Auth = "new"
	s.Subscribe(sub2)

	s.mu.Lock()
	count := len(s.subs)
	auth := s.subs["https://push.example.com/1"].Keys.Auth
	s.mu.Unlock()

	if count != 1 {
		t.Errorf("count = %d, want 1 (should overwrite)", count)
	}
	if auth != "new" {
		t.Errorf("auth = %q, want 'new'", auth)
	}
}

// TestSubscribe_HostLogging verifies the host Subscribe logs: a
// parseable endpoint with a non-empty host logs that host; a parseable
// endpoint with an empty host (e.g. a mailto/opaque URL) keeps the
// "unknown" placeholder. The logged host is the only observable of this
// branch (Subscribe has no return value or exported state for it).
func TestSubscribe_HostLogging(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantHost string
	}{
		{
			name:     "non_empty_host_logged_verbatim",
			endpoint: "https://fcm.googleapis.com/fcm/send/sub",
			wantHost: "fcm.googleapis.com",
		},
		{
			name:     "empty_host_keeps_unknown",
			endpoint: "mailto:someone@example.com",
			wantHost: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(t.Context(), t.TempDir(), testSubject)
			defer s.Close() // drain writeLoop before TempDir cleanup

			// Install capture AFTER New so its "push: ready" line is excluded.
			capLog := capture.Default(t)
			s.Subscribe(api.PushSubscription{Endpoint: tt.endpoint})

			got, ok := capLog.AttrValue("push: subscribed", "host")
			if !ok {
				t.Fatalf("Subscribe(%q) did not emit a %q log line",
					tt.endpoint, "push: subscribed")
			}
			if got != tt.wantHost {
				t.Errorf("Subscribe(%q) logged host = %v, want %q",
					tt.endpoint, got, tt.wantHost)
			}
		})
	}
}

func TestSetPreferences(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")

	// Defaults: both true.
	s.mu.Lock()
	if !s.prefs[api.PushKindAgentFinished] || !s.prefs[api.PushKindPermission] {
		t.Error("default preferences should be true")
	}
	s.mu.Unlock()

	s.SetPreferences(map[api.PushKind]bool{
		api.PushKindAgentFinished: false,
		api.PushKindPermission:    true,
	})
	s.mu.Lock()
	if s.prefs[api.PushKindAgentFinished] {
		t.Error("agentFinished should be false")
	}
	if !s.prefs[api.PushKindPermission] {
		t.Error("permissionNeeded should be true")
	}
	s.mu.Unlock()
}

func TestLoadPreferences(t *testing.T) {
	tests := []struct {
		name     string
		settings string // empty means no file written
		wantAF   bool
		wantPN   bool
	}{
		{"MissingFileKeepsDefaults", "", true, true},
		{"AppliesDisabledToggles", `{"notify_agent_finished":false,"notify_permission":false}`, false, false},
		{"MalformedJSONFallsBackToDefaults", `{not json`, true, true},
		{"PartialJSONOnlyAgentFinished", `{"notify_agent_finished":false}`, false, true},
		{"PartialJSONOnlyPermission", `{"notify_permission":false}`, true, false},
		{"EmptyObjectKeepsDefaults", `{}`, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.settings != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"),
					[]byte(tt.settings), 0o644); err != nil {
					t.Fatalf("write settings: %v", err)
				}
			}

			s := New(t.Context(), dir, "mailto:test@example.com")

			s.mu.Lock()
			af, pn := s.prefs[api.PushKindAgentFinished], s.prefs[api.PushKindPermission]
			s.mu.Unlock()
			if af != tt.wantAF || pn != tt.wantPN {
				t.Errorf("agentFinished=%v permissionNeeded=%v, want %v %v",
					af, pn, tt.wantAF, tt.wantPN)
			}
		})
	}
}

// TestClose_CancelsInternalContext verifies Close cancels the service's
// internal context.
func TestClose_CancelsInternalContext(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")

	select {
	case <-s.ctx.Done():
		t.Fatal("context already Done before Close")
	default:
	}

	s.Close()

	select {
	case <-s.ctx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Error("context not Done after Close")
	}
}

// TestClose_IsIdempotent — context.CancelFunc is safe to call multiple
// times. Hub shutdown paths can race parent cancels; Close must
// tolerate that without panic.
func TestClose_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	s.Close()
	s.Close() // must not panic
}
