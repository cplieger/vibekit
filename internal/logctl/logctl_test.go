package logctl

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/settings"
)

// snapshotLevel returns the currently-active slog level on the package's
// shared levelVar. Package-internal access is intentional: it is the only
// observable signal of Install/SetDebug, so the test lives in-package.
func snapshotLevel() slog.Level { return levelVar.Level() }

// restoreDefaultLogger snapshots slog.Default at entry and restores it at
// cleanup so Install's slog.SetDefault side-effect does not leak across
// tests or into other packages in the same test binary.
func restoreDefaultLogger(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
}

func writeSettings(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}
}

// readDebugFlag is a test helper that exercises the same code path as
// Install: settings.Field[bool] for the "debug_logs" key. Returns
// (value, ok) matching the settings.Field contract.
// context.Background() rather than t.Context(): no *testing.T is in scope here.
func readDebugFlag(dir string) (bool, bool) {
	return settings.Field[bool](context.Background(), dir, "debug_logs")
}

func TestReadDebugFlag(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, dir string) // optional custom setup instead of writeSettings
		content string                         // written via writeSettings when setup is nil
		want    bool
		wantOK  bool
	}{
		{
			name:   "MissingFile_ReturnsFalseNoError",
			setup:  func(*testing.T, string) {}, // no file written
			want:   false,
			wantOK: false,
		},
		{
			name:    "CorruptJSON_ReturnsFalse",
			content: "{not json",
			want:    false,
			wantOK:  false,
		},
		{
			name:    "MissingKey_ReturnsFalse",
			content: `{"other_key":true}`,
			want:    false,
			wantOK:  false,
		},
		{
			name:    "WrongType_ReturnsFalse",
			content: `{"debug_logs":"yes"}`,
			want:    false,
			wantOK:  false,
		},
		{
			name:    "ValidTrue_ReturnsTrue",
			content: `{"debug_logs":true}`,
			want:    true,
			wantOK:  true,
		},
		{
			name:    "ValidFalse_ReturnsFalse",
			content: `{"debug_logs":false}`,
			want:    false,
			wantOK:  true,
		},
		{
			name:    "ExtraKeys_IgnoredAndReturnsDebugLogsValue",
			content: `{"foo":1,"debug_logs":true,"bar":"x"}`,
			want:    true,
			wantOK:  true,
		},
		{
			name: "OpenErrorNotENOENT_ReturnsFalse",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "config.json"), 0o755); err != nil {
					t.Fatalf("mkdir config.json dir: %v", err)
				}
			},
			want:   false,
			wantOK: false,
		},
		{
			name: "OversizeSettings_ReturnsFalse",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				big := strings.Repeat("x", (1<<20)+16)
				writeSettings(t, dir, `{"debug_logs":true,"pad":"`+big+`"}`)
			},
			want:   false,
			wantOK: false,
		},
		{
			name: "ExactlyAtCap_IsAccepted",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				const cap = 1 << 20
				prefix := `{"debug_logs":true,"pad":"`
				suffix := `"}`
				padLen := cap - len(prefix) - len(suffix)
				if padLen < 0 {
					t.Fatalf("test setup: prefix+suffix longer than cap")
				}
				payload := prefix + strings.Repeat("x", padLen) + suffix
				if len(payload) != cap {
					t.Fatalf("test setup: payload len = %d, want %d", len(payload), cap)
				}
				writeSettings(t, dir, payload)
			},
			want:   true,
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.setup != nil {
				tc.setup(t, dir)
			} else if tc.content != "" {
				writeSettings(t, dir, tc.content)
			}

			got, ok := readDebugFlag(dir)

			if ok != tc.wantOK {
				t.Errorf("readDebugFlag ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("readDebugFlag = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstall_WithDebugTrue_SetsDebugLevel(t *testing.T) {
	restoreDefaultLogger(t)
	dir := t.TempDir()
	writeSettings(t, dir, `{"debug_logs":true}`)

	Install(t.Context(), dir)

	if got := snapshotLevel(); got != slog.LevelDebug {
		t.Errorf("after Install(context.Background(), debug=true) level = %v, want %v", got, slog.LevelDebug)
	}
}

func TestInstall_WithDebugFalse_SetsInfoLevel(t *testing.T) {
	restoreDefaultLogger(t)
	dir := t.TempDir()
	writeSettings(t, dir, `{"debug_logs":false}`)

	Install(t.Context(), dir)

	if got := snapshotLevel(); got != slog.LevelInfo {
		t.Errorf("after Install(context.Background(), debug=false) level = %v, want %v", got, slog.LevelInfo)
	}
}

func TestInstall_WithMissingSettings_DefaultsToInfo(t *testing.T) {
	restoreDefaultLogger(t)
	dir := t.TempDir() // no config.json written

	Install(t.Context(), dir)

	if got := snapshotLevel(); got != slog.LevelInfo {
		t.Errorf("after Install(context.Background(), no config.json) level = %v, want %v (must not silently enable debug)", got, slog.LevelInfo)
	}
}

func TestInstall_WithCorruptSettings_DefaultsToInfo(t *testing.T) {
	restoreDefaultLogger(t)
	dir := t.TempDir()
	writeSettings(t, dir, "{not json")

	Install(t.Context(), dir)

	if got := snapshotLevel(); got != slog.LevelInfo {
		t.Errorf("after Install(context.Background(), corrupt config.json) level = %v, want %v (must fail safe)", got, slog.LevelInfo)
	}
}

func TestInstall_ReplacesSlogDefault(t *testing.T) {
	restoreDefaultLogger(t)
	prev := slog.Default()
	dir := t.TempDir()

	Install(t.Context(), dir)

	if slog.Default() == prev {
		t.Error("Install did not replace slog.Default; runtime level toggling will not work")
	}
}

func TestSetDebug_On_SwitchesToDebug(t *testing.T) {
	restoreDefaultLogger(t)
	dir := t.TempDir()
	writeSettings(t, dir, `{"debug_logs":false}`)
	Install(t.Context(), dir)
	if snapshotLevel() != slog.LevelInfo {
		t.Fatalf("precondition: expected Info before SetDebug(true)")
	}

	SetDebug(true)

	if got := snapshotLevel(); got != slog.LevelDebug {
		t.Errorf("after SetDebug(true) level = %v, want %v", got, slog.LevelDebug)
	}
}

func TestSetDebug_Off_SwitchesToInfo(t *testing.T) {
	restoreDefaultLogger(t)
	dir := t.TempDir()
	writeSettings(t, dir, `{"debug_logs":true}`)
	Install(t.Context(), dir)
	if snapshotLevel() != slog.LevelDebug {
		t.Fatalf("precondition: expected Debug before SetDebug(false)")
	}

	SetDebug(false)

	if got := snapshotLevel(); got != slog.LevelInfo {
		t.Errorf("after SetDebug(false) level = %v, want %v", got, slog.LevelInfo)
	}
}

func TestSetDebug_TogglesIdempotent(t *testing.T) {
	// Invariant: calling SetDebug with the same value twice leaves the
	// level in the same state (no drift from repeated calls).
	restoreDefaultLogger(t)
	Install(t.Context(), t.TempDir())

	SetDebug(true)
	SetDebug(true)
	if got := snapshotLevel(); got != slog.LevelDebug {
		t.Errorf("after SetDebug(true) twice level = %v, want %v", got, slog.LevelDebug)
	}

	SetDebug(false)
	SetDebug(false)
	if got := snapshotLevel(); got != slog.LevelInfo {
		t.Errorf("after SetDebug(false) twice level = %v, want %v", got, slog.LevelInfo)
	}
}
