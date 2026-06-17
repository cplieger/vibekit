package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gk_vibekit_u29_captureSlog installs a Debug-level slog handler writing to a
// buffer and restores the previous default logger on cleanup.
func gk_vibekit_u29_captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// Test_gk_vibekit_u29_WarnUnknownKeys kills both mutants on defaults.go:87
// (`len(unknown) > 0`):
//   - CONDITIONALS_BOUNDARY (`>= 0`): would warn even when there are zero
//     unknown keys.
//   - CONDITIONALS_NEGATION (`<= 0`): would warn ONLY when there are zero
//     unknown keys.
//
// "all known" (assert no warn) defeats both; "one unknown" (assert warn)
// additionally pins the negation.
func Test_gk_vibekit_u29_WarnUnknownKeys(t *testing.T) {
	const msg = "settings: unknown keys in write"

	t.Run("all_known_no_warn", func(t *testing.T) {
		buf := gk_vibekit_u29_captureSlog(t)
		got := WarnUnknownKeys([]string{KeyAutoUpdate, KeyDebugLogs}, "gk-src")
		if len(got) != 0 {
			t.Errorf("WarnUnknownKeys(all known) = %v, want empty", got)
		}
		if strings.Contains(buf.String(), msg) {
			t.Errorf("warned with zero unknown keys; log=%q", buf.String())
		}
	})

	t.Run("one_unknown_warns", func(t *testing.T) {
		buf := gk_vibekit_u29_captureSlog(t)
		got := WarnUnknownKeys([]string{"gk_vibekit_u29_bogus"}, "gk-src")
		if len(got) != 1 || got[0] != "gk_vibekit_u29_bogus" {
			t.Errorf("WarnUnknownKeys(one unknown) = %v, want [gk_vibekit_u29_bogus]", got)
		}
		if !strings.Contains(buf.String(), msg) {
			t.Errorf("did not warn for an unknown key; log=%q", buf.String())
		}
	})
}

// Test_gk_vibekit_u29_GenInvalidationAfterDeleteRecreate kills both
// INCREMENT_DECREMENT mutants on the gen counter:
//   - settings.go:90  (`c.gen++` in the file-missing reset branch)
//   - settings.go:126 (`c.gen++` after a successful read)
//
// Either `--` mutant makes the gen value collide across the
// read-A -> delete -> read-B sequence, so the parsedMap cache falsely hits and
// returns the STALE parse of A. The original returns B's value.
func Test_gk_vibekit_u29_GenInvalidationAfterDeleteRecreate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)

	if err := os.WriteFile(path, []byte(`{"k":"AAA"}`), 0o600); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if v, ok := Field[string](ctx, dir, "k", "gk"); !ok || v != "AAA" {
		t.Fatalf("read A = (%q,%v), want (AAA,true)", v, ok)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := Field[string](ctx, dir, "k", "gk"); ok {
		t.Fatalf("read after delete ok = true, want false")
	}
	if err := os.WriteFile(path, []byte(`{"k":"BBB"}`), 0o600); err != nil {
		t.Fatalf("write B: %v", err)
	}
	v, ok := Field[string](ctx, dir, "k", "gk")
	if !ok || v != "BBB" {
		t.Errorf("read after recreate = (%q,%v), want (BBB,true) — stale parse cache", v, ok)
	}
}

// Test_gk_vibekit_u29_CacheSizeCheck kills settings.go:99 CONDITIONALS_NEGATION
// (`info.Size() == cachedSize` -> `!=`). With mtime forced equal but the file
// size changed, the original treats it as a cache MISS and re-reads the new
// content, while the `!=` mutant treats it as a HIT and returns stale cached
// bytes. A fixed whole-second mtime avoids sub-second granularity issues.
func Test_gk_vibekit_u29_CacheSizeCheck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	fixed := time.Unix(1700000000, 0)

	const c1 = `{"k":"a"}`      // 9 bytes
	const c2 = `{"k":"abcdef"}` // 14 bytes

	if err := os.WriteFile(path, []byte(c1), 0o600); err != nil {
		t.Fatalf("write c1: %v", err)
	}
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatalf("chtimes c1: %v", err)
	}
	if d1, err := ReadBytes(ctx, dir); err != nil || string(d1) != c1 {
		t.Fatalf("first ReadBytes = (%q,%v), want (%q,nil)", d1, err, c1)
	}

	if err := os.WriteFile(path, []byte(c2), 0o600); err != nil {
		t.Fatalf("write c2: %v", err)
	}
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatalf("chtimes c2: %v", err)
	}
	d2, err := ReadBytes(ctx, dir)
	if err != nil {
		t.Fatalf("second ReadBytes err = %v", err)
	}
	if string(d2) != c2 {
		t.Errorf("second ReadBytes = %q, want %q (stale size-cache hit)", d2, c2)
	}
}

// Test_gk_vibekit_u29_ParsedMapCacheReuse kills both CONDITIONALS_NEGATION
// mutants on settings.go:213 — `c.parsed != nil` (col 14) and
// `c.parsedGen == c.gen` (col 36). On a second identical call the cached parsed
// map must be reused; both mutants break the cache-hit gate and force a
// re-parse. Reuse is detected by mutating the first returned map and observing
// the mutation on the second call (re-parse would not carry it).
func Test_gk_vibekit_u29_ParsedMapCacheReuse(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	if err := os.WriteFile(path, []byte(`{"x":"1"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	m1, err := parsedMap(ctx, dir)
	if err != nil || m1 == nil {
		t.Fatalf("first parsedMap = (%v,%v)", m1, err)
	}
	m1["gk_vibekit_u29_sentinel"] = json.RawMessage(`true`)

	m2, err := parsedMap(ctx, dir)
	if err != nil {
		t.Fatalf("second parsedMap err = %v", err)
	}
	if _, ok := m2["gk_vibekit_u29_sentinel"]; !ok {
		t.Error("parsedMap re-parsed instead of returning the cached map (cache-hit gate broken)")
	}
}
