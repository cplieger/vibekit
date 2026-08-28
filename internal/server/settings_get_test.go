package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// getEffective issues GET /api/settings against a config dir and decodes the
// response into the wire type. Decoding into the STRUCT rather than a map is part
// of the assertion: a response missing a field would leave it at its Go zero
// value, which for chat_retention_days is 0 ("delete chats on close") and would be
// caught by every case below that expects a real default.
func getEffective(t *testing.T, dir string) vibekit.EffectiveSettings {
	t.Helper()
	rec := httptest.NewRecorder()
	handleSettingsGet(rec, filepath.Join(dir, settings.Filename))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200 (the read fails OPEN)", rec.Code)
	}
	var got vibekit.EffectiveSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response %s: %v", rec.Body.String(), err)
	}
	return got
}

// seedConfig writes raw bytes to config.json in a fresh dir.
func seedConfig(t *testing.T, raw string) string {
	t.Helper()
	dir := t.TempDir()
	if raw != "" {
		if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(raw), 0o600); err != nil {
			t.Fatalf("seed config.json: %v", err)
		}
	}
	return dir
}

// TestSettingsGet_AbsentFileServesEveryDefault pins the fresh-volume answer. It is
// the case the handler always got right, and it is here so the cases below are
// read as changes to the OTHER branch rather than to this one.
func TestSettingsGet_AbsentFileServesEveryDefault(t *testing.T) {
	got := getEffective(t, t.TempDir())
	if want := settings.EffectiveDefaults(); !effectiveEqual(got, want) {
		t.Errorf("GET with no config.json = %+v, want the defaults %+v", got, want)
	}
}

// TestSettingsGet_ResolvesDefaultsUnderStoredValues is the whole point of the
// change: a stored file that names two keys must not make the other thirteen
// absent from the response.
//
// Reverting the handler to echoing the file's bytes fails this on the first
// unlisted field it checks — which is what the client used to have to paper over
// with its own copies of these defaults.
func TestSettingsGet_ResolvesDefaultsUnderStoredValues(t *testing.T) {
	dir := seedConfig(t, `{"chat_retention_days":-1,"theme":"dark"}`)
	got := getEffective(t, dir)

	// Stored values win.
	if got.ChatRetentionDays != -1 {
		t.Errorf("chat_retention_days = %d, want -1 (the stored value)", got.ChatRetentionDays)
	}
	if got.Theme != "dark" {
		t.Errorf("theme = %q, want dark (the stored value)", got.Theme)
	}
	// Everything the file did not name is present and at its default. These three
	// are the ones whose default is NOT the zero value, so they are the ones a
	// client reading an absent key would have got wrong.
	if !got.KnowledgeEnabled {
		t.Error("knowledge_enabled = false, want true: absent must not read as the zero value")
	}
	if !got.NotifyAgentFinished || !got.NotifyPRStatus {
		t.Errorf("notify kinds = (%v, %v), want (true, true): the per-kind switches default ON while the master defaults off",
			got.NotifyAgentFinished, got.NotifyPRStatus)
	}
	if len(got.AgentIgnoreFiles) == 0 {
		t.Error("agent_ignore_files is empty; this is the live bug — the panel rendered an empty chip row while the filter applied two patterns")
	}
	// And the master switch really is off, so the assertion above is about polarity
	// rather than about everything defaulting true.
	if got.NotificationsEnabled {
		t.Error("notifications_enabled = true, want false")
	}
}

