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
	"strings"
	"testing"

	"github.com/cplieger/slogx/capture"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestNew_PersistsAndReloadsKeys(t *testing.T) {
	dir := t.TempDir()
	s1 := New(t.Context(), dir, "mailto:test@example.com")
	key1 := s1.PublicKey()

	s2 := New(t.Context(), dir, "mailto:test@example.com")
	if s2.PublicKey() != key1 {
		t.Error("reloaded key differs from original")
	}
}

func TestSubscriptionPersistence(t *testing.T) {
	dir := t.TempDir()
	s1 := New(t.Context(), dir, "mailto:test@example.com")
	s1.Subscribe(vibekit.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/a"})
	s1.Subscribe(vibekit.PushSubscription{Endpoint: "https://updates.push.services.mozilla.com/b"})
	s1.flushSaves()
	s1.Close()

	s2 := New(t.Context(), dir, "mailto:test@example.com")
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
	subs := []vibekit.PushSubscription{
		{Endpoint: "https://fcm.googleapis.com/fcm/send/ok"},
		{Endpoint: "http://localhost:6379/SHUTDOWN"},
	}
	data, _ := json.Marshal(subs)
	if err := os.WriteFile(filepath.Join(dir, "push-subs.json"), data, 0o600); err != nil {
		t.Fatalf("write subs: %v", err)
	}
	s := New(t.Context(), dir, "mailto:test@example.com")
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
	subs := []vibekit.PushSubscription{
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

	s := &Service{dir: dir, subs: make(map[string]vibekit.PushSubscription)}
	capLog := capture.Default(t)

	s.loadSubs()

	got, ok := capLog.AttrValue("push: dropping subscription with disallowed endpoint", "host")
	if !ok {
		t.Fatalf("loadSubs did not log the disallowed-endpoint drop")
	}
	if got != "evil.example.com" {
		t.Errorf("dropped-endpoint host = %v, want %q", got, "evil.example.com")
	}
}

// writeSubsFile stages a push-subs.json holding the given endpoints: the
// pre-existing subscription store every orphan case below starts from.
func writeSubsFile(t *testing.T, dir string, endpoints ...string) {
	t.Helper()
	subs := make([]vibekit.PushSubscription, 0, len(endpoints))
	for _, ep := range endpoints {
		subs = append(subs, vibekit.PushSubscription{Endpoint: ep})
	}
	data, err := json.Marshal(subs)
	if err != nil {
		t.Fatalf("marshal subs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "push-subs.json"), data, 0o600); err != nil {
		t.Fatalf("write subs file: %v", err)
	}
}

// TestLoadKeys_ReportsOrphanedSubscriptions pins the one moment this state is
// cheap to detect: a keypair generated on a volume that already holds
// subscriptions has just made every one of them undeliverable, because RFC 8292
// section 4.2 requires the user agent to create the replacement. Nothing later
// can tell an orphaned subscription from a working one — it simply answers
// 401/403 forever — so without this line push is silently dead with no cause on
// the box.
func TestLoadKeys_ReportsOrphanedSubscriptions(t *testing.T) {
	const msg = "push: the new VAPID keypair orphaned every stored subscription"

	t.Run("a_generated_keypair_beside_a_subscription_store_says_so", func(t *testing.T) {
		dir := t.TempDir()
		writeSubsFile(t, dir,
			"https://fcm.googleapis.com/fcm/send/a",
			"https://updates.push.services.mozilla.com/b")

		capLog := capture.Default(t)
		s := New(t.Context(), dir, testSubject)
		defer s.Close()

		if n := capLog.CountExact(msg); n != 1 {
			t.Fatalf("orphan report logged %d times, want 1; logs = %q", n, capLog.Messages())
		}
		if got, _ := capLog.AttrValue(msg, "count"); got != "2" {
			t.Errorf("orphaned count = %q, want %q", got, "2")
		}
		if !capLog.HasAttr(msg, "hint", pushResubscribeHint) {
			t.Error("the orphan report carried no re-subscribe remedy")
		}
	})

	t.Run("an_adopted_keypair_orphans_nothing", func(t *testing.T) {
		dir := t.TempDir()
		writeSubsFile(t, dir, "https://fcm.googleapis.com/fcm/send/a")
		s1 := New(t.Context(), dir, testSubject) // generates: reports
		s1.Close()

		capLog := capture.Default(t)
		s2 := New(t.Context(), dir, testSubject) // adopts the stored key: silent
		defer s2.Close()

		if n := capLog.CountExact(msg); n != 0 {
			t.Errorf("a restart that reused its keypair reported %d orphan lines, want 0", n)
		}
	})

	t.Run("a_first_boot_reports_nothing", func(t *testing.T) {
		capLog := capture.Default(t)
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()

		if n := capLog.CountExact(msg); n != 0 {
			t.Errorf("a first boot with no subscriptions reported %d orphan lines, want 0", n)
		}
	})
}

// TestLoadKeys_ReportsUnusableStoredKeys pins the other door into the same
// state, and it needs nobody to have deleted anything: a vapid-keys.json that is
// present but unusable (a truncated write, a partial restore) is REPLACED, which
// heals the service and kills every stored subscription at once. The service
// stays healthy on purpose — refusing to send would leave no way to recover —
// so this line is the only record of what happened.
//
// half_written is the case the guard used to miss, and it is the one a persistent
// volume produces: the file is valid JSON carrying only the public half, so a
// PublicKey-only check adopted it, left vapidPriv nil, and every send was then
// dropped by preflightSend's health gate with no boot able to repair it. Signing
// is what the assertion reads for that reason — a fresh public key alone would
// pass while the service still could not send. Invariant 6: a broken state must
// be able to heal itself.
func TestLoadKeys_ReportsUnusableStoredKeys(t *testing.T) {
	const msg = "push: stored VAPID keys unusable, generating a replacement"
	cases := []struct {
		name    string
		content string
		wantWhy string // the reason attr, which names WHICH half of the file is wrong
	}{
		{"unparseable", "{not json", "invalid character"},
		{"no_public_key", `{"privateKey":"AAAA"}`, "no public key"},
		{"half_written", `{"publicKey":"BM7WFPsFDlXH-h3nMzE0cJmS1oO-cCXQxUvKPqR6TdE"}`, "no private key"},
		// Both halves present, the private one decoding to the wrong length: the
		// file a PublicKey-only guard cannot tell from a working keypair either.
		{"undecodable_private_key", `{"privateKey":"broken","publicKey":"also-broken"}`, "decode private key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "vapid-keys.json"), []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write keys file: %v", err)
			}

			capLog := capture.Default(t)
			s := New(t.Context(), dir, testSubject)
			defer s.Close()

			if n := capLog.CountExact(msg); n != 1 {
				t.Errorf("unusable keys file logged %d replacement lines, want 1; logs = %q",
					n, capLog.Messages())
			}
			// The reason is the operator's only description of what was wrong with
			// the file, and each case has its own: a half-written file and one whose
			// private key will not decode are different things to go and look at.
			if why, ok := capLog.AttrValue(msg, "error"); !ok || !strings.Contains(why, tc.wantWhy) {
				t.Errorf("replacement reason = %q (found=%v), want one naming %q",
					why, ok, tc.wantWhy)
			}
			if s.PublicKey() == "" || !s.healthy || s.vapidPriv == nil {
				t.Errorf("after replacing an unusable keys file: publicKey=%q healthy=%v signingKey=%v, "+
					"want a fresh keypair the service can sign with",
					s.PublicKey(), s.healthy, s.vapidPriv != nil)
			}
		})
	}
}

