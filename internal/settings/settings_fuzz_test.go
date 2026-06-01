package settings

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func FuzzSettingsField(f *testing.F) {
	// Seed corpus: valid JSON with various key types.
	f.Add([]byte(`{"foo":"bar"}`), "foo")
	f.Add([]byte(`{"enabled":true}`), "enabled")
	f.Add([]byte(`{"count":42}`), "count")
	f.Add([]byte(`{"nested":{"a":1}}`), "nested")
	f.Add([]byte(`{}`), "missing")
	f.Add([]byte(`null`), "key")
	f.Add([]byte(`"just a string"`), "key")
	f.Add([]byte(``), "key")
	f.Add([]byte(`{"":"empty key"}`), "")

	f.Fuzz(func(t *testing.T, data []byte, key string) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}

		// Reset cache for this dir so each fuzz input is fresh.
		globalCacheMu.Lock()
		delete(globalCaches, dir)
		globalCacheMu.Unlock()

		ctx := context.Background()

		// Must not panic regardless of input.
		Field[string](ctx, dir, key, "fuzz")
		Field[bool](ctx, dir, key, "fuzz")
		Field[int](ctx, dir, key, "fuzz")
		Field[[]string](ctx, dir, key, "fuzz")
	})
}

func FuzzSettingsReadBytes(f *testing.F) {
	// Seed corpus: edge cases for the read path.
	f.Add([]byte(``))                                 // empty file
	f.Add([]byte(`{}`))                               // valid empty JSON
	f.Add([]byte(`{"key": "value"}`))                 // valid JSON
	f.Add([]byte(`{"truncated`))                      // truncated JSON
	f.Add([]byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}) // binary content
	f.Add(make([]byte, 4096))                         // zeroed block

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}

		// Reset cache for this dir.
		globalCacheMu.Lock()
		delete(globalCaches, dir)
		globalCacheMu.Unlock()

		ctx := context.Background()

		// Must not panic regardless of content.
		got, err := ReadBytes(ctx, dir)
		if err != nil {
			return
		}

		// If read succeeds, data should round-trip (capped at MaxBytes).
		expected := data
		if len(expected) > MaxBytes {
			expected = expected[:MaxBytes]
		}
		if len(got) != len(expected) {
			t.Errorf("ReadBytes length mismatch: got %d, want %d", len(got), len(expected))
		}
	})
}
