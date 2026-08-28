package server

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/settings"
)

// TestSettingsWrite_RefusesWhenTheStoredSettingsCannotBeRead is the claim that a
// write which cannot read what is already there does not write at all.
//
// Both arms of the one switch are covered, because they are eight lines apart and
// the whole point of the read returning an error is that neither can quietly keep
// the old reading. And both PATCH cases the file browser reaches are covered:
// navigating it PATCHes fb_path, so the destructive sequence needed no deliberate
// act from the user at all.
//
// If the read is reverted to answering "nothing is stored" for these files, every
// case fails on the SECOND assertion rather than the first: the request answers
// 200 and config.json comes back holding only the key the request carried, with
// the other keys gone from disk. The byte comparison is what catches it — a keys
// check alone would pass on a file the write left semantically equal.
func TestSettingsWrite_RefusesWhenTheStoredSettingsCannotBeRead(t *testing.T) {
	// Every key here is a real one a user can set, so the loss the refusal
	// prevents is the loss the report describes rather than a synthetic one.
	const stored = `{"chat_retention_days":-1,"security_profile":"trusted","theme":"dark"}`

	tests := []struct {
		desc   string
		method string
		// seed installs whatever stands at config.json and returns the bytes a
		// later read must still find, or "" when the subject is not a file whose
		// contents can be compared.
		seed func(t *testing.T, path string) string
	}{
		{
			desc:   "PATCH over an unparseable document",
			method: http.MethodPatch,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				// One hand-edit away from the real thing: a trailing comma is the
				// shape invariant 6 says the operator produces on this volume.
				const broken = `{"chat_retention_days":-1,"theme":"dark",}`
				if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
					t.Fatalf("seed %s: %v", path, err)
				}
				return broken
			},
		},
		{
			desc:   "PUT over an unparseable document",
			method: http.MethodPut,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				const broken = `{`
				if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
					t.Fatalf("seed %s: %v", path, err)
				}
				return broken
			},
		},
		{
			desc:   "PATCH over a file past the size cap",
			method: http.MethodPatch,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				// Valid JSON, so only the cap refuses it. Padding is trailing
				// whitespace, which keeps the document parseable at any size.
				doc := append([]byte(stored), bytes.Repeat([]byte(" "), maxSettingsBytes+1-len(stored))...)
				if err := os.WriteFile(path, doc, 0o600); err != nil {
					t.Fatalf("seed %s: %v", path, err)
				}
				return string(doc)
			},
		},
		{
			desc:   "PATCH over a directory at the name",
			method: http.MethodPatch,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				// os.Open succeeds on a directory and the read then fails, which is
				// exactly the outcome the old code answered as "nothing stored".
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("seed dir %s: %v", path, err)
				}
				return ""
			},
		},
		{
			desc:   "PATCH over a symlink at the name",
			method: http.MethodPatch,
			seed: func(t *testing.T, path string) string {
				t.Helper()
				other := filepath.Join(filepath.Dir(path), "elsewhere.json")
				if err := os.WriteFile(other, []byte(`{"theme":"light"}`), 0o600); err != nil {
					t.Fatalf("seed link target: %v", err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatalf("seed symlink %s: %v", path, err)
				}
				return ""
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, settings.Filename)
			before := tc.seed(t, path)
			s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}

			req := httptest.NewRequest(tc.method, "/api/settings", bytes.NewReader([]byte(`{"fb_path":"/workspace/src"}`)))
			rec := httptest.NewRecorder()
			s.handleSettingsWrite(rec, req, path)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("%s /api/settings = %d, want %d", tc.method, rec.Code, http.StatusInternalServerError)
			}
			// The body has to say the file was not overwritten, or the user's only
			// signal is a failure that reads as "your change was lost" when their
			// settings are the thing at stake.
			if body := rec.Body.String(); !bytes.Contains([]byte(body), []byte("not overwritten")) {
				t.Errorf("refusal body = %s, want it to state the settings were not overwritten", body)
			}
			if before == "" {
				return // a directory or a symlink has no contents to compare
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back %s: %v", path, err)
			}
			if string(after) != before {
				t.Errorf("config.json after a refused %s =\n%s\nwant it untouched:\n%s", tc.method, after, before)
			}
		})
	}
}

