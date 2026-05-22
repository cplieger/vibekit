package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vibekit/internal/api"
)

func TestNew_GeneratesKeys(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

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

func TestNew_PersistsAndReloadsKeys(t *testing.T) {
	dir := t.TempDir()
	s1 := New(context.Background(), dir, "mailto:test@example.com")
	key1 := s1.PublicKey()

	s2 := New(context.Background(), dir, "mailto:test@example.com")
	if s2.PublicKey() != key1 {
		t.Error("reloaded key differs from original")
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

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

func TestSubscriptionPersistence(t *testing.T) {
	dir := t.TempDir()
	s1 := New(context.Background(), dir, "mailto:test@example.com")
	s1.Subscribe(api.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/a"})
	s1.Subscribe(api.PushSubscription{Endpoint: "https://updates.push.services.mozilla.com/b"})
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

func TestSetPreferences(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

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

func TestVAPIDHeader(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

	header, err := s.vapidHeader("https://fcm.googleapis.com/fcm/send/abc123")
	if err != nil {
		t.Fatalf("vapidHeader: %v", err)
	}
	if !strings.HasPrefix(header, "vapid t=") {
		t.Errorf("header should start with 'vapid t=', got %q", header[:20])
	}
	if !strings.Contains(header, ", k=") {
		t.Error("header missing ', k=' public key")
	}

	// Verify the JWT signature.
	parts := strings.SplitN(strings.TrimPrefix(header, "vapid t="), ", k=", 2)
	token := parts[0]
	segments := strings.SplitN(token, ".", 3)
	if len(segments) != 3 {
		t.Fatalf("JWT has %d segments, want 3", len(segments))
	}

	// Decode claims and check audience.
	claimsJSON, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	if claims.Aud != "https://fcm.googleapis.com" {
		t.Errorf("aud = %q, want https://fcm.googleapis.com", claims.Aud)
	}
	if claims.Sub != "mailto:test@example.com" {
		t.Errorf("sub = %q", claims.Sub)
	}

	// Verify ECDSA signature.
	sigBytes, err := base64.RawURLEncoding.DecodeString(segments[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if len(sigBytes) != 64 {
		t.Fatalf("sig length = %d, want 64", len(sigBytes))
	}
	priv, _ := s.decodeVAPIDPrivateKey()
	hash := sha256.Sum256([]byte(segments[0] + "." + segments[1]))
	if !ecdsa.Verify(&priv.PublicKey, hash[:], decodeBigInt(sigBytes[:32]), decodeBigInt(sigBytes[32:])) {
		t.Fatal("signature verification failed")
	}
}

func decodeBigInt(b []byte) *big.Int {
	return new(big.Int).SetBytes(b)
}

func TestDeriveKeyNonce_Deterministic(t *testing.T) {
	shared := make([]byte, 32)
	auth := make([]byte, 16)
	clientPub := make([]byte, 65)
	serverPub := make([]byte, 65)
	salt := make([]byte, 16)

	// Same inputs should produce same outputs.
	cek1, nonce1, err := deriveKeyNonce(shared, auth, clientPub, serverPub, salt)
	if err != nil {
		t.Fatal(err)
	}
	cek2, nonce2, err := deriveKeyNonce(shared, auth, clientPub, serverPub, salt)
	if err != nil {
		t.Fatal(err)
	}
	if string(cek1) != string(cek2) {
		t.Error("CEK not deterministic")
	}
	if string(nonce1) != string(nonce2) {
		t.Error("nonce not deterministic")
	}
	if len(cek1) != 16 {
		t.Errorf("CEK length = %d, want 16", len(cek1))
	}
	if len(nonce1) != 12 {
		t.Errorf("nonce length = %d, want 12", len(nonce1))
	}
}

func TestDeriveKeyNonce_DifferentSalts(t *testing.T) {
	shared := make([]byte, 32)
	auth := make([]byte, 16)
	clientPub := make([]byte, 65)
	serverPub := make([]byte, 65)

	salt1 := make([]byte, 16)
	salt1[0] = 1
	salt2 := make([]byte, 16)
	salt2[0] = 2

	cek1, _, _ := deriveKeyNonce(shared, auth, clientPub, serverPub, salt1)
	cek2, _, _ := deriveKeyNonce(shared, auth, clientPub, serverPub, salt2)
	if string(cek1) == string(cek2) {
		t.Error("different salts should produce different CEKs")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	// Simulate a full encrypt/decrypt cycle using the same key derivation.
	clientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	salt := make([]byte, 16)
	rand.Read(salt)

	// Server encrypts.
	shared, _ := serverPriv.ECDH(clientPriv.PublicKey())
	cek, nonce, err := deriveKeyNonce(shared, authSecret,
		clientPriv.PublicKey().Bytes(), serverPriv.PublicKey().Bytes(), salt)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("hello push")
	padded := make([]byte, 2+len(plaintext))
	copy(padded[2:], plaintext)

	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	ciphertext := gcm.Seal(nil, nonce, padded, nil)

	// Client decrypts (same shared secret from the other direction).
	shared2, _ := clientPriv.ECDH(serverPriv.PublicKey())
	cek2, nonce2, _ := deriveKeyNonce(shared2, authSecret,
		clientPriv.PublicKey().Bytes(), serverPriv.PublicKey().Bytes(), salt)

	if string(cek) != string(cek2) {
		t.Fatal("CEK mismatch between encrypt and decrypt sides")
	}
	if string(nonce) != string(nonce2) {
		t.Fatal("nonce mismatch")
	}

	block2, _ := aes.NewCipher(cek2)
	gcm2, _ := cipher.NewGCM(block2)
	decrypted, err := gcm2.Open(nil, nonce2, ciphertext, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	// Strip 2-byte padding prefix.
	result := decrypted[2:]
	if string(result) != "hello push" {
		t.Errorf("decrypted = %q, want %q", result, "hello push")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		want string
		n    int
	}{
		{in: "short", n: 10, want: "short"},
		{in: "exactly10!", n: 10, want: "exactly10!"},
		{in: "this is longer than ten", n: 10, want: "this is lo..."},
		{in: "", n: 5, want: ""},
	}
	for _, tt := range tests {
		got := truncate(tt.in, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestSend_PreferenceFiltering(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	// Subscribe so Send actually reaches the preflight stage;
	// without subs the early-exit wouldn't prove the gate ran.
	s.Subscribe(api.PushSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/pref-test",
	})

	// With agentFinished disabled, Send for agent_finished must
	// NOT record a last-push timestamp — the preflight gate
	// short-circuits before the stamp.
	s.SetPreferences(map[api.PushKind]bool{
		api.PushKindAgentFinished: false,
		api.PushKindPermission:    true,
	})
	s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)
	s.mu.Lock()
	_, afRecorded := s.lastPush[api.PushKindAgentFinished]
	s.mu.Unlock()
	if afRecorded {
		t.Error("agentFinished=false should prevent Send from recording last-push timestamp")
	}

	// Mirror for permission.
	s.SetPreferences(map[api.PushKind]bool{
		api.PushKindAgentFinished: true,
		api.PushKindPermission:    false,
	})
	s.Send(context.Background(), "title", "body", api.PushKindPermission)
	s.mu.Lock()
	_, pnRecorded := s.lastPush[api.PushKindPermission]
	s.mu.Unlock()
	if pnRecorded {
		t.Error("permissionNeeded=false should prevent Send from recording last-push timestamp")
	}
}

func TestSend_Debounce(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	defer s.Close()

	// Set lastPush[agent_finished] to now to trigger debounce.
	s.mu.Lock()
	s.lastPush[api.PushKindAgentFinished] = time.Now()
	s.mu.Unlock()

	// Immediate second send should be debounced.
	// Subscribe a dummy endpoint so Send doesn't exit early on empty subs.
	s.Subscribe(api.PushSubscription{Endpoint: "https://push.example.com/debounce-test"})

	// Record lastPush before Send.
	s.mu.Lock()
	before := s.lastPush[api.PushKindAgentFinished]
	s.mu.Unlock()

	s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)

	// lastPush should not have been updated (debounced).
	s.mu.Lock()
	after := s.lastPush[api.PushKindAgentFinished]
	s.mu.Unlock()

	if !after.Equal(before) {
		t.Error("lastPush should not change when debounced")
	}
}

// TestSend_DebouncePerType pins the Q16/ops-u13c1-004 fix: a recent
// agent_finished push must NOT suppress a permission push (or vice
// versa) — debounce is keyed on type so the two windows are
// independent.
func TestSend_DebouncePerType(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

	// Mark agent_finished as just-sent.
	s.mu.Lock()
	s.lastPush[api.PushKindAgentFinished] = time.Now()
	s.mu.Unlock()

	// permission's window is empty; a permission Send must update
	// its own last-push timestamp (not blocked by the agent_finished
	// window).
	s.Subscribe(api.PushSubscription{Endpoint: "https://push.example.com/x"})
	s.Send(context.Background(), "title", "body", api.PushKindPermission)

	s.mu.Lock()
	permTimestamp := s.lastPush[api.PushKindPermission]
	s.mu.Unlock()
	if permTimestamp.IsZero() {
		t.Error("permission push was suppressed by agent_finished debounce window")
	}
}

// TestSend_UnknownKindRejected pins Q18: an unknown kind is refused
// with no persisted debounce side-effect.
func TestSend_UnknownKindRejected(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	s.Subscribe(api.PushSubscription{Endpoint: "https://push.example.com/x"})
	s.Send(context.Background(), "title", "body", "what-is-this")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lastPush["what-is-this"]; ok {
		t.Error("unknown kind should not record a debounce entry")
	}
}

func TestSend_UnhealthySkips(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	s.mu.Lock()
	s.healthy = false
	s.mu.Unlock()

	// Should return immediately without panicking.
	s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)
}

func TestSubscribe_OverwritesDuplicate(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

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

// --- loadPreferences ---

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
				if err := os.WriteFile(filepath.Join(dir, "settings.json"),
					[]byte(tt.settings), 0o644); err != nil {
					t.Fatalf("write settings: %v", err)
				}
			}

			s := New(context.Background(), dir, "mailto:test@example.com")

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

// --- decodeVAPIDPrivateKey error paths ---

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

// TestVAPIDHeader_InvalidKeyPropagatesError pins the vapidHeader →
// decodeVAPIDPrivateKey error forwarding so a future refactor can't
// accidentally swallow the error.
func TestVAPIDHeader_InvalidKeyPropagatesError(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

	s.keys.PrivateKey = "broken"

	if _, err := s.vapidHeader("https://fcm.googleapis.com/fcm/send/abc"); err == nil {
		t.Fatal("vapidHeader with broken key = nil error, want error")
	}
}

// TestVAPIDHeader_ExpWithin12h pins the 12h window so a future tweak
// back toward RFC's 24h ceiling doesn't slip past review.
func TestVAPIDHeader_ExpWithin12h(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	before := time.Now().Unix()
	header, err := s.vapidHeader("https://fcm.googleapis.com/fcm/send/x")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.SplitN(strings.TrimPrefix(header, "vapid t="), ", k=", 2)[0]
	seg := strings.Split(token, ".")[1]
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(seg)
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	delta := claims.Exp - before
	if delta <= 0 || delta > 12*3600+5 {
		t.Errorf("exp delta = %ds, want (0, 12h+5s] for 12h window", delta)
	}
}

// --- Close contract ---

func TestClose_CancelsInternalContext(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")

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
	s := New(context.Background(), dir, "mailto:test@example.com")
	s.Close()
	s.Close() // must not panic
}

// --- Stale-subscription pruning (end-to-end against httptest) ---

// pushSubscriptionWithValidKeys builds a subscription whose P256dh +
// Auth survive push()'s decode/import steps so the HTTP request
// actually fires against the test server.
func pushSubscriptionWithValidKeys(t *testing.T, endpoint string) api.PushSubscription {
	t.Helper()
	clientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen client key: %v", err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("auth secret: %v", err)
	}
	sub := api.PushSubscription{Endpoint: endpoint}
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(clientPriv.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(authSecret)
	return sub
}

func TestSend_StatusCodePruning(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantPruned bool
	}{
		{"PrunesOn410Gone", http.StatusGone, true},
		{"PrunesOn404NotFound", http.StatusNotFound, true},
		{"KeepsOn201Created", http.StatusCreated, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			dir := t.TempDir()
			s := New(context.Background(), dir, "mailto:test@example.com")
			s.client = srv.Client()
			s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

			s.Send(context.Background(), "title", "body", api.PushKindAgentFinished)

			if tt.wantPruned && s.HasSubscribers() {
				t.Errorf("Send did not prune subscription after %d", tt.status)
			}
			if !tt.wantPruned && !s.HasSubscribers() {
				t.Errorf("Send pruned subscription on %d", tt.status)
			}
		})
	}
}

// TestSaveSubs_Perm0o600 pins the file-mode hardening: subs file
// holds per-subscriber auth secrets and must never be world-readable.
func TestSaveSubs_Perm0o600(t *testing.T) {
	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	// Use an allowed endpoint so the file survives a future reload.
	s.Subscribe(api.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/perm-check"})

	info, err := os.Stat(filepath.Join(dir, "push-subs.json"))
	if err != nil {
		t.Fatalf("stat subs: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("subs file mode = %#o, want 0o600", mode)
	}
}

func TestSend_TruncatesOversizePayload(t *testing.T) {
	// Capture the payload the push endpoint receives.
	var receivedPayload []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The payload is encrypted, so we can't inspect it directly.
		// Instead, verify the Send path doesn't error out.
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	dir := t.TempDir()
	s := New(context.Background(), dir, "mailto:test@example.com")
	s.client = srv.Client()
	s.Subscribe(pushSubscriptionWithValidKeys(t, srv.URL))

	// Build a body that exceeds pushBodyCap (3000 bytes).
	title := "Vibekit"
	body := strings.Repeat("x", 4000)

	// Send should not panic or error — it truncates internally.
	s.Send(context.Background(), title, body, api.PushKindAgentFinished)

	// Verify the subscriber wasn't pruned (201 = success).
	if !s.HasSubscribers() {
		t.Error("subscriber was pruned after successful oversize send")
	}

	// Verify truncation logic directly: title + truncated body should
	// be within pushBodyCap.
	room := max(pushBodyCap-len(title)-3, 0)
	truncated := truncate(body, room)
	total := len(title) + len(truncated)
	if total > pushBodyCap {
		t.Errorf("truncated payload size = %d, exceeds cap %d", total, pushBodyCap)
	}
	if !strings.HasSuffix(truncated, "...") {
		t.Errorf("truncated body should end with '...', got suffix %q",
			truncated[max(len(truncated)-10, 0):])
	}

	_ = receivedPayload // used for documentation; encrypted payload can't be inspected
}

func BenchmarkPushEncrypt(b *testing.B) {
	// Generate realistic key material once.
	clientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	clientPubBytes := clientPriv.PublicKey().Bytes()
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		b.Fatal(err)
	}
	payload := []byte(`{"title":"Agent finished","body":"Task completed"}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ephPriv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			b.Fatal(err)
		}
		shared, err := ephPriv.ECDH(clientPriv.PublicKey())
		if err != nil {
			b.Fatal(err)
		}
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			b.Fatal(err)
		}
		cek, nonce, err := deriveKeyNonce(shared, authSecret, clientPubBytes, ephPriv.PublicKey().Bytes(), salt)
		if err != nil {
			b.Fatal(err)
		}
		padded := make([]byte, 2+len(payload))
		copy(padded[2:], payload)
		block, err := aes.NewCipher(cek)
		if err != nil {
			b.Fatal(err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			b.Fatal(err)
		}
		gcm.Seal(nil, nonce, padded, nil)
	}
}