// TestLoadKeys_PersistSuccessNoWarn verifies loadKeys persists a freshly
// generated VAPID key pair to a writable dir without emitting the
// persist-failure warning.
func TestLoadKeys_PersistSuccessNoWarn(t *testing.T) {
	capLog := capture.Default(t)
	s := New(t.Context(), t.TempDir(), testSubject)
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

	s.writeSubsSnapshot([]vibekit.PushSubscription{
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
			subs:   map[string]vibekit.PushSubscription{},
			saveCh: make(chan saveRequest, 1),
			// The service lifetime is live so the send path is taken; the
			// per-call guard ctx is what each case varies.
			lifetime: t.Context(),
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
		s.saveSubsAsync(t.Context())
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
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.mu.Lock()
		s.subs[ep] = vibekit.PushSubscription{Endpoint: ep}
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
		s := New(t.Context(), t.TempDir(), testSubject)
		defer s.Close()
		s.mu.Lock()
		s.subs[ep] = vibekit.PushSubscription{Endpoint: ep}
		s.mu.Unlock()
		_ = os.Remove(s.subsPath())

		s.saveSubs(t.Context())

		if _, err := os.Stat(s.subsPath()); err != nil {
			t.Errorf("saveSubs(active) did not write %s: %v", s.subsPath(), err)
		}
	})
}

// TestSaveSubs_Perm0o600 pins the file-mode hardening: subs file
// holds per-subscriber auth secrets and must never be world-readable.
func TestSaveSubs_Perm0o600(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")
	defer s.Close()
	// Use an allowed endpoint so the file survives a future reload.
	s.Subscribe(vibekit.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/perm-check"})
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
	s := New(t.Context(), dir, "mailto:test@example.com")

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
	s := New(t.Context(), dir, "mailto:test@example.com")

	s.keys.PrivateKey = "not$valid$base64!!"

	if _, err := s.decodeVAPIDPrivateKey(); err == nil {
		t.Fatal("decodeVAPIDPrivateKey with invalid base64 = nil error, want error")
	}
}

func TestDecodeVAPIDPrivateKey_WrongLength(t *testing.T) {
	dir := t.TempDir()
	s := New(t.Context(), dir, "mailto:test@example.com")

	// Valid base64 but only 16 bytes; P-256 requires 32.
	s.keys.PrivateKey = base64.RawURLEncoding.EncodeToString(make([]byte, 16))

	if _, err := s.decodeVAPIDPrivateKey(); err == nil {
		t.Fatal("decodeVAPIDPrivateKey with 16-byte key = nil error, want error")
	}
}
