package settings

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadBytes_MissingFile(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)

	data, err := ReadBytes(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil data for missing file, got %d bytes", len(data))
	}
}

func TestReadBytes_EmptyConfigDir(t *testing.T) {
	data, err := ReadBytes(context.Background(), "")
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

	data, err := ReadBytes(context.Background(), dir)
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

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ReadBytes(ctx, dir)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestField_StringKey(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"name":"alice","age":30}`))

	val, ok := Field[string](context.Background(), dir, "name", "test")
	if !ok || val != "alice" {
		t.Fatalf("got (%q, %v), want (\"alice\", true)", val, ok)
	}
}

func TestField_BoolKey(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"enabled":true}`))

	val, ok := Field[bool](context.Background(), dir, "enabled", "test")
	if !ok || !val {
		t.Fatalf("got (%v, %v), want (true, true)", val, ok)
	}
}

func TestField_MissingKey(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"other":"value"}`))

	val, ok := Field[string](context.Background(), dir, "missing", "test")
	if ok {
		t.Fatalf("expected ok=false for missing key, got val=%q", val)
	}
}

func TestField_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`not json`))

	val, ok := Field[string](context.Background(), dir, "key", "test")
	if ok {
		t.Fatalf("expected ok=false for invalid JSON, got val=%q", val)
	}
}

func TestField_TypeMismatch(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"count":"not a number"}`))

	val, ok := Field[int](context.Background(), dir, "count", "test")
	if ok {
		t.Fatalf("expected ok=false for type mismatch, got val=%d", val)
	}
}

func TestFieldInto_Success(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"items":["a","b","c"]}`))

	var items []string
	ok := FieldInto(context.Background(), dir, "items", "test", &items)
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
	data1, _ := ReadBytes(context.Background(), dir)

	// Second read should return cached data.
	data2, _ := ReadBytes(context.Background(), dir)
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
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
}