// TestSettingsGet_AWrongTypedStoredValueYieldsTheDefault is the finding the
// adversarial review contributed: a complete response whose values were never
// checked is not safer than a sparse one, it just moves where the wrong value
// enters — and it moves it to the moment the client stops guarding.
//
// A hand-edited config.json is the realistic source. Each case stores a
// well-formed JSON value of the wrong type for its field.
func TestSettingsGet_AWrongTypedStoredValueYieldsTheDefault(t *testing.T) {
	tests := []struct {
		desc  string
		raw   string
		check func(*testing.T, vibekit.EffectiveSettings)
	}{
		{
			desc: "a string where a number is declared",
			raw:  `{"chat_retention_days":"seven"}`,
			check: func(t *testing.T, got vibekit.EffectiveSettings) {
				if got.ChatRetentionDays != settings.DefaultChatRetentionDays {
					t.Errorf("chat_retention_days = %d, want the default %d", got.ChatRetentionDays, settings.DefaultChatRetentionDays)
				}
			},
		},
		{
			desc: "a number where a bool is declared",
			raw:  `{"knowledge_enabled":0}`,
			check: func(t *testing.T, got vibekit.EffectiveSettings) {
				if !got.KnowledgeEnabled {
					t.Error("knowledge_enabled = false; a 0 must not be read as false, it must be refused for the default true")
				}
			},
		},
		{
			desc: "a null, which encoding/json would otherwise accept as a no-op",
			raw:  `{"chat_retention_days":null}`,
			check: func(t *testing.T, got vibekit.EffectiveSettings) {
				// The trap: json.Unmarshal of null into an int succeeds and leaves the
				// scratch at 0, so accepting it would persist "delete chats on close".
				if got.ChatRetentionDays != settings.DefaultChatRetentionDays {
					t.Errorf("chat_retention_days = %d, want the default %d; a stored null must not resolve to the zero value",
						got.ChatRetentionDays, settings.DefaultChatRetentionDays)
				}
			},
		},
		{
			desc: "a string where a list is declared",
			raw:  `{"agent_ignore_files":".gitignore"}`,
			check: func(t *testing.T, got vibekit.EffectiveSettings) {
				if !slices.Equal(got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles()) {
					t.Errorf("agent_ignore_files = %v, want the default %v", got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles())
				}
			},
		},
		{
			// The element values are deliberately unlike the defaults. Measured: an
			// in-place json.Unmarshal writes each element until it meets the bad one, so
			// decoding ["zzz",7] over [".gitignore",".kiroignore"] leaves
			// [zzz .kiroignore] — a MIXTURE of stored and default, which is worse than
			// either. A fixture whose first element happened to equal the default would
			// pass against that bug by coincidence, which an earlier version of this case
			// did.
			desc: "a partially-decodable list does not mix stored and default elements",
			raw:  `{"agent_ignore_files":["zzz",7]}`,
			check: func(t *testing.T, got vibekit.EffectiveSettings) {
				if !slices.Equal(got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles()) {
					t.Errorf("agent_ignore_files = %v, want the whole default %v with nothing of the stored list in it",
						got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles())
				}
			},
		},
		{
			// Measured: unlike a scalar, a null decoded in place over a slice SETS IT TO
			// NIL and returns no error, so accepting it would empty the ignore list
			// silently — the same end state as the live bug this change fixes.
			desc: "a null over a list does not wipe it",
			raw:  `{"agent_ignore_files":null}`,
			check: func(t *testing.T, got vibekit.EffectiveSettings) {
				if !slices.Equal(got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles()) {
					t.Errorf("agent_ignore_files = %v, want the default %v; a stored null must not empty the list",
						got.AgentIgnoreFiles, settings.DefaultAgentIgnoreFiles())
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			tc.check(t, getEffective(t, seedConfig(t, tc.raw)))
		})
	}
}

// TestSettingsGet_AWrongTypedValueIsReportedByKeyNotByValue pins the log contract.
// A settings file can hold a token somebody pasted into the wrong field, and this
// line goes to Loki, so the key is reportable and the value is not.
func TestSettingsGet_AWrongTypedValueIsReportedByKeyNotByValue(t *testing.T) {
	const secret = "ghp_notarealtokenbutshapedlikeone"
	logs := captureLogs(t)
	getEffective(t, seedConfig(t, `{"chat_retention_days":"`+secret+`"}`))

	line := logs.String()
	if !strings.Contains(line, settings.KeyChatRetentionDays) {
		t.Errorf("log = %q, want it to name the key %q", line, settings.KeyChatRetentionDays)
	}
	if strings.Contains(line, secret) {
		t.Errorf("log contains the stored VALUE; a settings file can hold a pasted credential:\n%s", line)
	}
}

// TestSettingsGet_AStoredNullIsReported is the reporting half, and it is the case
// that a value assertion alone cannot see.
//
// Measured: json.Unmarshal of null into an int or a bool is a NO-OP returning a nil
// error, so a null decoded in place leaves the default standing and reports
// nothing. The value is then right by accident while the user who wrote that null
// is told nothing about it. Refusing null explicitly is what makes it reportable,
// so this test is what distinguishes the implementation from the shortcut.
func TestSettingsGet_AStoredNullIsReported(t *testing.T) {
	logs := captureLogs(t)
	got := getEffective(t, seedConfig(t, `{"knowledge_enabled":null}`))

	if !got.KnowledgeEnabled {
		t.Error("knowledge_enabled = false, want the default true")
	}
	if !strings.Contains(logs.String(), settings.KeyKnowledgeEnabled) {
		t.Errorf("a stored null was applied silently; want the key reported:\n%s", logs.String())
	}
}

// TestSettingsGet_FailsOpenOnAnUnreadableDocument is the read half of the
// read-open/write-closed asymmetry.
//
// Each of these files makes the write path REFUSE (see
// TestSettingsWrite_RefusesWhenTheStoredSettingsCannotBeRead, which asserts 500
// for the same shapes). The read serves defaults instead, because this is a
// dev-box container whose operator reshapes /config by hand and a surface that
// shows nothing helps them less than one that shows the values in force. The
// write's refusal is what protects the file.
func TestSettingsGet_FailsOpenOnAnUnreadableDocument(t *testing.T) {
	tests := []struct {
		desc string
		seed func(*testing.T, string)
	}{
		{
			desc: "invalid json",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(`{"theme":`), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "a top-level null",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(`null`), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "a top-level array",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "over the size cap",
			seed: func(t *testing.T, path string) {
				t.Helper()
				big := append(bytes.Repeat([]byte(" "), maxSettingsBytes+1), []byte("{}")...)
				if err := os.WriteFile(path, big, 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "a directory at the name",
			seed: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
		{
			desc: "a symlink at the name",
			seed: func(t *testing.T, path string) {
				t.Helper()
				other := filepath.Join(filepath.Dir(path), "elsewhere.json")
				if err := os.WriteFile(other, []byte(`{"theme":"light"}`), 0o600); err != nil {
					t.Fatalf("seed target: %v", err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatalf("seed symlink: %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			tc.seed(t, filepath.Join(dir, settings.Filename))
			logs := captureLogs(t)

			got := getEffective(t, dir)
			if want := settings.EffectiveDefaults(); !effectiveEqual(got, want) {
				t.Errorf("GET over %s = %+v, want the defaults", tc.desc, got)
			}
			// Serving defaults SILENTLY would be the bad version of failing open: the
			// operator would see plausible values and no reason to look at the file.
			if !strings.Contains(logs.String(), "unreadable") {
				t.Errorf("no warning logged for %s; failing open must say so:\n%s", tc.desc, logs.String())
			}
		})
	}
}

// TestSettingsGet_DoesNotBlockOnAFIFO is the GET's half of a guard the write path
// already had. os.ReadFile, which this handler used to call, blocks in open(2) on
// a FIFO with no deadline to rescue it, so one FIFO planted at config.json parked
// the handler's goroutine for the life of the process. Sharing readStoredSettings
// brings atomicfile.OpenRegular's refusal with it.
//
// Reverting the handler to os.ReadFile makes this test HANG rather than fail,
// which is the failure mode worth knowing about.
func TestSettingsGet_DoesNotBlockOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, settings.Filename)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		handleSettingsGet(rec, path)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleSettingsGet blocked on a FIFO at config.json")
	}
}

// TestSettingsGet_ThenPatchPersistsOnlyTheStoredKeys is the test the adversarial
// review asked for, and it pins the claim that a complete RESPONSE does not become
// a complete FILE.
//
// The mechanism it protects: PATCH merges against the stored document rather than
// against whatever the client last read (mergeSettingsPatch), so the fifteen
// defaults the GET now sends cannot be written back. If a future change routed the
// write through the response instead, every default would materialise on disk and
// a later change to any default would stop reaching existing installs.
func TestSettingsGet_ThenPatchPersistsOnlyTheStoredKeys(t *testing.T) {
	dir := seedConfig(t, `{"theme":"dark"}`)
	path := filepath.Join(dir, settings.Filename)

	// The client reads the full effective document...
	if got := getEffective(t, dir); !got.KnowledgeEnabled {
		t.Fatal("precondition: the response should carry knowledge_enabled=true")
	}
	// ...then writes one key.
	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(`{"fb_path":"/workspace"}`)))
	rec := httptest.NewRecorder()
	s.handleSettingsWrite(rec, req, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var onDisk map[string]json.RawMessage
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	if len(onDisk) != 2 {
		t.Errorf("config.json holds %d keys (%s), want exactly 2 — the stored theme and the patched fb_path", len(onDisk), raw)
	}
	for _, unwanted := range []string{settings.KeyKnowledgeEnabled, settings.KeyChatRetentionDays, settings.KeyNotifyAgentFinished} {
		if _, ok := onDisk[unwanted]; ok {
			t.Errorf("config.json gained %q; the GET's defaults must not materialise on disk", unwanted)
		}
	}
}

// TestSettingsGet_PatchAgainstANullDocumentDoesNotPanic is a regression test for a
// live defect found while designing this change.
//
// json.Unmarshal of the literal `null` into a map sets it to NIL and returns a nil
// error, overriding the make() that preceded it. maps.Copy onto a nil map panics,
// so a four-byte config.json made every settings write fail with an opaque 500
// through webhttp.Recoverer. `[]` and `"str"` both error on their own; null was the
// gap, and readStoredSettings now refuses it explicitly.
func TestSettingsGet_PatchAgainstANullDocumentDoesNotPanic(t *testing.T) {
	dir := seedConfig(t, `null`)
	path := filepath.Join(dir, settings.Filename)
	s := &Server{agent: &fakeEngine{}, push: &testPush{}, configDir: dir}

	req := httptest.NewRequest(http.MethodPatch, "/api/settings", bytes.NewReader([]byte(`{"theme":"dark"}`)))
	rec := httptest.NewRecorder()
	// No recover() here on purpose: the panic this pins must not reach the
	// middleware, so an unrecovered panic failing the test IS the assertion.
	s.handleSettingsWrite(rec, req, path)

	// The write REFUSES, because it cannot read what is stored and its next act
	// would be a whole-file replace.
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("PATCH over a null document = %d, want 500 (refuse, do not overwrite)", rec.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != "null" {
		t.Errorf("config.json = %s, want it untouched", after)
	}
}

// effectiveEqual compares two views over EVERY field, including any added later —
// which is why it is one DeepEqual rather than a written-out list that would need
// maintaining in step with the struct.
//
// DeepEqual is safe here because this package owns the type (no unexported fields,
// no dependency's struct), and the one normalisation it needs is the slice: an
// empty agent_ignore_files decoded from `[]` and a nil one from an absent key both
// mean "no patterns", and DeepEqual would call them different.
func effectiveEqual(a, b vibekit.EffectiveSettings) bool {
	if len(a.AgentIgnoreFiles) == 0 {
		a.AgentIgnoreFiles = nil
	}
	if len(b.AgentIgnoreFiles) == 0 {
		b.AgentIgnoreFiles = nil
	}
	return reflect.DeepEqual(a, b)
}
