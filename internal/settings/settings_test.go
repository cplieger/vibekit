package settings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
)

func TestReadBytes_MissingFile(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)

	data, err := readBytes(t.Context(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil data for missing file, got %d bytes", len(data))
	}
}

func TestReadBytes_EmptyConfigDir(t *testing.T) {
	data, err := readBytes(t.Context(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Fatal("expected nil data for empty configDir")
	}
}

func TestReadBytes_ValidFile(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	content := []byte(`{"key":"value"}`)
	writeSettings(t, dir, content)

	data, err := readBytes(t.Context(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("got %q, want %q", data, content)
	}
}

func TestReadBytes_CancelledContext(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{}`))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := readBytes(ctx, dir)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestField_StringKey(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"name":"alice","age":30}`))

	val, ok := Field[string](t.Context(), dir, "name")
	if !ok || val != "alice" {
		t.Fatalf("got (%q, %v), want (\"alice\", true)", val, ok)
	}
}

func TestField_BoolKey(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"enabled":true}`))

	val, ok := Field[bool](t.Context(), dir, "enabled")
	if !ok || !val {
		t.Fatalf("got (%v, %v), want (true, true)", val, ok)
	}
}

func TestField_MissingKey(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"other":"value"}`))

	val, ok := Field[string](t.Context(), dir, "missing")
	if ok {
		t.Fatalf("expected ok=false for missing key, got val=%q", val)
	}
}

func TestField_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`not json`))

	val, ok := Field[string](t.Context(), dir, "key")
	if ok {
		t.Fatalf("expected ok=false for invalid JSON, got val=%q", val)
	}
}

func TestField_TypeMismatch(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"count":"not a number"}`))

	val, ok := Field[int](t.Context(), dir, "count")
	if ok {
		t.Fatalf("expected ok=false for type mismatch, got val=%d", val)
	}
}

func TestFieldInto_Success(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"items":["a","b","c"]}`))

	var items []string
	ok := FieldInto(t.Context(), dir, "items", &items)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(items) != 3 || items[0] != "a" {
		t.Fatalf("got %v, want [a b c]", items)
	}
}

func TestReadBytes_MtimeCache(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)

	writeSettings(t, dir, []byte(`{"v":1}`))
	data1, _ := readBytes(t.Context(), dir)

	// Second read should return cached data.
	data2, _ := readBytes(t.Context(), dir)
	if string(data1) != string(data2) {
		t.Fatalf("cache miss: got %q then %q", data1, data2)
	}
}

// helpers

func resetCache(t *testing.T, dir string) {
	t.Helper()
	globalCacheMu.Lock()
	delete(globalCaches, dir)
	globalCacheMu.Unlock()
}

func writeSettings(t *testing.T, dir string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestField_GenInvalidationAfterDeleteRecreate verifies the parsed-map cache
// is invalidated across a read -> delete -> recreate sequence: the second
// read must see the new file contents, not a stale cached parse.
func TestField_GenInvalidationAfterDeleteRecreate(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)

	if err := os.WriteFile(path, []byte(`{"k":"AAA"}`), 0o600); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if v, ok := Field[string](ctx, dir, "k"); !ok || v != "AAA" {
		t.Fatalf("read A = (%q,%v), want (AAA,true)", v, ok)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := Field[string](ctx, dir, "k"); ok {
		t.Fatalf("read after delete ok = true, want false")
	}
	if err := os.WriteFile(path, []byte(`{"k":"BBB"}`), 0o600); err != nil {
		t.Fatalf("write B: %v", err)
	}
	v, ok := Field[string](ctx, dir, "k")
	if !ok || v != "BBB" {
		t.Errorf("read after recreate = (%q,%v), want (BBB,true) — stale parse cache", v, ok)
	}
}

// TestReadBytes_SizeChangeBypassesMtimeCache verifies that a same-mtime but
// different-size config.json is treated as a cache MISS and re-read. The
// mtime is pinned to a fixed whole second so only the size differs.
func TestReadBytes_SizeChangeBypassesMtimeCache(t *testing.T) {
	ctx := t.Context()
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
	if d1, err := readBytes(ctx, dir); err != nil || string(d1) != c1 {
		t.Fatalf("first readBytes = (%q,%v), want (%q,nil)", d1, err, c1)
	}

	if err := os.WriteFile(path, []byte(c2), 0o600); err != nil {
		t.Fatalf("write c2: %v", err)
	}
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatalf("chtimes c2: %v", err)
	}
	d2, err := readBytes(ctx, dir)
	if err != nil {
		t.Fatalf("second readBytes err = %v", err)
	}
	if string(d2) != c2 {
		t.Errorf("second readBytes = %q, want %q (stale size-cache hit)", d2, c2)
	}
}

