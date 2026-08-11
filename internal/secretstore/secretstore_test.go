package secretstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realKey is a key in the exact shape KAS derives, so the tests exercise the
// same lengths and characters the wire carries. The hash is the probe-confirmed
// sha256("http://127.0.0.1:46877/mcp" + "|").
const realKey = "kiro.mcp.2a0a3d1d4672ffaff77fcbe95f21be210e2e444f1b152fb537773dd72a3ddf3a.client"

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	return s
}

// TestRoundTripAcrossProcesses is the task's own done-when clause: a credential
// stored by one process must be readable by the next one. This is the property
// that takes MCP OAuth from one DCR per bridge spawn to zero.
func TestRoundTripAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	first, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	blob := `{"client_id":"probe-client-1","client_secret":"s3cret"}`
	if err := first.Set(ctx, realKey, blob); err != nil {
		t.Fatalf("Set() error = %v, want nil", err)
	}

	// A second Store over the same directory stands in for the next process.
	second, err := New(dir)
	if err != nil {
		t.Fatalf("New() (second) error = %v", err)
	}
	got, ok := second.Get(realKey)
	if !ok {
		t.Fatal("Get() ok = false after a Set in a previous process, want true")
	}
	if got != blob {
		t.Errorf("Get() = %q, want %q (the store must return the exact bytes it was given)", got, blob)
	}
}

// TestFileIs0600 pins the permission. These are OAuth client secrets and
// refresh tokens; a group- or world-readable file is the finding.
func TestFileIs0600(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := s.Set(context.Background(), realKey, "v"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store perms = %#o, want 0600", perm)
	}
}

// TestLoadTightensLoosePerms covers a file written by an older build, or copied
// onto the volume by hand: opening it must narrow the mode rather than trust it.
func TestLoadTightensLoosePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	seeded := `{"secrets":{"k":"` + base64.StdEncoding.EncodeToString([]byte("v")) + `"}}`
	if err := os.WriteFile(path, []byte(seeded), 0o644); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store perms = %#o after load, want 0600 (load must tighten)", perm)
	}
	if v, ok := s.Get("k"); !ok || v != "v" {
		t.Errorf("Get() = (%q, %v), want (\"v\", true): tightening must not lose the contents", v, ok)
	}
}

// TestGetMissIsNotAnError pins the contract KAS relies on: an absent
// credential is "not registered yet", which is the first-run answer.
func TestGetMissIsNotAnError(t *testing.T) {
	s := newStore(t)
	if v, ok := s.Get("kiro.mcp.deadbeef.tokens"); ok || v != "" {
		t.Errorf("Get(absent) = (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := s.Set(ctx, realKey, "v"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := s.Delete(ctx, realKey); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
	if _, ok := s.Get(realKey); ok {
		t.Error("Get() ok = true after Delete, want false")
	}
	// Absent-key delete succeeds: the requested post-state already holds.
	if err := s.Delete(ctx, realKey); err != nil {
		t.Errorf("Delete(absent) error = %v, want nil", err)
	}
}

// TestDeleteAbsentDoesNotWrite pins that a no-op delete leaves the file alone.
// A store that rewrote on every miss would turn KAS's speculative deletes into
// disk churn.
func TestDeleteAbsentDoesNotWrite(t *testing.T) {
	s := newStore(t)
	if err := s.Delete(context.Background(), "never-stored"); err != nil {
		t.Fatalf("Delete(absent) error = %v", err)
	}
	if _, err := os.Stat(s.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat store err = %v, want ErrNotExist: an absent-key delete must not create the file", err)
	}
}

func TestBounds(t *testing.T) {
	ctx := context.Background()

	t.Run("value over limit", func(t *testing.T) {
		s := newStore(t)
		err := s.Set(ctx, realKey, strings.Repeat("x", MaxValueBytes+1))
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("Set(oversize value) error = %v, want ErrTooLarge", err)
		}
		if _, ok := s.Get(realKey); ok {
			t.Error("a rejected Set left the key present")
		}
	})

	t.Run("key over limit", func(t *testing.T) {
		s := newStore(t)
		if err := s.Set(ctx, strings.Repeat("k", MaxKeyBytes+1), "v"); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Set(oversize key) error = %v, want ErrTooLarge", err)
		}
	})

	t.Run("empty key", func(t *testing.T) {
		s := newStore(t)
		if err := s.Set(ctx, "", "v"); err == nil {
			t.Error("Set(\"\") error = nil, want an error")
		}
	})

	t.Run("entry count", func(t *testing.T) {
		s := newStore(t)
		for i := range MaxEntries {
			if err := s.Set(ctx, "k"+string(rune('a'+i%26))+strings.Repeat("z", i/26+1), "v"); err != nil {
				t.Fatalf("Set(#%d) error = %v", i, err)
			}
		}
		if got := s.count(); got != MaxEntries {
			t.Fatalf("count = %d, want %d", got, MaxEntries)
		}
		if err := s.Set(ctx, "one-too-many", "v"); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Set() past the entry cap error = %v, want ErrTooLarge", err)
		}
		// An OVERWRITE of an existing key must still be allowed at the cap:
		// KAS refreshes a token set in place, and refusing that would strand
		// a full store on stale credentials forever.
		existing, _ := firstKey(s)
		if err := s.Set(ctx, existing, "refreshed"); err != nil {
			t.Errorf("Set(existing key) at the cap error = %v, want nil", err)
		}
	})
}

