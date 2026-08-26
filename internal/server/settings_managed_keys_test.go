package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/settings"
)

// TestSettingsWrite_PUTCarriesOverAManagedKeyItOmits is the claim that a
// full-object PUT is non-destructive of the keys no client PUT body carries.
//
// The set grew when internal/uistate's whole-document arrangement was retired:
// `theme` and `fb_path` moved into config.json and both are written only by a
// PATCH — the theme toggle and the file browser's navigation. A PUT that replaced
// the file would therefore wipe them, and the THEME is the member where that is
// visible rather than merely wrong: the reader sees the wrong colour on the next
// load, which is the same reason it is the one value the uistate deletion carries
// across at all.
//
// Driven through the real handler rather than by calling ServerManagedKeys and
// comparing lists, because the list is not the behaviour: the carry-over loop is,
// and a list pinned by a test that never exercises the loop passes with the loop
// deleted.
func TestSettingsWrite_PUTCarriesOverAManagedKeyItOmits(t *testing.T) {
	tests := []struct {
		desc      string
		persisted string
		body      string
		want      map[string]string
	}{
		{
			desc:      "a PUT omitting the theme keeps it",
			persisted: `{"theme":"light","last_model":"old"}`,
			body:      `{"last_model":"new"}`,
			// last_model is NOT managed, so the PUT replaces it; theme is, so it
			// survives. Both directions in one case, or a handler that carried
			// everything over would pass.
			want: map[string]string{"theme": "light", "last_model": "new"},
		},
		{
			desc:      "a PUT omitting the browser path keeps it",
			persisted: `{"fb_path":"/workspace/src"}`,
			body:      `{"last_model":"new"}`,
			want:      map[string]string{"fb_path": "/workspace/src", "last_model": "new"},
		},
		{
			desc:      "a PUT that names a managed key overrides it",
			persisted: `{"theme":"light"}`,
			body:      `{"theme":"dark"}`,
			// The carry-over is for an OMITTED key only. A body that states the
			// value is the freshest layer, or the toggle could never turn a theme off.
			want: map[string]string{"theme": "dark"},
		},
		{
			desc:      "the ignore list is managed too, and stays so",
			persisted: `{"agent_ignore_files":[".gitignore"],"theme":"dark"}`,
			body:      `{"last_model":"new"}`,
			want:      map[string]string{"theme": "dark"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, settings.Filename)
			if err := os.WriteFile(path, []byte(tc.persisted), 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}

			req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader([]byte(tc.body)))
			rec := httptest.NewRecorder()
			s.handleSettingsWrite(rec, req, path)

			if rec.Code != http.StatusOK {
				t.Fatalf("PUT /api/settings = %d, want %d", rec.Code, http.StatusOK)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back %s: %v", path, err)
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("after PUT, %s = %v, want %q", k, got[k], want)
				}
			}
			// The ignore list rides the same loop and is compared separately: it is
			// an array, so it cannot go in the string table above.
			if tc.desc == "the ignore list is managed too, and stays so" {
				list, ok := got[settings.KeyAgentIgnoreFiles].([]any)
				if !ok || len(list) != 1 || list[0] != ".gitignore" {
					t.Errorf("after PUT, %s = %v, want [.gitignore]", settings.KeyAgentIgnoreFiles, got[settings.KeyAgentIgnoreFiles])
				}
			}
		})
	}
}
