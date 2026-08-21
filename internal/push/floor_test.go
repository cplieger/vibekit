package push

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestPermissionKindHasNoSettingsKey is the structural half of the
// protected-approval floor: PushKindPermission is registered with an EMPTY
// settings key, which is what makes it unreachable from config.json rather than
// merely absent from the UI. Hiding the checkbox while leaving the key wired
// would leave the stall one hand-edit away.
//
// The behavioural half is TestPermissionKindSurvivesEveryConfig below, and the
// write path's half is server.TestSyncPushPreferences_permissionIsAFloor.
func TestPermissionKindHasNoSettingsKey(t *testing.T) {
	var found bool
	for _, kr := range kindRegistry {
		if kr.Kind != vibekit.PushKindPermission {
			continue
		}
		found = true
		if kr.SettingsKey != "" {
			t.Errorf("PushKindPermission is wired to settings key %q; the ask must have no off switch", kr.SettingsKey)
		}
		if !kr.DefaultOn {
			t.Error("PushKindPermission must default ON: an unanswered ask blocks the turn")
		}
	}
	if !found {
		t.Fatal("PushKindPermission is not in kindRegistry; a permission ask would notify nobody")
	}
	if _, known := settings.KnownKeys["notify_permission"]; known {
		t.Error("notify_permission is still a known settings key")
	}
}

// TestKeylessKindIsPermissionOnly pins the UNIQUENESS half of the convention.
// The test above says permission has no key; this says nothing else may, because
// an empty key is indistinguishable from a forgotten one — and a forgotten one
// produces a kind that is permanently on and unreachable from config.json, with
// no error anywhere.
func TestKeylessKindIsPermissionOnly(t *testing.T) {
	var keyless []vibekit.PushKind
	for _, kr := range kindRegistry {
		if kr.SettingsKey == "" {
			keyless = append(keyless, kr.Kind)
		}
	}
	if len(keyless) != 1 || keyless[0] != vibekit.PushKindPermission {
		t.Errorf("keyless kinds = %v, want exactly [%s]", keyless, vibekit.PushKindPermission)
	}
	// The live table has to satisfy the rule the validator enforces, or the
	// package would not have loaded.
	if err := validateKindRegistry(kindRegistry); err != nil {
		t.Errorf("live kindRegistry is invalid: %v", err)
	}
}

// TestValidateKindRegistry_RejectsAForgottenKey exercises the rejection itself.
// In production it is a panic at init, which no test can reach, so the rule
// lives in a function that takes its table.
func TestValidateKindRegistry_RejectsAForgottenKey(t *testing.T) {
	cases := map[string]struct {
		entries []KindPref
		wantErr string
	}{
		"the live table": {entries: kindRegistry},
		"another kind forgot its key": {
			entries: []KindPref{{vibekit.PushKindAgentFinished, "", true}},
			wantErr: "only the permission floor may omit one",
		},
		"the floor is not default-on": {
			entries: []KindPref{{vibekit.PushKindPermission, "", false}},
			wantErr: "must be DefaultOn",
		},
		"an unknown kind": {
			entries: []KindPref{{vibekit.PushKind("notify_smoke_signal"), "notify_smoke", true}},
			wantErr: "invalid PushKind",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateKindRegistry(tc.entries)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("validateKindRegistry = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateKindRegistry = nil, want an error naming %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestPermissionKindSurvivesEveryConfig drives loadPreferences over the
// config.json shapes that could plausibly carry a stale off switch and asserts
// the permission kind is on in every one. This is the assertion that would fail
// if the registry entry were ever re-wired to a key.
func TestPermissionKindSurvivesEveryConfig(t *testing.T) {
	configs := map[string]string{
		"NoFile":                  "",
		"Empty":                   `{}`,
		"ExplicitFalse":           `{"notify_permission":false}`,
		"FalseWithMasterOn":       `{"notifications_enabled":true,"notify_permission":false}`,
		"FalseWithEverythingElse": `{"notify_agent_finished":false,"notify_permission":false,"debug_logs":true}`,
		"WrongType":               `{"notify_permission":"off"}`,
		"Malformed":               `{"notify_permission":fals`,
	}
	for name, body := range configs {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if body != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
					t.Fatalf("write settings: %v", err)
				}
			}

			s := New(t.Context(), dir, "mailto:test@example.com")
			t.Cleanup(func() { s.Close() })

			s.mu.Lock()
			on := s.prefs[vibekit.PushKindPermission]
			s.mu.Unlock()
			if !on {
				t.Errorf("config %s silenced the permission ask; it is a floor, not a preference", body)
			}
		})
	}
}
