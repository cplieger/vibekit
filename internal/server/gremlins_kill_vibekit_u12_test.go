package server

// Tests added by mutant-killing unit vibekit-u12. Each test targets one or
// more surviving gremlins mutants in internal/server and is named/keyed with
// the gk_vibekit_u12_ prefix to avoid colliding with sibling units sharing
// this package. New helpers carry the same prefix; existing package helpers
// (idemHandler/idemReq/serveIdem from idempotency_test.go, testPush from
// server_test.go) are reused, not redefined.

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// gk_vibekit_u12_hasItem reports whether items contains an entry of the given
// Type and Name. Used to assert that each scan stage actually ran.
func gk_vibekit_u12_hasItem(items []kiroConfigItem, typ, name string) bool {
	for _, it := range items {
		if it.Type == typ && it.Name == name {
			return true
		}
	}
	return false
}

// cli.go:45-49 ARITHMETIC_BASE — the `N * time.Second/Minute` products. A `*`
// mutated to `/` (or any other arithmetic op) collapses each duration to a
// different value (5/time.Second == 0, etc.). Asserting the exact whole-second
// / whole-minute magnitude pins the multiplication.
func Test_gk_vibekit_u12_defaultCLITimeouts(t *testing.T) {
	got := defaultCLITimeouts()
	if s := got.Models.Seconds(); s != 5 {
		t.Errorf("defaultCLITimeouts().Models = %v (%.0fs), want 5s", got.Models, s)
	}
	if s := got.Version.Seconds(); s != 2 {
		t.Errorf("defaultCLITimeouts().Version = %v (%.0fs), want 2s", got.Version, s)
	}
	if s := got.Diagnostics.Seconds(); s != 20 {
		t.Errorf("defaultCLITimeouts().Diagnostics = %v (%.0fs), want 20s", got.Diagnostics, s)
	}
	if s := got.Settings.Seconds(); s != 3 {
		t.Errorf("defaultCLITimeouts().Settings = %v (%.0fs), want 3s", got.Settings, s)
	}
	if m := got.ToolsInstall.Minutes(); m != 10 {
		t.Errorf("defaultCLITimeouts().ToolsInstall = %v (%.0fm), want 10m", got.ToolsInstall, m)
	}
}

