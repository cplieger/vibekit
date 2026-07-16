package push

// Tests for persist.go: VAPID key load/generate, subscription
// load/save (including the load-time allowlist re-validation and the
// 0600 file mode), the context guards on the async/sync save paths,
// and decodeVAPIDPrivateKey.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/slogx/capture"
	"github.com/cplieger/vibekit/internal/api"
)

func TestNew_PersistsAndReloadsKeys(t *testing.T) {
	dir := t.TempDir()
	s1 := New(context.Background(), dir, "mailto:test@example.com")
	key1 := s1.PublicKey()

	s2 := New(context.Background(), dir, "mailto:test@example.com")
	if s2.PublicKey() != key1 {
		t.Error("reloaded key differs from original")
	}
}

func TestSubscriptionPersistence(t *testing.T) {
	dir := t.TempDir()
	s1 := New(context.Background(), dir, "mailto:test@example.com")
	s1.Subscribe(api.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/a"})
	s1.Subscribe(api.PushSubscription{Endpoint: "https://updates.push.services.mozilla.com/b"})
	s1.flushSaves()
	s1.Close()

	s2 := New(context.Background(), dir, "mailto:test@example.com")
	if !s2.HasSubscribers() {
		t.Error("subscriptions not persisted")
	}
	s2.mu.Lock()
	count := len(s2.subs)
	s2.mu.Unlock()
	if count != 2 {
		t.Errorf("subscription count = %d, want 2", count)
	}
}

// TestLoadSubs_DropsDisallowedEndpoints pins the re-validation gate:
// a subs file written under a looser ruleset (or tampered manually)
// must NOT resurrect endpoints today's allowlist rejects.
func TestLoadSubs_DropsDisallowedEndpoints(t *testing.T) {
	dir := t.TempDir()
	// Write a subs file directly with one allowed + one disallowed.
	subs := []api.PushSubscription{
		{Endpoint: "https://fcm.googleapis.com/fcm/send/ok"},
		{Endpoint: "http://localhost:6379/SHUTDOWN"},
	}
	data, _ := json.Marshal(subs)
	if err := os.WriteFile(filepath.Join(dir, "push-subs.json"), data, 0o600); err != nil {
		t.Fatalf("write subs: %v", err)
	}
	s := New(context.Background(), dir, "mailto:test@example.com")
	s.mu.Lock()
	_, okOk := s.subs["https://fcm.googleapis.com/fcm/send/ok"]
	_, badOk := s.subs["http://localhost:6379/SHUTDOWN"]
	count := len(s.subs)
	s.mu.Unlock()
	if !okOk {
		t.Error("allowed endpoint was dropped on load")
	}
	if badOk {
		t.Error("disallowed endpoint survived load-time re-validation")
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (allowed only)", count)
	}
}

// TestLoadSubs_DropsDisallowedHostLogged verifies loadSubs logs the
// parsed host (not "unknown") when it drops a disallowed endpoint, so
// an operator can see which endpoint was rejected at load time.
func TestLoadSubs_DropsDisallowedHostLogged(t *testing.T) {
	dir := t.TempDir()
	subs := []api.PushSubscription{
		{Endpoint: "https://evil.example.com/steal"},
	}
	data, err := json.Marshal(subs)
	if err != nil {
		t.Fatalf("marshal subs: %v", err)
	}
	subsFile := (&Service{dir: dir}).subsPath()
	if werr := os.WriteFile(subsFile, data, 0o600); werr != nil {
		t.Fatalf("write subs file: %v", werr)
	}

	s := &Service{dir: dir, subs: make(map[string]api.PushSubscription)}
	capLog := capture.Default(t)

	s.loadSubs()

	rec, ok := findLogRec(capLog, "push: dropping subscription with disallowed endpoint")
	if !ok {
		t.Fatalf("loadSubs did not log the disallowed-endpoint drop")
	}
	if got := rec.attrs["host"]; got != "evil.example.com" {
		t.Errorf("dropped-endpoint host = %v, want %q", got, "evil.example.com")
	}
}

// TestLoadKeys_PersistSuccessNoWarn verifies loadKeys persists a freshly
// generated VAPID key pair to a writable dir without emitting the
// persist-failure warning.
func TestLoadKeys_PersistSuccessNoWarn(t *testing.T) {
	capLog := capture.Default(t)
	s := New(context.Background(), t.TempDir(), testSubject)
	defer s.Close()

	if capLog.CountExact("push: persist VAPID keys failed") > 0 {
		t.Errorf("loadKeys logged %q on a successful key write; want no warning",
			"push: persist VAPID keys failed")
	}
}

// TestWriteSubsSnapshot_SuccessNoWarn verifies a write to a writable dir
// emits no persist-failure warning and actually lands the file on disk.
func TestWriteSubsSnapshot_SuccessNoWarn(t *testing.T) {
	dir := t.TempDir()
	s := &Service{dir: dir}
	capLog := capture.Default(t)

	s.writeSubsSnapshot([]api.PushSubscription{
		{Endpoint: "https://fcm.googleapis.com/fcm/send/snap"},
	})

	if capLog.CountExact("push: persist subscriptions failed") > 0 {
		t.Errorf("writeSubsSnapshot logged %q on a successful write; want none",
			"push: persist subscriptions failed")
	}
	// Sanity: the file was actually written (proves we hit the success path).
	if _, err := os.Stat(s.subsPath()); err != nil {
		t.Fatalf("writeSubsSnapshot did not write %s: %v", s.subsPath(), err)
	}
}

// TestSaveSubsAsync_CtxGuard verifies saveSubsAsync enqueues a write
// only when the passed context is live: a cancelled guard ctx skips the
// enqueue, an active one queues exactly one request.
func TestSaveSubsAsync_CtxGuard(t *testing.T) {
	newSvc := func() *Service {
		return &Service{
			subs:   map[string]api.PushSubscription{},
			saveCh: make(chan saveRequest, 1),
			ctx:    context.Background(), // service ctx active so the send path is taken
		}
	}

	t.Run("cancelled_guard_skips_save", func(t *testing.T) {
		s := newSvc()
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.saveSubsAsync(cctx)
		if n := len(s.saveCh); n != 0 {
			t.Errorf("saveSubsAsync(cancelled) queued %d requests, want 0", n)
		}
	})

	t.Run("active_guard_queues_save", func(t *testing.T) {
		s := newSvc()
		s.saveSubsAsync(context.Background())
		if n := len(s.saveCh); n != 1 {
			t.Errorf("saveSubsAsync(active) queued %d requests, want 1", n)
		}
	})
}

// TestSaveSubs_CtxGuard verifies saveSubs writes synchronously only when
// the passed context is live: a cancelled guard ctx skips the write, an
// active one persists the file.
func TestSaveSubs_CtxGuard(t *testing.T) {
	const ep = "https://fcm.googleapis.com/fcm/send/savesubs"

	t.Run("cancelled_guard_skips_write", func(t *testing.T) {
		s := New(context.Background(), t.TempDir(), testSubject)
		defer s.Close()
		s.mu.Lock()
		s.subs[ep] = api.PushSubscription{Endpoint: ep}
		s.mu.Unlock()
		_ = os.Remove(s.subsPath()) // ensure absent before the call

		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.saveSubs(cctx)

		if _, err := os.Stat(s.subsPath()); !os.IsNotExist(err) {
			t.Errorf("saveSubs(cancelled) wrote %s (stat err=%v), want no write",
				s.subsPath(), err)
		}
	})

	t.Run("active_guard_writes", func(t *testing.T) {
		s := New(context.Background(), t.TempDir(), testSubject)
		defer s.Close()
		s.mu.Lock()
		s.subs[ep] = api.PushSubscription{Endpoint: ep}
		s.mu.Unlock()
		_ = os.Remove(s.subsPath())

		s.saveSubs(context.Background())

		if _, err := os.Stat(s.subsPath()); err != nil {
			t.Errorf("saveSubs(active) did not write %s: %v", s.subsPath(), err)
		}
	})
}

// TestSaveSubs_Perm0o600 pins the file-mode hardening: subs file
// holds per-subscriber auth secrets and must never be world-readable.
func TestSaveSubs_Perm0o600(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	defer s.Close()
	// Use an allowed endpoint so the file survives a future reload.
	s.Subscribe(api.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/perm-check"})
	s.flushSaves()

	info, err := os.Stat(filepath.Join(dir, "push-subs.json"))
	if err != nil {
		t.Fatalf("stat subs: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("subs file mode = %#o, want 0o600", mode)
	}
}

func TestDecodeVAPIDPrivateKey(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

	priv, err := s.decodeVAPIDPrivateKey()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if priv.Curve != elliptic.P256() {
		t.Error("wrong curve")
	}
	// Verify the key is valid by signing and verifying.
	hash := sha256.Sum256([]byte("test"))
	r, ss, signErr := ecdsa.Sign(rand.Reader, priv, hash[:])
	if signErr != nil {
		t.Fatalf("sign: %v", signErr)
	}
	if !ecdsa.Verify(&priv.PublicKey, hash[:], r, ss) {
		t.Error("signature verification failed")
	}
}

func TestDecodeVAPIDPrivateKey_InvalidBase64(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

	s.keys.PrivateKey = "not$valid$base64!!"

	if _, err := s.decodeVAPIDPrivateKey(); err == nil {
		t.Fatal("decodeVAPIDPrivateKey with invalid base64 = nil error, want error")
	}
}

func TestDecodeVAPIDPrivateKey_WrongLength(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

	// Valid base64 but only 16 bytes; P-256 requires 32.
	s.keys.PrivateKey = base64.RawURLEncoding.EncodeToString(make([]byte, 16))

	if _, err := s.decodeVAPIDPrivateKey(); err == nil {
		t.Fatal("decodeVAPIDPrivateKey with 16-byte key = nil error, want error")
	}
}

// TestVAPIDHeader_InvalidKeyPropagatesError verifies that a service
// constructed with an invalid VAPID key is marked unhealthy at startup.
// With the cached key approach, invalid keys are caught at construction
// time rather than at per-push time.
func TestVAPIDHeader_InvalidKeyPropagatesError(t *testing.T) {
	dir := t.TempDir()
	// Write an invalid key file so loadKeys finds it but can't decode it.
	badKeys := `{"privateKey":"broken","publicKey":"also-broken"}`
	if err := os.WriteFile(filepath.Join(dir, "vapid-keys.json"), []byte(badKeys), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(context.Background(), dir, "mailto:test@example.com")
	if s.vapidPriv != nil {
		t.Fatal("vapidPriv should be nil for invalid key")
	}
	// The service should be unhealthy.
	if s.healthy {
		t.Fatal("service should be unhealthy with invalid VAPID key")
	}
}
