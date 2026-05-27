package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestCreate_ValidStdio(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Create(context.Background(), &Server{
		Transport: TransportStdio,
		Name:      "github",
		Command:   "npx",
		Args:      []string{"-y", "@modelcontextprotocol/server-github"},
		Env:       []KeyPair{{Name: "GITHUB_TOKEN", Value: "ghp_abc"}},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID == "" {
		t.Error("expected ID assigned")
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Error("expected timestamps")
	}
	// List returns masked.
	list := s.List(context.Background())
	if len(list) != 1 {
		t.Fatalf("List len = %d", len(list))
	}
	if list[0].Env[0].Value != SecretMask {
		t.Errorf("secret not masked: %q", list[0].Env[0].Value)
	}
}

func TestCreate_NameConflict(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "foo", Command: "bash", Enabled: true,
	})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Case-insensitive conflict check.
	_, err = s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "FOO", Command: "bash", Enabled: true,
	})
	if err != ErrNameConflict {
		t.Errorf("expected ErrNameConflict, got %v", err)
	}
}

func TestUpdate_PreservesSecretsWithMask(t *testing.T) {
	s := newTestStore(t)
	orig, _ := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "gh", Command: "npx",
		Env:     []KeyPair{{Name: "TOKEN", Value: "real-secret"}},
		Enabled: true,
	})

	// PUT with the mask as the value — should preserve the stored secret.
	_, err := s.Update(context.Background(), orig.ID, &Server{
		Transport: TransportStdio, Name: "gh", Command: "npx",
		Env:     []KeyPair{{Name: "TOKEN", Value: SecretMask}},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	raw := s.EnabledRaw(context.Background())
	if len(raw) != 1 {
		t.Fatalf("expected 1 enabled, got %d", len(raw))
	}
	if raw[0].Env[0].Value != "real-secret" {
		t.Errorf("secret not preserved; got %q", raw[0].Env[0].Value)
	}
}

func TestUpdate_ReplacesSecretWithNewValue(t *testing.T) {
	s := newTestStore(t)
	orig, _ := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "gh", Command: "npx",
		Env:     []KeyPair{{Name: "TOKEN", Value: "old-secret"}},
		Enabled: true,
	})
	_, err := s.Update(context.Background(), orig.ID, &Server{
		Transport: TransportStdio, Name: "gh", Command: "npx",
		Env:     []KeyPair{{Name: "TOKEN", Value: "new-secret"}},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	raw := s.EnabledRaw(context.Background())
	if raw[0].Env[0].Value != "new-secret" {
		t.Errorf("secret not replaced; got %q", raw[0].Env[0].Value)
	}
}

func TestSetEnabled_TogglesAndPersists(t *testing.T) {
	s := newTestStore(t)
	orig, _ := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "x", Command: "bash", Enabled: true,
	})
	got, err := s.SetEnabled(context.Background(), orig.ID, false)
	if err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if got.Enabled {
		t.Error("expected disabled")
	}
	if len(s.EnabledRaw(context.Background())) != 0 {
		t.Error("disabled server leaked into EnabledRaw")
	}
	// Second set to same value is a true no-op: the idempotent branch
	// returns early before persist or timestamp updates.
	_, err = s.SetEnabled(context.Background(), orig.ID, false)
	if err != nil {
		t.Errorf("idempotent set: %v", err)
	}
}

func TestDelete_RemovesAndIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	orig, _ := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "x", Command: "bash",
	})
	if err := s.Delete(context.Background(), orig.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(s.List(context.Background())) != 0 {
		t.Error("server not removed")
	}
	// Second delete is a no-op.
	if err := s.Delete(context.Background(), orig.ID); err != nil {
		t.Errorf("idempotent delete: %v", err)
	}
}

func TestPersist_FileIs0600(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "x", Command: "bash",
	})
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestOnChangeFires(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	s, err := New(dir, func(context.Context) { calls.Add(1) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, _ = s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash"})
	waitForCounter(t, &calls, 1)
}

func TestLoad_ReadsExistingFile(t *testing.T) {
	dir := t.TempDir()
	data := `{
		"version": 1,
		"servers": [
			{"id":"abc","name":"x","transport":"stdio","command":"bash","enabled":true,
			 "env":[{"name":"K","value":"v"}], "created_at":1, "updated_at":1}
		]
	}`
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	list := s.List(context.Background())
	if len(list) != 1 || list[0].Name != "x" {
		t.Fatalf("unexpected list: %#v", list)
	}
	// Secret is masked.
	if list[0].Env[0].Value != SecretMask {
		t.Error("env not masked on load")
	}
	// Raw value is preserved on disk.
	raw := s.EnabledRaw(context.Background())
	if raw[0].Env[0].Value != "v" {
		t.Errorf("raw value lost; got %q", raw[0].Env[0].Value)
	}
}

func TestLoad_CorruptFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New should tolerate corrupt file, got: %v", err)
	}
	if len(s.List(context.Background())) != 0 {
		t.Error("corrupt file should load empty")
	}
}