// cli.go:103:20 CONDITIONALS_BOUNDARY (`c > '9'`) and
// cli.go:107:24 CONDITIONALS_BOUNDARY (`len(v) <= 4`), both in
// safeKiroSettingValueFor's settingInt branch.
//   - "9" sits on the digit upper boundary: original keeps '9' as valid; the
//     `>=` mutant rejects it (returns "").
//   - "1234" sits on the length boundary: original accepts len==4; the `<`
//     mutant rejects it (returns "").
func Test_gk_vibekit_u12_safeKiroSettingValueFor_intBoundaries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"nine is a valid digit (c > '9' boundary)", "9", "9"},
		{"four-digit value is max length (len(v) <= 4 boundary)", "1234", "1234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeKiroSettingValueFor(tt.in, settingInt)
			if got != tt.want {
				t.Errorf("safeKiroSettingValueFor(%q, settingInt) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// cli.go:117:43 CONDITIONALS_BOUNDARY (`i > 0` in parseKiroSettingOutput).
// A '(' at index 0 makes LastIndexByte return 0; the guard `i > 0` must be
// false so the value is returned untrimmed. The `>=` mutant would trim s[:0]
// to the empty string.
func Test_gk_vibekit_u12_parseKiroSettingOutput_parenAtIndexZero(t *testing.T) {
	const in = "(foo)"
	if got := parseKiroSettingOutput(in); got != in {
		t.Errorf("parseKiroSettingOutput(%q) = %q, want %q", in, got, in)
	}
}

// request_logger.go:85:12 (`len(s) < 1`) and :85:26 (`len(s) > 64`) in
// validReqID. len==1 and len==64 are the inclusive boundaries the original
// accepts; the `<=` and `>=` mutants reject them respectively.
func Test_gk_vibekit_u12_validReqID_lengthBoundaries(t *testing.T) {
	if !validReqID("a") {
		t.Errorf("validReqID(%q) = false, want true (len==1 lower boundary)", "a")
	}
	id64 := strings.Repeat("a", 64)
	if !validReqID(id64) {
		t.Errorf("validReqID(64 chars) = false, want true (len==64 upper boundary)")
	}
}

// tools_handlers.go:104:29 CONDITIONALS_BOUNDARY (`len(name) > 80`) in
// validToolName. len==80 is accepted by the original; the `>=` mutant rejects
// it.
func Test_gk_vibekit_u12_validToolName_lengthBoundary(t *testing.T) {
	name80 := strings.Repeat("a", 80)
	if !validToolName(name80) {
		t.Errorf("validToolName(80 chars) = false, want true (len==80 upper boundary)")
	}
}

// server_handlers_settings.go:224:29 CONDITIONALS_NEGATION
// (`json.Unmarshal(v, &pn) == nil`). With a valid `false` value, the original
// (== nil) records prefs[Permission]=false; the `!= nil` mutant skips the
// assignment, leaving the default true. The existing TestSyncPushPreferences
// uses `true`, which can't distinguish — `false` does.
func Test_gk_vibekit_u12_syncPushPreferences_permissionFalse(t *testing.T) {
	mp := &testPush{}
	s := &Server{push: mp}

	s.syncPushPreferences(map[string]json.RawMessage{
		"notify_permission": json.RawMessage(`false`),
	})

	if got := mp.prefs[api.PushKindPermission]; got != false {
		t.Errorf("syncPushPreferences(notify_permission=false) -> prefs[Permission] = %v, want false", got)
	}
}

// kiro_config.go negation mutants 75/79/90/94/101/124/144: each one, under a
// non-cancelled context with a populated tree, causes a section's items to
// disappear (early return on the flipped ctx/err guard, or a skipped append).
// Asserting that steering+skill+agent items are ALL present with a background
// context kills every one of them at once.
func Test_gk_vibekit_u12_scanKiroDirFS_returnsAllSections(t *testing.T) {
	mfs := fstest.MapFS{
		"steering":            &fstest.MapFile{Mode: fs.ModeDir},
		"steering/foo.md":     &fstest.MapFile{Data: []byte("---\ninclusion: manual\n---\n# Foo")},
		"skills":              &fstest.MapFile{Mode: fs.ModeDir},
		"skills/bar":          &fstest.MapFile{Mode: fs.ModeDir},
		"skills/bar/SKILL.md": &fstest.MapFile{Data: []byte("# Bar")},
		"agents":              &fstest.MapFile{Mode: fs.ModeDir},
		"agents/baz.md":       &fstest.MapFile{Data: []byte("# Baz")},
	}

	items := scanKiroDirFS(context.Background(), mfs, "P/.kiro")

	if !gk_vibekit_u12_hasItem(items, "steering", "foo") {
		t.Errorf("scanKiroDirFS missing steering item 'foo'; got %+v", items)
	}
	if !gk_vibekit_u12_hasItem(items, "skill", "bar") {
		t.Errorf("scanKiroDirFS missing skill item 'bar'; got %+v", items)
	}
	if !gk_vibekit_u12_hasItem(items, "agent", "baz") {
		t.Errorf("scanKiroDirFS missing agent item 'baz'; got %+v", items)
	}
}

// idempotency.go:173:35 CONDITIONALS_BOUNDARY (`now.Sub(e.ts) >= c.ttl` in
// sweep). An entry whose age is exactly ttl must be swept by the original
// (>=); the `>` mutant keeps it. sweep takes `now` as a parameter, so the
// boundary is deterministic.
func Test_gk_vibekit_u12_sweep_deletesEntryAtExactTTL(t *testing.T) {
	c := &idempotencyCache{
		entries: map[string]*idempotencyEntry{},
		ttl:     100 * time.Millisecond,
	}
	now := time.Now()
	c.entries["k"] = &idempotencyEntry{ts: now.Add(-c.ttl)} // age == ttl exactly

	c.sweep(now)

	if _, ok := c.entries["k"]; ok {
		t.Errorf("sweep kept an entry aged exactly ttl; want deleted (>= boundary)")
	}
}

// idempotency.go:200:20 CONDITIONALS_BOUNDARY (`>`) and CONDITIONALS_NEGATION
// (`<`) of `len(c.entries) >= c.maxEntries` in begin. At exactly capacity, the
// original evicts the oldest completed entry then adds the new key (len stays
// at max); both the `>` and `<` mutants skip eviction (len grows past max and
// the oldest survives).
func Test_gk_vibekit_u12_begin_evictsAtCapacity(t *testing.T) {
	c := &idempotencyCache{
		entries:    map[string]*idempotencyEntry{},
		ttl:        time.Hour,
		maxEntries: 2,
	}
	now := time.Now()
	c.entries["old"] = &idempotencyEntry{ts: now.Add(-2 * time.Minute)}
	c.entries["new"] = &idempotencyEntry{ts: now.Add(-1 * time.Minute)}

	replay, inflight := c.begin("fresh")
	if replay != nil || inflight {
		t.Fatalf("begin(fresh) = (%v, %v), want (nil, false) for a new key", replay, inflight)
	}
	if got := len(c.entries); got != 2 {
		t.Errorf("len(entries) after begin at capacity = %d, want 2 (oldest evicted, new added)", got)
	}
	if _, ok := c.entries["old"]; ok {
		t.Errorf("oldest completed entry survived; want evicted at capacity")
	}
}

// idempotency.go:302:16 CONDITIONALS_BOUNDARY (`cw.status < 500` in
// middleware). A handler returning exactly 500 must NOT be cached (the
// original aborts the key); the `<=` mutant would cache the 500 response.
// Inspect the cache directly after one request.
func Test_gk_vibekit_u12_middleware_doesNotCache500(t *testing.T) {
	c := &idempotencyCache{
		entries:    map[string]*idempotencyEntry{},
		ttl:        idempotencyTTL,
		maxEntries: idempotencyMaxEntries,
		maxBody:    idempotencyMaxBody,
	}
	h, _ := idemHandler(http.StatusInternalServerError, "application/json", `{"err":1}`)
	mw := c.middleware(h)

	rec := serveIdem(mw, idemReq(http.MethodPost, "/x", "k500"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d, want 500", rec.Code)
	}

	ck := idempotencyCompositeKey(http.MethodPost, "/x", "k500")
	if _, cached := c.entries[ck]; cached {
		t.Errorf("a 500 response was cached; want aborted (status < 500 boundary)")
	}
}

// idempotency.go:354:21 CONDITIONALS_BOUNDARY (`cw.buf.Len()+n > cw.limit` in
// idempotencyWriter.Write). Writing exactly `limit` bytes from an empty buffer
// must NOT overflow under the original (`>`); the `>=` mutant flags overflow
// and discards the buffer.
func Test_gk_vibekit_u12_writer_buffersExactlyAtLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := &idempotencyWriter{ResponseWriter: rec, status: http.StatusOK, limit: 4}

	n, err := cw.Write([]byte("abcd")) // exactly limit bytes
	if err != nil || n != 4 {
		t.Fatalf("Write(4 bytes) = (%d, %v), want (4, nil)", n, err)
	}
	if cw.overflow {
		t.Errorf("writing exactly limit bytes set overflow=true; want false (> boundary)")
	}
	if got := cw.buf.String(); got != "abcd" {
		t.Errorf("buffered = %q, want %q", got, "abcd")
	}
}