// TestSettingsWrite_StillMergesAReadableDocument is the control the refusals above
// need: without it a handler that answered 500 unconditionally would pass every
// case in that table. It also pins the merge itself in both arms — the keys the
// request does not name survive, which is the behaviour the refusal exists to
// protect.
func TestSettingsWrite_StillMergesAReadableDocument(t *testing.T) {
	for _, method := range []string{http.MethodPatch, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, settings.Filename)
			// theme is server-managed, so it survives both arms by different
			// mechanisms — PATCH merges the whole stored document, PUT carries the
			// managed keys the body omits — and both mechanisms sit behind the read
			// this test proves still succeeds.
			if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}

			req := httptest.NewRequest(method, "/api/settings", bytes.NewReader([]byte(`{"last_model":"opus"}`)))
			rec := httptest.NewRecorder()
			s.handleSettingsWrite(rec, req, path)

			if rec.Code != http.StatusOK {
				t.Fatalf("%s /api/settings = %d, want %d; body %s", method, rec.Code, http.StatusOK, rec.Body)
			}
			got, err := readStoredSettings(path)
			if err != nil {
				t.Fatalf("read back %s: %v", path, err)
			}
			if string(got[settings.KeyLastModel]) != `"opus"` {
				t.Errorf("%s = %s, want \"opus\"", settings.KeyLastModel, got[settings.KeyLastModel])
			}
			if string(got[settings.KeyTheme]) != `"dark"` {
				t.Errorf("%s = %s, want \"dark\" (the merge dropped a key the request did not name)",
					settings.KeyTheme, got[settings.KeyTheme])
			}
		})
	}
}

// TestExistingSettingsForMerge_AbsentFileIsNotAFailure is the half of the contract
// the refusals above must not swallow: a fresh volume has no config.json, and its
// first settings write is the ordinary case rather than an error.
//
// It fails if the split is ever taken the other way — refusing everything that is
// not a readable file — which would make a new install unable to save a setting at
// all.
func TestExistingSettingsForMerge_AbsentFileIsNotAFailure(t *testing.T) {
	got, err := readStoredSettings(filepath.Join(t.TempDir(), settings.Filename))
	if err != nil {
		t.Fatalf("readStoredSettings with no file: err = %v, want nil", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("readStoredSettings with no file = %v, want an empty non-nil map", got)
	}
}

// TestExistingSettingsForMerge_DoesNotBlockOnAFIFO is item 3's own case, and the
// reason the read side of this file was hardened for it first.
//
// handleSettingsWrite takes s.settingsMu BEFORE this read, so a read that blocks
// in open(2) does not fail one request — it holds the mutex for the life of the
// process and every later settings write queues behind it. /config is a granted
// browse mount and the agent has a shell there, so one mkfifo is the whole attack.
//
// Bounded rather than direct, because reverting the fix does not make this test
// fail, it makes it HANG: os.Open on a FIFO with no writer waits forever and no
// context deadline reaches it. The timer is what turns that into a reported
// failure. The goroutine is left blocked on a revert, which is acceptable in a
// test binary that is about to report a failure and exit.
func TestExistingSettingsForMerge_DoesNotBlockOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, settings.Filename)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo %s: %v", path, err)
	}

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, err := readStoredSettings(path)
		done <- result{err: err}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("readStoredSettings over a FIFO returned a nil error, want a refusal")
		}
		// Named rather than any-error: the refusal has to come from the file being
		// the wrong KIND, not from a read that happened to fail some other way.
		if !errors.Is(got.err, atomicfile.ErrNotRegular) {
			t.Errorf("readStoredSettings over a FIFO = %v, want atomicfile.ErrNotRegular", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readStoredSettings blocked on a FIFO at config.json; it holds s.settingsMu, so every later settings write is wedged")
	}
}