func TestACPServers_EnabledOnly(t *testing.T) {
	s := newTestStore(t)
	a, _ := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "a", Command: "bash", Enabled: true,
	})
	_, _ = s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "b", Command: "bash", Enabled: false,
	})
	// Force enabled true on a via SetEnabled which is more idiomatic.
	_, _ = s.SetEnabled(context.Background(), a.ID, true)

	acp := s.ACPServers(context.Background())
	if len(acp) != 1 {
		t.Fatalf("expected 1 enabled server in ACP export, got %d", len(acp))
	}
	if acp[0]["name"] != "a" {
		t.Errorf("wrong server exported; got %v", acp[0]["name"])
	}
}

func TestACPServers_WireShape(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Create(context.Background(), &Server{
		Transport: TransportHTTP, Name: "linear", URL: "https://mcp.linear.app/sse",
		Headers: []KeyPair{{Name: "Authorization", Value: "Bearer x"}},
		Enabled: true,
	})
	acp := s.ACPServers(context.Background())
	if len(acp) != 1 {
		t.Fatalf("len = %d", len(acp))
	}
	// Serialise to JSON to verify the shape matches ACP's McpServerHttp
	// exactly: headers is an array of {name,value} objects, not a map.
	j, err := json.Marshal(acp[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s1 := string(j)
	for _, want := range []string{
		`"type":"http"`,
		`"name":"linear"`,
		`"url":"https://mcp.linear.app/sse"`,
		`"headers":[{"name":"Authorization","value":"Bearer x"}]`,
	} {
		if !strings.Contains(s1, want) {
			t.Errorf("missing %q in %s", want, s1)
		}
	}
}

func TestACPServers_StdioWireShape(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "gh", Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-github"},
		Env:     []KeyPair{{Name: "GITHUB_TOKEN", Value: "tok"}},
		Enabled: true,
	})
	acp := s.ACPServers(context.Background())
	j, _ := json.Marshal(acp[0])
	s1 := string(j)
	// env must be an array of {name,value}, not a JSON object.
	if !strings.Contains(s1, `"env":[{"name":"GITHUB_TOKEN","value":"tok"}]`) {
		t.Errorf("env not in array shape: %s", s1)
	}
	if !strings.Contains(s1, `"args":["-y","@modelcontextprotocol/server-github"]`) {
		t.Errorf("args not in expected shape: %s", s1)
	}
}

func TestACPServers_EmptyArraysNotNull(t *testing.T) {
	s := newTestStore(t)
	// stdio with no args and no env: wire must use [] for both, never null.
	_, _ = s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "x", Command: "/bin/sh",
		Enabled: true,
	})
	acp := s.ACPServers(context.Background())
	j, _ := json.Marshal(acp[0])
	s1 := string(j)
	if strings.Contains(s1, `"args":null`) || strings.Contains(s1, `"env":null`) {
		t.Errorf("args/env serialised as null: %s", s1)
	}
	if !strings.Contains(s1, `"args":[]`) || !strings.Contains(s1, `"env":[]`) {
		t.Errorf("args/env not [] when empty: %s", s1)
	}

	// http with no headers: same.
	_, _ = s.Create(context.Background(), &Server{
		Transport: TransportHTTP, Name: "y", URL: "https://example.com",
		Enabled: true,
	})
	acp = s.ACPServers(context.Background())
	for _, entry := range acp {
		if entry["name"] == "y" {
			j, _ := json.Marshal(entry)
			if !strings.Contains(string(j), `"headers":[]`) {
				t.Errorf("http headers not [] when empty: %s", j)
			}
		}
	}
}

func TestEnabledNames_DefensiveFilter(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "on", Command: "bash", Enabled: true})
	_, _ = s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "off", Command: "bash", Enabled: false})
	names := s.EnabledNames(context.Background())
	if _, ok := names["on"]; !ok {
		t.Error("on missing")
	}
	if _, ok := names["off"]; ok {
		t.Error("off present")
	}
}

