package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/workspace"
)

// readKAS decodes the rendered KAS config file.
func readKAS(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kas config: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse kas config %s: %v", data, err)
	}
	return doc
}

// readKASServers decodes just the mcpServers block.
func readKASServers(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	doc := readKAS(t, path)
	raw, ok := doc[kasServerKey]
	if !ok {
		t.Fatalf("kas config has no %q key: %v", kasServerKey, doc)
	}
	var out map[string]map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse mcpServers: %v", err)
	}
	return out
}

// newIsolatedStore builds a store whose BOTH files live under t.TempDir().
func newIsolatedStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas", "mcp.json")
	s, err := New(context.Background(), dir, nil, WithKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, kas
}

// TestNew_DefaultKASPathIsUnderKiroHome pins that PRODUCTION gets the real path,
// which is the other half of the isolation option: every test overrides the path,
// so without this nothing would notice if the default silently changed (or if a
// refactor left it empty and started writing to the process's cwd).
//
// It is also the regression test for a real incident: before the option existed,
// `New` resolved the default eagerly and the package's own tests wrote the
// developer's own ~/.kiro/settings/mcp.json.
func TestNew_DefaultKASPathIsUnderKiroHome(t *testing.T) {
	home := t.TempDir()
	workspace.SetKiroHomeForTest(t, filepath.Join(home, ".kiro"))

	dir := t.TempDir()
	s, err := New(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := filepath.Join(home, ".kiro", "settings", "mcp.json")
	if s.kasPath != want {
		t.Errorf("kasPath = %q, want %q", s.kasPath, want)
	}
	// And it really wrote there, so the path is used and not merely stored.
	if _, err := os.Stat(want); err != nil {
		t.Errorf("no config at the default path: %v", err)
	}
}

// TestWriteKASConfig_StdioShape pins the stdio entry: command/args plus env as a
// RECORD (the store holds ordered KeyPairs; KAS's schema is a map).
func TestWriteKASConfig_StdioShape(t *testing.T) {
	s, kas := newIsolatedStore(t)
	srv, err := NewServer(TransportStdio, "gh",
		WithCommand("npx", "-y", "gh-mcp"),
		WithEnv([]KeyPair{{Name: "TOKEN", Value: "t1"}, {Name: "MODE", Value: "ro"}}))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := s.Create(context.Background(), srv); err != nil {
		t.Fatalf("Create: %v", err)
	}

	entry := readKASServers(t, kas)["gh"]
	if entry["command"] != "npx" {
		t.Errorf("command = %v, want npx", entry["command"])
	}
	args, ok := entry["args"].([]any)
	if !ok || len(args) != 2 || args[0] != "-y" || args[1] != "gh-mcp" {
		t.Errorf("args = %v, want [-y gh-mcp]", entry["args"])
	}
	env, ok := entry["env"].(map[string]any)
	if !ok {
		t.Fatalf("env = %T, want an object (KAS's schema is a record, not an array)", entry["env"])
	}
	if env["TOKEN"] != "t1" || env["MODE"] != "ro" {
		t.Errorf("env = %v, want TOKEN=t1 MODE=ro", env)
	}
	// url/headers belong to the other transport and must not appear.
	if _, ok := entry["url"]; ok {
		t.Errorf("stdio entry carries a url: %v", entry)
	}
}

// TestWriteKASConfig_RemoteCarriesOAuth is the one that matters most, because
// this field never reached the agent before. On the inline path KAS's
// acpServerToWire read only command/args/env or url/headers plus two _meta
// fields — it DROPPED oauth, so a pre-registered client id for a server that
// cannot do dynamic client registration (Slack, GitHub, Figma) was collected in
// the UI, validated, persisted, and then silently discarded.
func TestWriteKASConfig_RemoteCarriesOAuth(t *testing.T) {
	s, kas := newIsolatedStore(t)
	srv, err := NewServer(TransportHTTP, "slack",
		WithURL("https://slack.example/mcp"),
		WithOAuthClientID("cid-123"))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.OAuthClientSecret = "shh"
	if _, err := s.Create(context.Background(), srv); err != nil {
		t.Fatalf("Create: %v", err)
	}

	entry := readKASServers(t, kas)["slack"]
	oauth, ok := entry["oauth"].(map[string]any)
	if !ok {
		t.Fatalf("oauth = %T, want an object — the field the inline path dropped", entry["oauth"])
	}
	if oauth["clientId"] != "cid-123" || oauth["clientSecret"] != "shh" {
		t.Errorf("oauth = %v, want clientId=cid-123 clientSecret=shh", oauth)
	}
	if entry["url"] != "https://slack.example/mcp" {
		t.Errorf("url = %v", entry["url"])
	}
}

// TestWriteKASConfig_NoTypeHint pins that no `type` is emitted. KAS accepts one
// and then ignores it: transport is inferred from which fields are present and
// HTTP-vs-SSE is negotiated at connect time. Writing it would imply the store's
// http/sse split still means something on the wire, and a reader would look for
// behaviour that is not there.
func TestWriteKASConfig_NoTypeHint(t *testing.T) {
	s, kas := newIsolatedStore(t)
	for _, tr := range []Transport{TransportHTTP, TransportSSE} {
		srv, err := NewServer(tr, "srv-"+string(tr), WithURL("https://x.example/mcp"))
		if err != nil {
			t.Fatalf("NewServer(%s): %v", tr, err)
		}
		if _, err := s.Create(context.Background(), srv); err != nil {
			t.Fatalf("Create(%s): %v", tr, err)
		}
	}
	servers := readKASServers(t, kas)
	http, sse := servers["srv-http"], servers["srv-sse"]
	if _, ok := http["type"]; ok {
		t.Errorf("http entry carries a type hint: %v", http)
	}
	if _, ok := sse["type"]; ok {
		t.Errorf("sse entry carries a type hint: %v", sse)
	}
	// Same url, so the two render identically — the distinction is the store's.
	if http["url"] != sse["url"] {
		t.Errorf("http url %v != sse url %v", http["url"], sse["url"])
	}
}

// TestWriteKASConfig_DisabledServerStaysWithFlag pins that a disabled server is
// RETAINED with `disabled: true` rather than omitted. Omitting it would make
// "off" indistinguishable from "deleted" to KAS, and its status would go missing
// from the UI instead of reading "disabled".
func TestWriteKASConfig_DisabledServerStaysWithFlag(t *testing.T) {
	s, kas := newIsolatedStore(t)
	srv, err := NewServer(TransportStdio, "off-server", WithCommand("npx"))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	created, err := s.Create(context.Background(), srv)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.SetEnabled(context.Background(), created.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	entry, ok := readKASServers(t, kas)["off-server"]
	if !ok {
		t.Fatal("a disabled server was dropped from the file; KAS cannot report it as disabled")
	}
	if entry["disabled"] != true {
		t.Errorf("disabled = %v, want true", entry["disabled"])
	}
}

// TestWriteKASConfig_PreservesForeignKeys pins the shared-file contract: KAS
// reads `powers.mcpServers` out of this same file, so a write must replace ONLY
// the key vibekit owns.
func TestWriteKASConfig_PreservesForeignKeys(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "mcp.json")
	seed := `{"mcpServers":{"stale":{"command":"gone"}},"powers":{"mcpServers":{"kept":{"command":"keepme"}}}}`
	if err := os.WriteFile(kas, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := New(context.Background(), dir, nil, WithKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = s

	doc := readKAS(t, kas)
	powers, ok := doc["powers"]
	if !ok {
		t.Fatal("the powers block was clobbered; KAS reads it from this file too")
	}
	if !strings.Contains(string(powers), "keepme") {
		t.Errorf("powers = %s, want the seeded entry preserved", powers)
	}
	// And ours was replaced, not merged: the store is empty, so the stale entry
	// must be gone.
	if strings.Contains(string(doc[kasServerKey]), "stale") {
		t.Errorf("mcpServers = %s, want the stale entry replaced by the store's (empty) set", doc[kasServerKey])
	}
}

// TestWriteKASConfig_Mode0600 pins the file mode. It holds header values and
// OAuth client secrets.
func TestWriteKASConfig_Mode0600(t *testing.T) {
	_, kas := newIsolatedStore(t)
	info, err := os.Stat(kas)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// TestRenderKASServers_SkipsUnknownTransport pins that a record with a transport
// this code does not understand is skipped rather than written. Such an entry
// would have neither a command nor a url, and KAS rejects that with a warning —
// so writing it produces log noise and no server.
func TestRenderKASServers_SkipsUnknownTransport(t *testing.T) {
	got := renderKASServers([]*Server{
		{Name: "weird", Transport: Transport("carrier-pigeon"), Enabled: true},
		{Name: "fine", Transport: TransportStdio, Command: "npx", Enabled: true},
		nil,
		{Name: "", Transport: TransportStdio, Command: "npx", Enabled: true},
	})
	if _, ok := got["weird"]; ok {
		t.Errorf("unknown transport was rendered: %v", got["weird"])
	}
	if _, ok := got["fine"]; !ok {
		t.Error("the valid server was dropped")
	}
	if len(got) != 1 {
		t.Errorf("rendered %d entries, want 1 (nil and empty-name entries skipped): %v", len(got), got)
	}
}

// TestPairsRecord_LastDuplicateWins states the flattening loss explicitly: the
// store keeps ordered KeyPairs so the editor can round-trip duplicates, and
// KAS's schema cannot represent them.
func TestPairsRecord_LastDuplicateWins(t *testing.T) {
	got := pairsRecord([]KeyPair{{Name: "K", Value: "first"}, {Name: "K", Value: "second"}})
	if len(got) != 1 || got["K"] != "second" {
		t.Errorf("pairsRecord = %v, want {K: second}", got)
	}
	if pairsRecord(nil) != nil {
		t.Error("pairsRecord(nil) should stay nil so the field is omitted")
	}
}
