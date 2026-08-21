package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestSyncPushPreferences_SparsePatchDoesNotResetAnOmittedKind is the claim that
// syncPushPreferences is correct on its OWN terms rather than because of what its
// caller happens to pass.
//
// The function seeds every kind to its registry default and then overrides, so an
// argument that is the whole merged settings document behaves correctly and a
// genuinely sparse one used to re-enable every kind it did not mention. The
// asymmetry is what makes it worth a test: re-enabling a notification the user
// switched OFF is a preference silently reverting itself, while the other
// direction merely delays a toggle the next reload would apply anyway.
//
// Both directions are asserted for both configurable kinds, so a fix that
// happened to pin one value (or one key) cannot pass.
func TestSyncPushPreferences_SparsePatchDoesNotResetAnOmittedKind(t *testing.T) {
	const (
		finished = settings.KeyNotifyAgentFinished
		prStatus = settings.KeyNotifyPRStatus
	)
	tests := map[string]struct {
		persisted    string // config.json contents; "" writes no file at all
		patch        string
		wantFinished bool
		wantPR       bool
		why          string
	}{
		"omitted key keeps a disabled preference": {
			persisted:    `{"notify_agent_finished":false,"notify_pr_status":false}`,
			patch:        `{"debug_logs":true}`,
			wantFinished: false,
			wantPR:       false,
			why:          "a patch naming neither kind must not turn either back on",
		},
		"omitted key keeps an enabled preference": {
			persisted:    `{"notify_agent_finished":true,"notify_pr_status":true}`,
			patch:        `{"debug_logs":true}`,
			wantFinished: true,
			wantPR:       true,
			why:          "the other direction: an untouched enabled kind stays enabled",
		},
		"a present key wins over the persisted value": {
			persisted:    `{"notify_agent_finished":false,"notify_pr_status":false}`,
			patch:        `{"notify_agent_finished":true}`,
			wantFinished: true,
			wantPR:       false,
			why:          "the patch is the freshest layer, and it moves only the kind it names",
		},
		"a present key can disable while its sibling is untouched": {
			persisted:    `{"notify_agent_finished":true,"notify_pr_status":true}`,
			patch:        `{"notify_pr_status":false}`,
			wantFinished: true,
			wantPR:       false,
			why:          "one toggle is one kind; the sibling reads from disk, not from the default",
		},
		"no settings file falls back to the registry defaults": {
			persisted:    "",
			patch:        `{}`,
			wantFinished: true,
			wantPR:       true,
			why:          "nothing on disk and nothing in the patch leaves the registry's DefaultOn",
		},
		"an unparseable settings file falls back to the registry defaults": {
			persisted:    `{not json`,
			patch:        `{}`,
			wantFinished: true,
			wantPR:       true,
			why:          "readExistingSettings yields an empty map on a corrupt file rather than failing the write",
		},
		"a malformed patch value falls back to the registry default": {
			persisted:    `{"notify_agent_finished":false}`,
			patch:        `{"notify_agent_finished":"nonsense"}`,
			wantFinished: true,
			wantPR:       true,
			// The key IS present, so this is not the sparse case. The write path
			// persists the patch verbatim, so the junk value is now on disk too and
			// the next ReloadPreferences resolves it to the same default — pinning
			// the default here is what keeps the two paths agreeing.
			why: "a present-but-unreadable value is not an absent one; it resolves the way a reload would",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.persisted != "" {
				path := filepath.Join(dir, settings.Filename)
				if err := os.WriteFile(path, []byte(tc.persisted), 0o600); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			}
			var patch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.patch), &patch); err != nil {
				t.Fatalf("unmarshal patch %s: %v", tc.patch, err)
			}
			mp := &testPush{}
			s := &Server{push: mp, configDir: dir}

			s.syncPushPreferences(patch)

			if got := mp.prefs[vibekit.PushKindAgentFinished]; got != tc.wantFinished {
				t.Errorf("prefs[%s] = %v, want %v (%s)", finished, got, tc.wantFinished, tc.why)
			}
			if got := mp.prefs[vibekit.PushKindPRStatus]; got != tc.wantPR {
				t.Errorf("prefs[%s] = %v, want %v (%s)", prStatus, got, tc.wantPR, tc.why)
			}
			// The floor rides every case: no resolution path may lower it.
			if !mp.prefs[vibekit.PushKindPermission] {
				t.Errorf("prefs[Permission] = false, want true (the ask is a floor, %s)", tc.why)
			}
		})
	}
}

// TestSyncPushPreferences_MergedPatchNeedsNoDiskRead pins the fast path: when the
// caller passes the whole merged document, every configurable key is present and
// the persisted file is never consulted. Asserted by writing a config.json that
// DISAGREES with the patch — if the resolution read it for a key the patch
// already answered, the disk value would win and the assertions would flip.
func TestSyncPushPreferences_MergedPatchNeedsNoDiskRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, settings.Filename)
	if err := os.WriteFile(path, []byte(`{"notify_agent_finished":true,"notify_pr_status":true}`), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	mp := &testPush{}
	s := &Server{push: mp, configDir: dir}

	s.syncPushPreferences(map[string]json.RawMessage{
		settings.KeyNotifyAgentFinished: json.RawMessage(`false`),
		settings.KeyNotifyPRStatus:      json.RawMessage(`false`),
	})

	if mp.prefs[vibekit.PushKindAgentFinished] || mp.prefs[vibekit.PushKindPRStatus] {
		t.Errorf("prefs = %+v, want both false: a key the patch carries must not be re-read from disk", mp.prefs)
	}
}
