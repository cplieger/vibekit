package hub

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/secretstore"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// probeKey is the key shape KAS actually derives, hash and all
// (probe-confirmed sha256("http://127.0.0.1:46877/mcp" + "|")).
const probeKey = "kiro.mcp.2a0a3d1d4672ffaff77fcbe95f21be210e2e444f1b152fb537773dd72a3ddf3a.client"

func newSecretStore(t *testing.T) *secretstore.Store {
	t.Helper()
	s, err := secretstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("secretstore.New() error = %v", err)
	}
	return s
}

func rawParams(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return data
}

// TestSecretGetMissIsExplicitNull pins the wire contract. KAS reads
// result.value and treats null as "no credential yet"; an omitted key or an
// error would land in its catch-and-warn path and mean the same thing less
// clearly.
func TestSecretGetMissIsExplicitNull(t *testing.T) {
	store := newSecretStore(t)
	got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey}))
	if got.Value != nil {
		t.Errorf("Value = %q, want nil for a miss", *got.Value)
	}
	// The nil pointer must reach the wire as an explicit null, not an omitted
	// key: KAS reads result.value.
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(data) != `{"value":null}` {
		t.Errorf("wire form = %s, want {\"value\":null}", data)
	}
}

// TestSecretStoreThenGet is the round trip KAS depends on across a bridge
// spawn: the get that follows a store must return the same blob.
func TestSecretStoreThenGet(t *testing.T) {
	store := newSecretStore(t)
	ctx := t.Context()
	blob := `{"client_id":"probe-client-1","client_secret":"s3cret"}`

	result, err := secretStoreResult(ctx, store, rawParams(t, map[string]string{"key": probeKey, "value": blob}))
	if err != nil {
		t.Fatalf("secretStoreResult() error = %v, want nil", err)
	}
	if len(result) != 0 {
		t.Errorf("store result = %v, want an empty object", result)
	}

	got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey}))
	if got.Value == nil || *got.Value != blob {
		t.Errorf("Value = %v, want %q", got.Value, blob)
	}
}

// TestSecretDelete covers the delete leg, including the absent-key case KAS
// issues speculatively.
func TestSecretDelete(t *testing.T) {
	store := newSecretStore(t)
	ctx := t.Context()
	if err := store.Set(ctx, probeKey, "v"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := secretDeleteResult(ctx, store, rawParams(t, map[string]string{"key": probeKey})); err != nil {
		t.Fatalf("secretDeleteResult() error = %v, want nil", err)
	}
	if got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey})); got.Value != nil {
		t.Errorf("Value = %q after delete, want nil", *got.Value)
	}
	if _, err := secretDeleteResult(ctx, store, rawParams(t, map[string]string{"key": probeKey})); err != nil {
		t.Errorf("secretDeleteResult(absent) error = %v, want nil", err)
	}
}

// TestSecretStoreRejectsBadParams pins that a malformed request gets a JSON-RPC
// error rather than silently storing under an empty key. KAS RETHROWS a store
// failure, so this surfaces as a failed MCP connect — which is correct: the
// alternative is a credential filed where nothing will look for it.
func TestSecretStoreRejectsBadParams(t *testing.T) {
	store := newSecretStore(t)
	ctx := t.Context()

	cases := []struct {
		name   string
		params json.RawMessage
	}{
		{"no key", rawParams(t, map[string]string{"value": "v"})},
		{"empty key", rawParams(t, map[string]string{"key": "", "value": "v"})},
		{"not an object", json.RawMessage(`["key","value"]`)},
		{"nil params", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := secretStoreResult(ctx, store, tc.params); err == nil {
				t.Error("secretStoreResult() error = nil, want an error")
			}
			// The store must be untouched. Asserted through the read path
			// rather than an entry count, because "the credential is not
			// there" is the property that matters to KAS.
			if got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey})); got.Value != nil {
				t.Errorf("a rejected store left a value: %q", *got.Value)
			}
		})
	}
}

// TestSecretNilStoreDegradesRatherThanFails covers the not-configured hub (no
// configDir, and every existing hub test): a get must report "absent" so MCP
// OAuth falls back to re-registering per spawn, instead of failing the connect.
func TestSecretNilStoreDegradesRatherThanFails(t *testing.T) {
	if got := secretGetResult(nil, rawParams(t, map[string]string{"key": probeKey})); got.Value != nil {
		t.Errorf("Value = %q, want nil", *got.Value)
	}
	// A delete against no store succeeds: the key is already absent.
	if _, err := secretDeleteResult(t.Context(), nil, rawParams(t, map[string]string{"key": probeKey})); err != nil {
		t.Errorf("secretDeleteResult(nil store) error = %v, want nil", err)
	}
	// A store, though, must NOT claim success it cannot deliver — KAS would
	// then believe the credential is durable.
	if _, err := secretStoreResult(t.Context(), nil, rawParams(t, map[string]string{"key": probeKey, "value": "v"})); err == nil {
		t.Error("secretStoreResult(nil store) error = nil, want an error")
	}
}

// TestHandleKiroSecretRequestClaimsOnlyItsOwnMethods pins the dispatch hop.
// Claiming a method it cannot answer would swallow a frame the rest of the
// cascade needs; NOT claiming one of its own would leave an A→C request
// unanswered, which wedges the turn.
func TestHandleKiroSecretRequestClaimsOnlyItsOwnMethods(t *testing.T) {
	h, _, _ := newTestHub()
	var id int64 = 1
	cases := []struct {
		method string
		want   bool
	}{
		{methodKiroSecretGet, true},
		{methodKiroSecretStore, true},
		{methodKiroSecretDelete, true},
		{methodKiroGetAccessToken, false},
		{vibekit.MethodFSRead, false},
		{"_kiro/secret/unknown", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			msg := &vibekit.RPCResponse{Method: tc.method, ID: &id}
			// No bridge is registered, so respondBridge logs and drops the
			// write; the return value is the whole contract under test.
			if got := h.handleKiroSecretRequest(t.Context(), "c1", msg); got != tc.want {
				t.Errorf("handleKiroSecretRequest(%q) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

// TestSecretStoreIsSharedAcrossBridges pins the property that makes the whole
// feature work: the DCR result a chat bridge obtained must be visible to the
// NEXT bridge, or each one re-registers exactly as it did before the capability
// was declared.
func TestSecretStoreIsSharedAcrossBridges(t *testing.T) {
	store := newSecretStore(t)
	ctx := t.Context()

	// Bridge A stores a registration.
	if _, err := secretStoreResult(ctx, store, rawParams(t, map[string]string{"key": probeKey, "value": "reg"})); err != nil {
		t.Fatalf("bridge A store: %v", err)
	}
	// Bridge B — same process, same store pointer — reads it back.
	if got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey})); got.Value == nil || *got.Value != "reg" {
		t.Errorf("bridge B Value = %v, want %q", got.Value, "reg")
	}
}