func TestCreate_IDNotNameCollides(t *testing.T) {
	// Regression: the ID should be a generated random string, not the
	// user-supplied name. If it were the name, a user could create a
	// server named "status" or "registry" and collide with the
	// /api/mcp/status and /api/mcp/registry/search routes.
	s := newTestStore(t)
	got, err := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "status", Command: "bash",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID == "status" {
		t.Error("ID leaked the user-supplied name; would collide with /api/mcp/status")
	}
	if got.ID == "" {
		t.Error("ID empty")
	}
}

// waitForCounter polls an atomic counter up to 2s for the target value.
func waitForCounter(t *testing.T, c *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("counter never reached %d (last=%d)", want, c.Load())
}

// F1: SetOnChange replaces the callback so the later-wired consumer
// actually sees mutations. Critical for the circular-init ordering
// with PrewarmRunner described in the store godoc.
func TestSetOnChange_ReplacesCallback(t *testing.T) {
	dir := t.TempDir()
	var firstCalls, secondCalls atomic.Int32
	s, err := New(dir, func(context.Context) { firstCalls.Add(1) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// First mutation fires the original callback.
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash"}); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	waitForCounter(t, &firstCalls, 1)

	// Replace the callback.
	s.SetOnChange(func(context.Context) { secondCalls.Add(1) })

	// Second mutation fires only the replacement.
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "b", Command: "bash"}); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	waitForCounter(t, &secondCalls, 1)

	if got := firstCalls.Load(); got != 1 {
		t.Errorf("SetOnChange did not detach old callback: firstCalls = %d, want 1", got)
	}
}

func TestSetOnChange_NilIsNoop(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	s, err := New(dir, func(context.Context) { calls.Add(1) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s.SetOnChange(nil) // detach

	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No deterministic wait because we assert absence; give the
	// notifier goroutine a beat.
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Errorf("SetOnChange(nil) did not detach callback: calls = %d, want 0", got)
	}
}

// F3: Create and Update must propagate DisabledTools. Previously both
// silently dropped the field — ToACP emits it but the store never
// persisted it, so the deny list never reached kiro-cli.
func TestCreate_PersistsDisabledTools(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Create(context.Background(), &Server{
		Transport:     TransportStdio,
		Name:          "gh",
		Command:       "npx",
		DisabledTools: []string{"delete_repo"},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(got.DisabledTools) != 1 || got.DisabledTools[0] != "delete_repo" {
		t.Errorf("Create dropped DisabledTools: got %+v, want [delete_repo]", got.DisabledTools)
	}
	raw := s.EnabledRaw(context.Background())
	if len(raw) != 1 {
		t.Fatalf("EnabledRaw len = %d, want 1", len(raw))
	}
	if len(raw[0].DisabledTools) != 1 || raw[0].DisabledTools[0] != "delete_repo" {
		t.Errorf("EnabledRaw lost DisabledTools: got %+v", raw[0].DisabledTools)
	}
}

func TestUpdate_PersistsDisabledTools(t *testing.T) {
	s := newTestStore(t)
	orig, err := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "gh", Command: "npx", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Update(context.Background(), orig.ID, &Server{
		Transport:     TransportStdio,
		Name:          "gh",
		Command:       "npx",
		DisabledTools: []string{"delete_repo", "force_push"},
		Enabled:       true,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	raw := s.EnabledRaw(context.Background())
	if len(raw[0].DisabledTools) != 2 {
		t.Errorf("Update dropped DisabledTools: got %+v", raw[0].DisabledTools)
	}
}

// F4: Update error branches — lowest-covered function in the suite.
func TestUpdate_ReturnsErrNotFoundForUnknownID(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Update(context.Background(), "does-not-exist", &Server{
		Transport: TransportStdio, Name: "x", Command: "bash",
	})
	if err != ErrNotFound {
		t.Errorf("Update(unknown) = %v, want ErrNotFound", err)
	}
}

func TestUpdate_RejectsInvalidShape(t *testing.T) {
	s := newTestStore(t)
	orig, err := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "ok", Command: "bash", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Change to HTTP without URL — must fail Validate.
	if _, err := s.Update(context.Background(), orig.ID, &Server{
		Transport: TransportHTTP, Name: "ok", URL: "",
	}); err == nil {
		t.Errorf("Update(invalid) = nil, want validation error")
	}
	// Original record must be untouched on validation failure.
	raw := s.EnabledRaw(context.Background())
	if len(raw) != 1 || raw[0].Command != "bash" {
		t.Errorf("Update rollback failed on validation error: %+v", raw)
	}
}

func TestUpdate_RejectsRenameToExistingName(t *testing.T) {
	s := newTestStore(t)
	a, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "alpha", Command: "bash"})
	if err != nil {
		t.Fatalf("Create alpha: %v", err)
	}
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "beta", Command: "bash"}); err != nil {
		t.Fatalf("Create beta: %v", err)
	}
	// Rename alpha → BETA (case-insensitive collision).
	if _, err := s.Update(context.Background(), a.ID, &Server{
		Transport: TransportStdio, Name: "BETA", Command: "bash",
	}); err != ErrNameConflict {
		t.Errorf("Update rename-to-conflict = %v, want ErrNameConflict", err)
	}
}

func TestUpdate_RenameToOwnNameAllowed(t *testing.T) {
	// Boundary: a rename that keeps the same name must succeed (the
	// hasNameLocked check correctly ignores the target record via
	// ignoreID).
	s := newTestStore(t)
	a, _ := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "alpha", Command: "bash"})
	if _, err := s.Update(context.Background(), a.ID, &Server{
		Transport: TransportStdio, Name: "alpha", Command: "zsh",
	}); err != nil {
		t.Errorf("Update same-name rename = %v, want nil", err)
	}
}

