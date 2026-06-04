package logctl

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// FuzzInstall exercises the Install path with arbitrary config.json
// content. The invariant is: regardless of file content, Install must
// never panic, and the resulting level must be either Info or Debug.
func FuzzInstall(f *testing.F) {
	f.Add([]byte(`{"debug_logs":true}`))
	f.Add([]byte(`{"debug_logs":false}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"debug_logs":"yes"}`))
	f.Add([]byte(`{"debug_logs":null}`))
	f.Add([]byte{})
	f.Add([]byte(`{"debug_logs":1}`))

	f.Fuzz(func(t *testing.T, content []byte) {
		prev := slog.Default()
		t.Cleanup(func() { slog.SetDefault(prev) })

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), content, 0o600); err != nil {
			t.Fatal(err)
		}

		Install(context.Background(), dir)

		level := levelVar.Level()
		if level != slog.LevelInfo && level != slog.LevelDebug {
			t.Errorf("unexpected level %v after Install with content %q", level, content)
		}
	})
}
