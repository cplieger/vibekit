package settings

import (
	"bytes"
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

		ctx := t.Context()

		// Must not panic regardless of input, across representative target types.
		Field[bool](ctx, dir, key, "fuzz")
		Field[int](ctx, dir, key, "fuzz")
		Field[[]string](ctx, dir, key, "fuzz")

		// Cross-function consistency: Field[string] and FieldInto(&string) read
		// the same key through the same parse path, so they must agree on both
		// presence and value for every input.
		val, okField := Field[string](ctx, dir, key, "fuzz")
		var into string
		okInto := FieldInto(ctx, dir, key, "fuzz", &into)
		if okField != okInto {
			t.Errorf("Field/FieldInto presence disagree for key %q: %v vs %v", key, okField, okInto)
		}
		if okField && val != into {
			t.Errorf("Field/FieldInto value disagree for key %q: %q vs %q", key, val, into)
		}
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

		ctx := t.Context()

		// Must not panic regardless of content.
		got, err := ReadBytes(ctx, dir)
		if err != nil {
			return
		}

		// If read succeeds, content should round-trip exactly (capped at MaxBytes).
		expected := data
		if len(expected) > MaxBytes {
			expected = expected[:MaxBytes]
		}
		if !bytes.Equal(got, expected) {
			t.Errorf("ReadBytes content mismatch: got %d bytes, want %d", len(got), len(expected))
		}
	})
}