// TestReadBytes_EqualLengthRenamePublishIsAMiss is the os.SameFile leg's own
// case, and the one the (mtime, size) pair could not see: a second generation
// published by RENAME, of equal length, carrying the same mtime. Linux stamps
// inode times from a coarse clock, so two publishes inside one tick share an
// mtime to the nanosecond, and a backup restore or `rsync -t` reproduces it
// deliberately. The new inode is then the only difference, and a hit here pins
// the agent read filter and the retention window to bytes the operator replaced —
// for as long as nothing else happens to touch the file.
func TestReadBytes_EqualLengthRenamePublishIsAMiss(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	resetCache(t, dir)
	path := filepath.Join(dir, Filename)
	fixed := time.Unix(1700000000, 0)

	const v1 = `{"k":"aaa"}`
	const v2 = `{"k":"bbb"}` // same length, so only the inode differs

	publish := func(content string) {
		t.Helper()
		if _, err := atomicfile.WriteFile(ctx, path, []byte(content), atomicfile.WithMode(0o600)); err != nil {
			t.Fatalf("publish %s: %v", content, err)
		}
		if err := os.Chtimes(path, fixed, fixed); err != nil {
			t.Fatalf("chtimes %s: %v", content, err)
		}
	}

	publish(v1)
	if d1, err := readBytes(ctx, dir); err != nil || string(d1) != v1 {
		t.Fatalf("first readBytes = (%q,%v), want (%q,nil)", d1, err, v1)
	}
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat v1: %v", err)
	}

	publish(v2)
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat v2: %v", err)
	}
	// Without this the test could pass for the wrong reason: an in-place write or
	// a moved mtime would make the old two-leg check catch it too.
	if os.SameFile(info1, info2) {
		t.Fatalf("setup: the second publish reused the inode; this case needs a rename publish")
	}
	if info1.Size() != info2.Size() || !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("setup: generations differ in size (%d vs %d) or mtime (%v vs %v); only the inode may differ",
			info1.Size(), info2.Size(), info1.ModTime(), info2.ModTime())
	}

	d2, err := readBytes(ctx, dir)
	if err != nil {
		t.Fatalf("second readBytes err = %v", err)
	}
	if string(d2) != v2 {
		t.Errorf("readBytes after an equal-length rename publish = %q, want %q (stale cache hit)", d2, v2)
	}
}

// TestParsedMap_CacheReuse verifies parsedMap returns the cached map on a
// second identical call rather than re-parsing. Reuse is detected by mutating
// the first returned map and observing the mutation on the second call (a
// re-parse would not carry it).
func TestParsedMap_CacheReuse(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	if err := os.WriteFile(path, []byte(`{"x":"1"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	m1, err := parsedMap(ctx, dir)
	if err != nil || m1 == nil {
		t.Fatalf("first parsedMap = (%v,%v)", m1, err)
	}
	m1["sentinel"] = json.RawMessage(`true`)

	m2, err := parsedMap(ctx, dir)
	if err != nil {
		t.Fatalf("second parsedMap err = %v", err)
	}
	if _, ok := m2["sentinel"]; !ok {
		t.Error("parsedMap re-parsed instead of returning the cached map (cache-hit gate broken)")
	}
}