// count reports how many keys the store holds. A test helper rather than an
// exported method: production never asks, and an exported accessor with only
// test callers is dead weight punused correctly flags.
func (s *Store) count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.secrets)
}

// firstKey returns any key the store holds. Only for the cap test.
func firstKey(s *Store) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k := range s.secrets {
		return k, true
	}
	return "", false
}

// TestCorruptStoreMovedAside covers the recovery posture: these credentials are
// re-derivable, so an unparseable file must not stop the store from opening.
func TestCorruptStoreMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt store: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() over a corrupt store error = %v, want nil (credentials are re-derivable)", err)
	}
	if got := s.count(); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("stat moved-aside file: %v, want it preserved", err)
	}
	// The store is usable afterwards.
	if err := s.Set(context.Background(), realKey, "v"); err != nil {
		t.Errorf("Set() after corrupt recovery error = %v, want nil", err)
	}
}

// TestPersistFailureRollsBack pins that the in-memory map never claims a
// durability the disk does not have. KAS rethrows a store failure, so a Set
// that reports success while the write failed would present as a credential
// that silently reads back empty on the next spawn.
//
// The persist is broken by putting a DIRECTORY at the store's own path, not by
// chmod'ing the parent unwritable. An unwritable directory is a DAC check, and
// uid 0 bypasses DAC — so under root (which is how this suite runs in the
// container) the write SUCCEEDED and the test failed on its own assertion,
// pinning nothing. rename(2) is the mechanism that cannot be bypassed: it
// refuses to replace an existing directory with a non-directory (EISDIR) for
// root and non-root alike, and atomicfile commits every write with a rename, so
// persistLocked fails at its rename phase for everyone. Gating the old shape on
// os.Geteuid() would have skipped the rollback logic in the one environment the
// suite usually runs in, which is close to not testing it at all.
func TestPersistFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx := context.Background()
	if err := s.Set(ctx, realKey, "original"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	// Replace the store file with a directory so the atomic rename cannot land.
	if err := os.Remove(s.path); err != nil {
		t.Fatalf("remove store file: %v", err)
	}
	if err := os.Mkdir(s.path, 0o700); err != nil {
		t.Fatalf("mkdir in place of the store file: %v", err)
	}

	if err := s.Set(ctx, realKey, "replacement"); err == nil {
		t.Fatal("Set() error = nil with a directory at the store path, want an error")
	}
	if v, _ := s.Get(realKey); v != "original" {
		t.Errorf("Get() = %q after a failed Set, want %q (the failed write must roll back)", v, "original")
	}

	// Same for a failed delete.
	if err := s.Delete(ctx, realKey); err == nil {
		t.Fatal("Delete() error = nil with a directory at the store path, want an error")
	}
	if v, ok := s.Get(realKey); !ok || v != "original" {
		t.Errorf("Get() = (%q, %v) after a failed Delete, want (%q, true)", v, ok, "original")
	}
}

