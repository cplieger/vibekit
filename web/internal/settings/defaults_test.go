package settings

import (
	"slices"
	"testing"
)

func TestDefaultSettings_AlwaysIncludesAutoUpdate(t *testing.T) {
	// The frontend's first-run path expects `auto_update: true` so
	// the update banner stays visible until the user explicitly
	// dismisses it. Regressing this default would silently disable
	// the update prompt for fresh installs.
	d := DefaultSettings()
	v, ok := d["auto_update"].(bool)
	if !ok {
		t.Fatalf("auto_update missing or wrong type: %T", d["auto_update"])
	}
	if !v {
		t.Errorf("auto_update default = false, want true")
	}
}

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
			keys: []string{"auto_update", "permission_mode", "trust_tools"},
			want: nil,
		},
		{
			name: "single unknown surfaces",
			keys: []string{"auto_update", "typo_key"},
			want: []string{"typo_key"},
		},
		{
			name: "multiple unknowns preserve input order",
			keys: []string{"auto_update", "zzz_typo", "aaa_typo"},
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
		"auto_update",
		"last_model",
		"notifications_enabled",
		"notify_agent_finished",
		"notify_permission",
		"permission_mode",
		"trust_tools",
		"agent_ignore_files",
		"debug_logs",
		"supervised_default",
	}
	for _, k := range frontendKeys {
		if _, ok := KnownKeys[k]; !ok {
			t.Errorf("KnownKeys missing %q (declared in static-src/persist.ts AppSettings)", k)
		}
	}
}
