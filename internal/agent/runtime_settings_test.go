package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// setKiroSettings lays down a kiro-cli-style settings file under a
// throwaway HOME so IsHookStatusEnabled can be exercised deterministically.
// Returns the parent HOME dir so callers can extend it if they need to.
func setKiroSettings(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".kiro", "settings")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if body != "" {
		// cli.json: the file `kiro-cli settings` actually persists to
		// (kiroSettingsPath reads it; settings.json doesn't exist on
		// current installs).
		if err := os.WriteFile(filepath.Join(dir, "cli.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	t.Setenv("HOME", home)
	// On some CI shells USERPROFILE can shadow HOME; overriding both
	// keeps os.UserHomeDir deterministic across runners.
	t.Setenv("USERPROFILE", home)
	return home
}

func TestIsHookStatusEnabled_fileMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	h, _, _ := newTestHub()
	if !h.hookStatus.IsHookStatusEnabled() {
		t.Error("missing cli.json: want default true")
	}
}

func TestIsHookStatusEnabled_cases(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"invalid_outer_json", "{not json", true},
		{"key_absent", `{}`, true},
		{"key_not_bool", `{"hooks.showStatus":"yes"}`, true},
		{"key_false", `{"hooks.showStatus":false}`, false},
		{"key_true", `{"hooks.showStatus":true}`, true},
		// json.Unmarshal of a JSON `null` onto *bool is a no-op per
		// encoding/json docs, so the zero value (false) is returned.
		// This is a kiro-cli-unreachable path (the setting CLI never
		// writes null) so either behaviour is defensible; pinning
		// the observed one here just freezes the current contract.
		{"key_null", `{"hooks.showStatus":null}`, false},
		{"other_keys_only", `{"telemetry.enabled":true}`, true},
		// Regression for the pre-fix bug: the old code read the
		// underscore form from vibekit's configDir, which meant
		// flipping the toggle did nothing. Assert the new key wins.
		{"snake_case_key_ignored", `{"hooks_show_status":false}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setKiroSettings(t, tc.content)
			h, _, _ := newTestHub()
			if got := h.hookStatus.IsHookStatusEnabled(); got != tc.want {
				t.Errorf("content %q: got %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}