// TestOnDiskShape pins the file format: a flat map of PLAINTEXT key →
// base64 value. Both halves are deliberate. The plaintext key is what an
// operator greps to answer "is this credential cached?", and a keyed map (rather
// than a file per key) is what keeps an attacker-adjacent key out of a path. The
// base64 value is what makes the round-trip byte-exact — see FuzzKeysAndValues.
func TestOnDiskShape(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := s.Set(context.Background(), realKey, "blob"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var f struct {
		Secrets map[string]string `json:"secrets"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal store: %v", err)
	}
	if len(f.Secrets) != 1 {
		t.Fatalf("len(secrets) = %d, want 1", len(f.Secrets))
	}
	want := base64.StdEncoding.EncodeToString([]byte("blob"))
	if f.Secrets[realKey] != want {
		t.Errorf("secrets[%q] = %q, want %q (base64 of the value)", realKey, f.Secrets[realKey], want)
	}
	// The value must NOT appear verbatim: that is the tell that the encoding
	// is actually applied rather than merely asserted above.
	if strings.Contains(string(data), `"blob"`) {
		t.Error("the raw value appears in the file; values must be base64-encoded")
	}
}

// TestUndecodableEntryDropped covers a hand-edited or truncated entry: it costs
// that one credential (KAS re-derives it), never the whole store.
func TestUndecodableEntryDropped(t *testing.T) {
	dir := t.TempDir()
	good := base64.StdEncoding.EncodeToString([]byte("keepme"))
	seeded := `{"secrets":{"good":"` + good + `","bad":"!!!not base64!!!"}}`
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if v, ok := s.Get("good"); !ok || v != "keepme" {
		t.Errorf("Get(good) = (%q, %v), want (\"keepme\", true)", v, ok)
	}
	if _, ok := s.Get("bad"); ok {
		t.Error("Get(bad) ok = true, want false: an undecodable entry must be dropped")
	}
}

// FuzzKeysAndValues checks the store against arbitrary keys and values: KAS
// derives the key from an MCP server URL the user supplies, so the key is
// untrusted input. Invariants: an accepted pair round-trips byte-for-byte, a
// rejected one leaves no trace, and no input escapes the store's own directory.
func FuzzKeysAndValues(f *testing.F) {
	f.Add(realKey, `{"client_id":"x"}`)
	f.Add("", "")
	f.Add("../../etc/passwd", "pwned")
	f.Add("kiro.mcp.../../../x.client", "traversal")
	f.Add("a/b/c", "slashes")
	f.Add("k\x00v", "nul")
	f.Add("ключ", "юникод")
	f.Add(strings.Repeat("k", MaxKeyBytes), "at the key limit")
	// Both halves of the round-trip bug this target found: a non-UTF-8 VALUE
	// (silently became U+FFFD before values were base64-encoded) and a
	// non-UTF-8 KEY (JSON object keys are sanitized the same way, so the entry
	// was written under a name it could never be found under; now rejected).
	f.Add(realKey, "\x9c")
	f.Add("\xfe", "0")

	f.Fuzz(func(t *testing.T, key, value string) {
		dir := t.TempDir()
		s, err := New(dir)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		ctx := context.Background()
		setErr := s.Set(ctx, key, value)
		if setErr != nil {
			if _, ok := s.Get(key); ok {
				t.Errorf("Set(%q) failed but the key is present", key)
			}
		} else {
			got, ok := s.Get(key)
			if !ok {
				t.Fatalf("Set(%q) succeeded but Get missed", key)
			}
			if got != value {
				t.Errorf("round-trip changed the value: got %q, want %q", got, value)
			}
			// Durability: a fresh Store over the same dir sees the same bytes.
			reopened, rErr := New(dir)
			if rErr != nil {
				t.Fatalf("New() (reopen) error = %v", rErr)
			}
			if v, ok2 := reopened.Get(key); !ok2 || v != value {
				t.Errorf("reopen lost the pair: got (%q, %v), want (%q, true)", v, ok2, value)
			}
		}

		// Whatever the key contained, the ONLY file the store may create is
		// its own. A key is never a path component; this is what a
		// file-per-key layout could not guarantee.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		for _, e := range entries {
			if e.Name() != fileName {
				t.Errorf("store created an unexpected entry %q from key %q", e.Name(), key)
			}
		}
	})
}