// F5: not-found branches on Get / SetEnabled / Delete.
func TestGet_ReturnsNilForUnknownID(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash"})
	if got := s.Get(context.Background(), "does-not-exist"); got != nil {
		t.Errorf("Get(unknown) = %+v, want nil", got)
	}
}

func TestSetEnabled_ReturnsErrNotFoundForUnknownID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetEnabled(context.Background(), "does-not-exist", true); err != ErrNotFound {
		t.Errorf("SetEnabled(unknown) = %v, want ErrNotFound", err)
	}
}

func TestDelete_UnknownIDReturnsNoError(t *testing.T) {
	// Documented behaviour: "No-op if not found." Make it explicit.
	s := newTestStore(t)
	if err := s.Delete(context.Background(), "does-not-exist"); err != nil {
		t.Errorf("Delete(unknown) = %v, want nil", err)
	}
}

// F10: List and Get must return deep copies so callers can't mutate the store.
func TestList_ReturnsDeepCopy(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "a", Command: "bash",
		Args:    []string{"-c", "echo"},
		Env:     []KeyPair{{Name: "K", Value: "v"}},
		Enabled: true,
	})
	list := s.List(context.Background())
	if len(list) != 1 {
		t.Fatalf("List len = %d", len(list))
	}

	// Mutate the returned slice and underlying fields.
	list[0].Name = "mutated"
	list[0].Args[0] = "HIJACKED"
	list[0].Env[0].Name = "HIJACKED"

	// Re-read and verify the store is untouched.
	refresh := s.List(context.Background())
	if refresh[0].Name != "a" {
		t.Errorf("List returned shallow copy (Name mutated): %q", refresh[0].Name)
	}
	if refresh[0].Args[0] != "-c" {
		t.Errorf("List returned shallow Args: %q", refresh[0].Args[0])
	}
	if refresh[0].Env[0].Name != "K" {
		t.Errorf("List returned shallow Env: %q", refresh[0].Env[0].Name)
	}
}

func TestGet_ReturnsDeepCopy(t *testing.T) {
	s := newTestStore(t)
	orig, _ := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "a", Command: "bash",
		Env: []KeyPair{{Name: "K", Value: "v"}},
	})
	got := s.Get(context.Background(), orig.ID)
	if got == nil {
		t.Fatal("Get returned nil")
	}
	got.Env[0].Name = "HIJACKED"

	refresh := s.Get(context.Background(), orig.ID)
	if refresh.Env[0].Name != "K" {
		t.Errorf("Get returned shallow Env: %q", refresh.Env[0].Name)
	}
}

// Regression: load() must preserve the corrupt mcp.json aside so the
// next persist doesn't destroy the user's config. Ops-mcp-003.
func TestLoad_CorruptFilePreservedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New should tolerate corrupt file, got: %v", err)
	}
	if len(s.List(context.Background())) != 0 {
		t.Error("corrupt file should load empty")
	}
	// The original file should have been moved to a .corrupt.<ts> sibling.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("corrupt mcp.json should have been renamed aside, still at %s", path)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "mcp.json.corrupt.") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected mcp.json.corrupt.<ts> sibling, got: %v", entries)
	}
}

