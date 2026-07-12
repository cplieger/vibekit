package settings

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func TestDefaultSettings_OnlyKnownKeys(t *testing.T) {
	// Every key DefaultSettings emits must be in KnownKeys; otherwise
	// a fresh GET response would itself trigger the unknown-keys
	// warning when the frontend round-trips back as a PATCH.
	for k := range DefaultSettings() {
		if _, ok := KnownKeys[k]; !ok {
			t.Errorf("DefaultSettings emits %q but it is not in KnownKeys", k)
		}
	}
}

func TestWarnUnknownKeys(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{
			name: "all known returns nil",
			keys: []string{"debug_logs", "shell_policy", "supervised_default"},
			want: nil,
		},
		{
			name: "single unknown surfaces",
			keys: []string{"debug_logs", "typo_key"},
			want: []string{"typo_key"},
		},
		{
			name: "multiple unknowns preserve input order",
			keys: []string{"debug_logs", "zzz_typo", "aaa_typo"},
			want: []string{"zzz_typo", "aaa_typo"},
		},
		{
			name: "all unknown",
			keys: []string{"foo", "bar"},
			want: []string{"foo", "bar"},
		},
		{
			name: "empty input returns nil",
			keys: nil,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WarnUnknownKeys(tc.keys, "test")
			if !slices.Equal(got, tc.want) {
				t.Errorf("WarnUnknownKeys(%v) = %v, want %v", tc.keys, got, tc.want)
			}
		})
	}
}

func TestKnownKeys_CoversFrontendSettings(t *testing.T) {
	// Sanity check: the union of KnownKeys must include every key
	// the frontend's AppSettings interface declares (per
	// static-src/persist.ts). This test fails loudly when one side
	// adds a key without the other; the warn-only PATCH path means
	// drift would otherwise be silent in production.
	frontendKeys := []string{
		"last_model",
		"notifications_enabled",
		"notify_agent_finished",
		"notify_permission",
		"shell_policy",
		"agent_ignore_files",
		"debug_logs",
		"supervised_default",
		"model_effort",
	}
	for _, k := range frontendKeys {
		if _, ok := KnownKeys[k]; !ok {
			t.Errorf("KnownKeys missing %q (declared in static-src/persist.ts AppSettings)", k)
		}
	}
}

// TestDefaultAgentIgnoreFiles_SeededAndDiscoverable pins the settled
// "agent read filter ON by default" decision: the seeded ignore-file list is
// non-empty and includes .gitignore + .kiroignore, DefaultSettings advertises
// the key (so a fresh GET carries it), and each returned slice is a fresh copy
// callers can mutate without corrupting the shared default.
func TestDefaultAgentIgnoreFiles_SeededAndDiscoverable(t *testing.T) {
	got := DefaultAgentIgnoreFiles()
	if len(got) == 0 {
		t.Fatal("DefaultAgentIgnoreFiles() is empty; a fresh install would not filter agent reads")
	}
	if !slices.Contains(got, ".gitignore") || !slices.Contains(got, ".kiroignore") {
		t.Errorf("DefaultAgentIgnoreFiles() = %v, want it to include .gitignore and .kiroignore", got)
	}

	// DefaultSettings must advertise the key so the GET-when-missing wire
	// shape carries the default (and the frontend can display it).
	def := DefaultSettings()
	raw, ok := def[KeyAgentIgnoreFiles]
	if !ok {
		t.Fatalf("DefaultSettings() missing %q; fresh GET would not surface the default", KeyAgentIgnoreFiles)
	}
	list, ok := raw.([]string)
	if !ok || !slices.Equal(list, DefaultAgentIgnoreFiles()) {
		t.Errorf("DefaultSettings()[%q] = %v (%T), want %v", KeyAgentIgnoreFiles, raw, raw, DefaultAgentIgnoreFiles())
	}

	// Fresh copy: mutating the returned slice must not affect the next call.
	got[0] = "MUTATED"
	if again := DefaultAgentIgnoreFiles(); again[0] == "MUTATED" {
		t.Error("DefaultAgentIgnoreFiles() returned a shared slice; callers can corrupt the default")
	}
}

// captureSlog installs a Debug-level slog handler writing to a buffer and
// restores the previous default logger on cleanup.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestWarnUnknownKeys_LogsOnlyWhenUnknownPresent pins the side-effect the
// return-value table test can't see: WarnUnknownKeys emits a single slog.Warn
// iff at least one key is unknown, and stays silent when every key is known.
// The warn is the operator-facing signal for config drift.
func TestWarnUnknownKeys_LogsOnlyWhenUnknownPresent(t *testing.T) {
	const msg = "settings: unknown keys in write"

	t.Run("all_known_no_warn", func(t *testing.T) {
		buf := captureSlog(t)
		got := WarnUnknownKeys([]string{KeyShellPolicy, KeyDebugLogs}, "test-src")
		if len(got) != 0 {
			t.Errorf("WarnUnknownKeys(all known) = %v, want empty", got)
		}
		if strings.Contains(buf.String(), msg) {
			t.Errorf("warned with zero unknown keys; log=%q", buf.String())
		}
	})

	t.Run("one_unknown_warns", func(t *testing.T) {
		buf := captureSlog(t)
		got := WarnUnknownKeys([]string{"bogus_key"}, "test-src")
		if len(got) != 1 || got[0] != "bogus_key" {
			t.Errorf("WarnUnknownKeys(one unknown) = %v, want [bogus_key]", got)
		}
		if !strings.Contains(buf.String(), msg) {
			t.Errorf("did not warn for an unknown key; log=%q", buf.String())
		}
	})
}