// Regression: load() must re-enforce 0600 when the file arrives with
// looser perms. Sec-u11c1-01.
func TestLoad_ReenforcesTightPermsOnDrift(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"servers":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := New(dir, nil); err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("load did not tighten perms: got %v, want 0600", info.Mode().Perm())
	}
}

// Regression: load() must preserve the non-nil []*Server{} invariant
// even when the on-disk file has a null "servers" field. Q9.
func TestLoad_NullServersPreservesNonNilInvariant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"servers":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// After load, the next persist should emit "servers":[], not "servers":null.
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "x", Command: "bash"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"servers":null`) {
		t.Errorf("persist emitted null servers: %s", data)
	}
}

// F3 (test-review u12c1): persist-failure rollback. Every mutator
// (Create / Update / SetEnabled / Delete) pairs an in-memory change
// with a persist call; if persist fails, the in-memory state must
// roll back so callers don't see ghost records. Provoke a failure by
// making the store dir read-only after construction.
func breakPersist(t *testing.T, s *Store) {
	t.Helper()
	if os.Geteuid() == 0 {
		// Root bypasses 0o500 on ext/btrfs, so the rollback path
		// can't be triggered this way. Skip rather than falsify.
		t.Skip("cannot deny writes as root; rollback branches remain uncovered")
	}
	dir := filepath.Dir(s.path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func TestCreate_RollsBackOnPersistFailure(t *testing.T) {
	s := newTestStore(t)
	// Seed one record so we can assert the count stays at 1.
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash"}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	breakPersist(t, s)

	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "b", Command: "bash"}); err == nil {
		t.Fatal("Create against read-only dir succeeded; expected persist error")
	}
	if got := len(s.List(context.Background())); got != 1 {
		t.Errorf("Create rollback failed: len(List) = %d, want 1", got)
	}
}

func TestUpdate_RollsBackOnPersistFailure(t *testing.T) {
	s := newTestStore(t)
	orig, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	breakPersist(t, s)

	if _, err := s.Update(context.Background(), orig.ID, &Server{
		Transport: TransportStdio, Name: "a", Command: "zsh", Enabled: true,
	}); err == nil {
		t.Fatal("Update succeeded on read-only dir")
	}
	// In-memory record must still show the original command.
	got := s.Get(context.Background(), orig.ID)
	if got == nil {
		t.Fatal("Get returned nil after rollback; record disappeared")
	}
	raw := s.EnabledRaw(context.Background())
	if len(raw) != 1 || raw[0].Command != "bash" {
		t.Errorf("Update rollback: EnabledRaw[0].Command = %q, want %q", raw[0].Command, "bash")
	}
}

func TestSetEnabled_RollsBackOnPersistFailure(t *testing.T) {
	s := newTestStore(t)
	orig, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	breakPersist(t, s)

	if _, err := s.SetEnabled(context.Background(), orig.ID, false); err == nil {
		t.Fatal("SetEnabled succeeded on read-only dir")
	}
	got := s.Get(context.Background(), orig.ID)
	if got == nil || !got.Enabled {
		t.Errorf("SetEnabled rollback failed: Get(%s).Enabled = %v, want true", orig.ID, got.Enabled)
	}
}

func TestDelete_RollsBackOnPersistFailure(t *testing.T) {
	s := newTestStore(t)
	orig, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	breakPersist(t, s)

	if err := s.Delete(context.Background(), orig.ID); err == nil {
		t.Fatal("Delete succeeded on read-only dir")
	}
	if s.Get(context.Background(), orig.ID) == nil {
		t.Error("Delete rollback failed: record disappeared despite persist error")
	}
}

// F6 (test-review u12c1): load() corrupt-file rename-failure fallback.
// When rename aside fails, load() must still return nil (store comes
// up empty) and must leave the corrupt file in place. Triggered by
// making the parent dir read-only so os.Rename returns EACCES.
func TestLoad_CorruptFileRenameFailureDoesNotError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses 0o500 so rename does not fail")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Deny writes on the parent dir after the corrupt file exists —
	// os.Rename needs write on the source dir's inode.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	s, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New must tolerate rename failure, got: %v", err)
	}
	if got := len(s.List(context.Background())); got != 0 {
		t.Errorf("corrupt file with failed rename still loaded %d servers, want 0", got)
	}
	// The corrupt file should remain (rename failed), not vanish.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("corrupt file disappeared despite rename failure: %v", err)
	}
}


// ---------------------------------------------------------------------------
// OAuth client ID (kiro-cli 2.3+): pre-registered client_id for HTTP MCP
// servers that don't support DCR.
// ---------------------------------------------------------------------------

func TestACPServers_HTTPWithOAuthClientID(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Create(context.Background(), &Server{
		Transport:     TransportHTTP,
		Name:          "slack",
		URL:           "https://slack.example/mcp",
		OAuthClientID: "abc123",
		Enabled:       true,
	})
	acp := s.ACPServers(context.Background())
	if len(acp) != 1 {
		t.Fatalf("len = %d", len(acp))
	}
	j, err := json.Marshal(acp[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(j)
	// Wire shape: nested object {oauth: {clientId: "..."}} per kiro-cli's
	// MCP server config schema.
	if !strings.Contains(got, `"oauth":{"clientId":"abc123"}`) {
		t.Errorf("expected nested oauth.clientId in wire shape, got %s", got)
	}
}

func TestACPServers_HTTPWithoutOAuthClientID_OmitsField(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Create(context.Background(), &Server{
		Transport: TransportHTTP, Name: "linear", URL: "https://linear.example/mcp",
		Enabled: true,
	})
	acp := s.ACPServers(context.Background())
	j, err := json.Marshal(acp[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(j)
	if strings.Contains(got, `"oauth"`) {
		t.Errorf("oauth field should be omitted when empty, got %s", got)
	}
}

func TestUpdate_PreservesOAuthClientID(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(context.Background(), &Server{
		Transport: TransportHTTP, Name: "slack", URL: "https://slack.example/mcp",
		OAuthClientID: "abc123", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Update keeps the OAuthClientID — it's a non-secret config value
	// that round-trips verbatim (no SecretMask treatment).
	updated, err := s.Update(context.Background(), created.ID, &Server{
		Transport: TransportHTTP, Name: "slack", URL: "https://slack.example/mcp",
		OAuthClientID: "abc123", Enabled: true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.OAuthClientID != "abc123" {
		t.Errorf("OAuthClientID = %q, want abc123", updated.OAuthClientID)
	}
}

func TestUpdate_ChangesOAuthClientID(t *testing.T) {
	s := newTestStore(t)
	created, _ := s.Create(context.Background(), &Server{
		Transport: TransportHTTP, Name: "slack", URL: "https://slack.example/mcp",
		OAuthClientID: "old-id", Enabled: true,
	})
	updated, err := s.Update(context.Background(), created.ID, &Server{
		Transport: TransportHTTP, Name: "slack", URL: "https://slack.example/mcp",
		OAuthClientID: "new-id", Enabled: true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.OAuthClientID != "new-id" {
		t.Errorf("OAuthClientID = %q, want new-id", updated.OAuthClientID)
	}
}

func TestCreate_RejectsOAuthClientIDOnStdio(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), &Server{
		Transport: TransportStdio, Name: "x", Command: "true",
		OAuthClientID: "should-not-work",
		Enabled:       true,
	})
	if err == nil {
		t.Fatal("expected validation error for OAuthClientID on stdio transport")
	}
	if !strings.Contains(err.Error(), "oauth_client_id") {
		t.Errorf("error should mention oauth_client_id, got: %v", err)
	}
}

func TestCreate_RejectsOversizedOAuthClientID(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), &Server{
		Transport:     TransportHTTP,
		Name:          "x",
		URL:           "https://example.com/mcp",
		OAuthClientID: strings.Repeat("a", 1024),
		Enabled:       true,
	})
	if err == nil {
		t.Fatal("expected error for oversized OAuthClientID")
	}
	if !strings.Contains(err.Error(), "oauth_client_id too long") {
		t.Errorf("error should mention size limit, got: %v", err)
	}
}

func TestCreate_RejectsControlCharsInOAuthClientID(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), &Server{
		Transport:     TransportHTTP,
		Name:          "x",
		URL:           "https://example.com/mcp",
		OAuthClientID: "abc\x00def",
		Enabled:       true,
	})
	if err == nil {
		t.Fatal("expected error for control char in OAuthClientID")
	}
}
