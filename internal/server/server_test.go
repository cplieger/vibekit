package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/pinstall/v2"
	"github.com/cplieger/vibekit/internal/modeltext"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestSyncPushPreferences(t *testing.T) {
	mp := &testPush{}
	// An empty configDir would resolve the persisted-settings lookup against the
	// package directory, so the fixture names a directory that has no config.json
	// rather than depending on the cwd not growing one.
	s := &Server{push: mp, configDir: t.TempDir()}

	// Both true by default.
	s.syncPushPreferences(map[string]json.RawMessage{})
	if !mp.prefs[vibekit.PushKindAgentFinished] || !mp.prefs[vibekit.PushKindPermission] {
		t.Error("defaults should be true")
	}

	// Set agent_finished to false.
	s.syncPushPreferences(map[string]json.RawMessage{
		"notify_agent_finished": json.RawMessage(`false`),
	})
	if mp.prefs[vibekit.PushKindAgentFinished] {
		t.Error("agent_finished should be false")
	}
	if !mp.prefs[vibekit.PushKindPermission] {
		t.Error("permission should be true")
	}
}

type testPush struct {
	prefs map[vibekit.PushKind]bool
}

var _ pushService = (*testPush)(nil)

// Two methods, because pushService is two methods. This fake used to carry
// eight, six of which this package can never call.
func (p *testPush) RegisterRoutes(*http.ServeMux)                  {}
func (p *testPush) SetPreferences(prefs map[vibekit.PushKind]bool) { p.prefs = prefs }

func TestSafeKiroSetting(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		// Original settings
		{"chat.enableCheckpoint", "chat.enableCheckpoint"},
		{"chat.enableTodoList", "chat.enableTodoList"},
		{"chat.enableKnowledge", "chat.enableKnowledge"},
		{"telemetry.enabled", "telemetry.enabled"},
		// New settings (Wave 1 items 15, 16)
		{"chat.enableSubagent", "chat.enableSubagent"},
		{"chat.enablePromptHints", "chat.enablePromptHints"},
		{"chat.disableAutoCompaction", "chat.disableAutoCompaction"},
		{"hooks.showStatus", "hooks.showStatus"},
		{"compaction.excludeContextWindowPercent", "compaction.excludeContextWindowPercent"},
		{"compaction.excludeMessages", "compaction.excludeMessages"},
		// kiro-cli 2.1+: load MCP tools on demand via tool_search.
		{"toolSearch.enabled", "toolSearch.enabled"},
		// Rejected settings
		{"chat.defaultModel", ""},
		{"api.timeout", ""},
		{"arbitrary.key", ""},
		// Removed from the allowlist: it only affected kiro-cli's own TUI
		// prompt line (which vibekit never renders) and is absent from
		// kiro-cli 2.12; vibekit reads context usage from usage_update.
		{"chat.enableContextUsageIndicator", ""},
	}
	for _, tt := range tests {
		got := safeKiroSetting(tt.key)
		if got != tt.want {
			t.Errorf("safeKiroSetting(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestSafeKiroSettingValue(t *testing.T) {
	tests := []struct {
		val  string
		want string
	}{
		{"true", "true"},
		{"false", "false"},
		{"15", "15"},
		{"100", "100"},
		{"0", "0"},
		{"9", "9"},       // single digit '9' sits on the digit upper boundary
		{"1234", "1234"}, // four digits is the inclusive max length
		{"abc", ""},
		{"12.5", ""},
		{"", ""},
		{"12345", ""}, // too long
		{"-1", ""},    // negative
	}
	for _, tt := range tests {
		got := safeKiroSettingValue(tt.val)
		if got != tt.want {
			t.Errorf("safeKiroSettingValue(%q) = %q, want %q", tt.val, got, tt.want)
		}
	}
}

func TestParseKiroSettingOutput(t *testing.T) {
	// kiro-cli appends a " (global)" or " (local)" scope suffix to
	// every non-empty settings value. Left unstripped, the settings
	// page compared `"true (global)"` against `"true"` and showed
	// every enabled toggle as off. Verify the stripper handles the
	// cases we've actually observed plus a couple of defensive ones.
	tests := []struct {
		in, want string
	}{
		{"true (global)", "true"},
		{"false (global)", "false"},
		{"true (local)", "true"},
		{"7 (global)", "7"},
		{"true (global)\n", "true"},
		{"  true (global)  ", "true"},
		{"", ""},
		{"true", "true"},
		{"plain-value", "plain-value"},
		// No leading paren: nothing to strip, bare text survives.
		{"name (with parens) (global)", "name (with parens)"},
		// A '(' at index 0 must not trigger a trim (that would return the
		// empty string); the whole value survives untouched.
		{"(foo)", "(foo)"},
	}
	for _, tt := range tests {
		if got := parseKiroSettingOutput(tt.in); got != tt.want {
			t.Errorf("parseKiroSettingOutput(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func FuzzSafeKiroSettingValue(f *testing.F) {
	f.Add("true")
	f.Add("false")
	f.Add("0")
	f.Add("9999")
	f.Add("12345")
	f.Add("")
	f.Add("-1")
	f.Add("abc")
	f.Add("12.5")

	f.Fuzz(func(t *testing.T, v string) {
		got := safeKiroSettingValue(v)
		if got == "" {
			return // rejected — fine
		}
		// Invariant: accepted values are either "true"/"false" or
		// numeric strings of 1-4 digits (non-negative integer).
		if got == "true" || got == "false" {
			return
		}
		if len(got) > 4 || len(got) == 0 {
			t.Fatalf("accepted %q with len %d (want 1-4 digit number or bool)", got, len(got))
		}
		for _, c := range got {
			if c < '0' || c > '9' {
				t.Fatalf("accepted %q containing non-digit %q", got, c)
			}
		}
	})
}

func FuzzParseKiroSettingOutput(f *testing.F) {
	// Seed corpus from existing test cases.
	seeds := []string{
		"true (global)", "false (global)", "true (local)",
		"7 (global)", "true (global)\n", "  true (global)  ",
		"", "true", "plain-value",
		"name (with parens) (global)",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		output := parseKiroSettingOutput(input)
		trimmed := strings.TrimSpace(input)
		// Output must be a substring of the trimmed input (no fabrication).
		if output != "" && !strings.Contains(trimmed, output) {
			t.Errorf("output %q is not a substring of trimmed input %q", output, trimmed)
		}
		// Output length must not exceed trimmed input length.
		if len(output) > len(trimmed) {
			t.Errorf("output %q longer than trimmed input %q", output, trimmed)
		}
		// Must never panic (implicit: reaching here means no panic).
	})
}

func TestModelHidden(t *testing.T) {
	tests := []struct {
		desc string
		want bool
	}{
		{"A great model for coding", false},
		{"[Deprecated] Old model", true},
		{"[Legacy] Older model", true},
		{"[deprecated] lowercase tag", true},
		{"[legacy] lowercase tag", true},
		{"This model is deprecated in prose", false},
		{"Model [DEPRECATED] uppercase", true},
		{"Model [LEGACY] uppercase", true},
		{"", false},
	}
	for _, tt := range tests {
		got := modeltext.Hidden(tt.desc)
		if got != tt.want {
			t.Errorf("TagExcluded(%q, HiddenTags) = %v, want %v", tt.desc, got, tt.want)
		}
	}
}

func TestStripCodeFence(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no fence passthrough", "plain text\nmore lines", "plain text\nmore lines"},
		{"fence with language tag", "```go\nfunc main() {}\n```", "func main() {}"},
		{"fence without language tag", "```\nsome code\n```", "some code"},
		{"fence with trailing whitespace", "```js\nlet x = 1;\n```  \n", "let x = 1;"},
		{"bare triple backtick no newline", "```", "```"},
		{"empty content between fences", "```\n\n```", ""},
		{"multiline content", "```python\nimport os\nprint(os.getcwd())\n```", "import os\nprint(os.getcwd())"},
		{"no trailing fence", "```go\nfunc main() {}", "func main() {}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modeltext.StripCodeFence(tt.input)
			if got != tt.want {
				t.Errorf("StripCodeFence(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSteeringInclusion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"standard front-matter", "---\ninclusion: manual\ndescription: test\n---\n# Doc", "manual"},
		{"always inclusion", "---\ninclusion: always\n---\n# Doc", "always"},
		{"fileMatch inclusion", "---\ninclusion: fileMatch\nfileMatchPattern: \"**/*.go\"\n---\n# Doc", "fileMatch"},
		{"no front-matter defaults to always", "# Just a heading\nSome content", "always"},
		{"empty file defaults to always", "", "always"},
		{"front-matter without inclusion defaults to always", "---\ndescription: no inclusion key\n---\n# Doc", "always"},
		{"unclosed front-matter defaults to always", "---\ninclusion: manual\n# No closing fence", "always"},
		{"inclusion with extra whitespace", "---\ninclusion:   manual  \n---\n# Doc", "manual"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSteeringInclusion([]byte(tt.content))
			if got != tt.want {
				t.Errorf("parseSteeringInclusion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseSteeringInclusion_NilData(t *testing.T) {
	got := parseSteeringInclusion(nil)
	if got != "always" {
		t.Errorf("parseSteeringInclusion(nil) = %q, want %q", got, "always")
	}
}

func TestSpaHandler(t *testing.T) {
	tests := []struct {
		fs         fstest.MapFS
		name       string
		path       string
		wantBody   string
		wantHeader string
		wantCode   int
	}{
		{
			name: "serves existing file",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html>index</html>")},
				"style.css":  {Data: []byte("body{}")},
				"js/app.js":  {Data: []byte("console.log('hi')")},
			},
			path:     "/style.css",
			wantCode: http.StatusOK,
			wantBody: "body{}",
		},
		{
			name: "falls back to index for unknown path",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html>index</html>")},
			},
			path:     "/chat/abc123",
			wantCode: http.StatusOK,
			wantBody: "<html>index</html>",
		},
		{
			name: "root serves index",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html>root</html>")},
			},
			path:     "/",
			wantCode: http.StatusOK,
			wantBody: "<html>root</html>",
		},
		{
			// HTML (and the SPA fallback) is never cached: fresh HTML on
			// every load is what makes a new release's script graph take
			// effect immediately (assets revalidate via ETag instead).
			name: "html gets no-store",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html></html>")},
			},
			path:       "/",
			wantCode:   http.StatusOK,
			wantHeader: "no-store",
		},
		{
			name: "asset gets revalidation policy",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html></html>")},
				"app.js":     {Data: []byte("console.log(1)")},
			},
			path:       "/app.js",
			wantCode:   http.StatusOK,
			wantHeader: "no-cache",
		},
		{
			name: "directory falls back to index",
			fs: fstest.MapFS{
				"index.html": {Data: []byte("<html>fallback</html>")},
				"assets":     {Mode: fs.ModeDir},
			},
			path:     "/assets",
			wantCode: http.StatusOK,
			wantBody: "<html>fallback</html>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := spaHandler(tt.fs)
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && !containsSubstring(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want to contain %q", rec.Body.String(), tt.wantBody)
			}
			if tt.wantHeader != "" {
				if got := rec.Header().Get("Cache-Control"); got != tt.wantHeader {
					t.Errorf("Cache-Control = %q, want %q", got, tt.wantHeader)
				}
			}
		})
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func FuzzParseSteeringInclusion(f *testing.F) {
	// Seed corpus from existing unit test inputs.
	seeds := []string{
		"---\ninclusion: manual\ndescription: test\n---\n# Doc",
		"---\ninclusion: always\n---\n# Doc",
		"---\ninclusion: fileMatch\nfileMatchPattern: \"**/*.go\"\n---\n# Doc",
		"# Just a heading\nSome content",
		"",
		"---\ndescription: no inclusion key\n---\n# Doc",
		"---\ninclusion: manual\n# No closing fence",
		"---\ninclusion:   manual  \n---\n# Doc",
		// Adversarial cases.
		"---\n---\n",
		"---\n\n---\n",
		"---\n---",
		string([]byte{0x00, 0x01, 0x02}),
		"---\n" + strings.Repeat("a", 10000) + "\n---\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, content string) {
		// Must not panic (implicit: reaching here means no panic).
		_ = parseSteeringInclusion([]byte(content))
	})
}

func BenchmarkScanKiroDirFS(b *testing.B) {
	makeDocs := func(n int) fstest.MapFS {
		m := fstest.MapFS{
			"steering":  &fstest.MapFile{Mode: fs.ModeDir},
			"skills":    &fstest.MapFile{Mode: fs.ModeDir},
			"skills/s1": &fstest.MapFile{Mode: fs.ModeDir},
			"agents":    &fstest.MapFile{Mode: fs.ModeDir},
			"agents/a.md": &fstest.MapFile{
				Data: []byte("---\ninclusion: always\n---\n# Agent"),
			},
		}
		for i := range n {
			// strconv.Itoa, not a 26-letter alphabet: the old
			// "doc"+rune('a'+i%26) COLLIDED after 26 entries, so makeDocs(50)
			// built 26 distinct steering docs rather than 50.
			name := "steering/doc" + strconv.Itoa(i) + ".md"
			m[name] = &fstest.MapFile{
				Data: []byte("---\ninclusion: manual\ndescription: benchmark doc\n---\n# Heading\nContent here for realism.\n"),
			}
		}
		return m
	}

	// The axis FLATTENS at maxSteeringPerDir (20): scanSteering stops once it
	// has 20 items, so the 50 case measures the capped path — 50 directory
	// entries read, 20 files opened — not 50 docs' worth of work. Measured on
	// go1.27.0 at -benchtime=200x: 5 docs 6.3 µs / 7,688 B / 71 allocs, 20 docs
	// 20.8 µs / 30,268 B / 210 allocs, 50 docs 29.5 µs / 32,664 B / 211 allocs.
	// The near-flat allocation count between the last two IS the cap, and it is
	// stated here because the name says 50 and the work does not.
	for _, count := range []int{5, 20, 50} {
		b.Run(strconv.Itoa(count)+"_docs", func(b *testing.B) {
			m := makeDocs(count)
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				_ = scanKiroDirFS(ctx, m, "test/.kiro")
			}
		})
	}
}

func FuzzScanKiroDirFS(f *testing.F) {
	f.Add("normal.md", "other.md", "skill1")
	f.Add("has-dash.md", "agent.md", "sk")
	f.Add("", ".hidden.md", "")
	f.Add("a.txt", "b.md", "dir")

	f.Fuzz(func(t *testing.T, steeringName, agentName, skillName string) {
		m := fstest.MapFS{
			"steering": &fstest.MapFile{Mode: fs.ModeDir},
			"agents":   &fstest.MapFile{Mode: fs.ModeDir},
			"skills":   &fstest.MapFile{Mode: fs.ModeDir},
		}
		if steeringName != "" {
			m["steering/"+steeringName] = &fstest.MapFile{
				Data: []byte("---\ninclusion: always\n---\n# Doc"),
			}
		}
		if agentName != "" {
			m["agents/"+agentName] = &fstest.MapFile{
				Data: []byte("# Agent"),
			}
		}
		if skillName != "" {
			m["skills/"+skillName] = &fstest.MapFile{Mode: fs.ModeDir}
		}

		items := scanKiroDirFS(t.Context(), m, "prefix/.kiro")

		for _, item := range items {
			if strings.Contains(item.Path, "\x00") {
				t.Errorf("item.Path contains null byte: %q", item.Path)
			}
			if item.Type != "steering" && item.Type != "skill" && item.Type != "agent" {
				t.Errorf("item.Type = %q, want steering|skill|agent", item.Type)
			}
			if item.Name == "" {
				t.Errorf("item.Name is empty for path %q", item.Path)
			}
		}
	})
}

// TestHandleHealth_envelopeMatchesTheLibrary pins the two wire properties this
// handler shares with webhttp.ReadinessHandler, and therefore with every other
// app in the fleet that serves a readiness verdict.
//
// KEY ORDER: this handler cannot BE the library's handler (its verdict is
// composite -- a second reason for an unavailable kiro-cli -- while
// ReadinessChecker is Ready() bool), so it matches the library's wire shape by
// hand. It built a map before, and encoding/json sorts map keys, so it emitted
// {"reason":…,"status":…} while the library emitted {"status":…,"reason":…}: one
// envelope in two orders, in apps whose own comments called it canonical.
//
// CACHE: a 200 with no explicit freshness is heuristically cacheable under RFC
// 9111, and a cached "ok" outliving the readiness it reported keeps traffic
// arriving at an instance that has begun draining -- the exact failure the gate
// exists to prevent.
func TestHandleHealth_envelopeMatchesTheLibrary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready bool
		want  string
	}{
		{"unready", false, `{"status":"unready","reason":"starting up or shutting down"}`},
		{"ready", true, `{"status":"ok"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			s.ready.Store(tc.ready)
			rec := httptest.NewRecorder()
			s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

			if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
				t.Errorf("raw health body = %q, want %q (byte-exact: the key ORDER is the shared contract)", got, tc.want)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store: a cached readiness verdict defeats the gate", got)
			}
		})
	}
}

func TestHandleHealth_returns_ok(t *testing.T) {
	s := &Server{}
	s.ready.Store(true)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v; body=%s", err, rec.Body.String())
	}
	if got["status"] != "ok" {
		t.Errorf("status = %q, want ok", got["status"])
	}
}

// TestHandleHealth_DegradedWhenKiroUnready pins the degraded-not-dead start
// (invariant 6): the server is up (ready=true) but the install manager has no
// usable kiro-cli, so health must report 503 with the reason this app publishes
// for the manager's verdict — the signal `docker ps`, monitoring and the client's
// degraded banner all key off.
//
// Each reason is a distinct operator situation, and carrying them separately is
// what the version-aware gate buys over the existence check it replaced: that
// one could only ever say "unavailable", and said "ok" for a binary drifted from
// the pin or one whose auto-update could not be switched off.
func TestHandleHealth_DegradedWhenKiroUnready(t *testing.T) {
	for why, want := range map[pinstall.Reason]string{
		pinstall.ReasonInstalling:  reasonInstalling,
		pinstall.ReasonRetrying:    reasonRetrying,
		pinstall.ReasonUnavailable: reasonUnavailable,
		pinstall.ReasonAssertion:   reasonSettings,
	} {
		t.Run(want, func(t *testing.T) {
			s := &Server{kiroReady: func() (bool, pinstall.Reason) { return false, why }}
			s.ready.Store(true)

			rec := httptest.NewRecorder()
			s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body not JSON: %v; body=%s", err, rec.Body.String())
			}
			if got["status"] != "unready" || got["reason"] != want {
				t.Errorf("body = %v, want unready/%q", got, want)
			}
		})
	}
}

// TestHandleHealth_OKWhenKiroReady covers the two shapes that answer 200: a
// manager reporting ready, and no manager at all (a bare `go run` with no pins,
// where readiness stays pure-listener). The verdict is read per
// probe, so an install completing flips the same server to ok with no restart —
// asserted by flipping the verdict between two probes.
func TestHandleHealth_OKWhenKiroReady(t *testing.T) {
	t.Run("manager reports ready", func(t *testing.T) {
		s := &Server{kiroReady: func() (bool, pinstall.Reason) { return true, pinstall.ReasonReady }}
		s.ready.Store(true)
		rec := httptest.NewRecorder()
		s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("no manager leaves readiness pure-listener", func(t *testing.T) {
		s := &Server{}
		s.ready.Store(true)
		rec := httptest.NewRecorder()
		s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("recovery is visible on the next probe", func(t *testing.T) {
		ready := false
		s := &Server{kiroReady: func() (bool, pinstall.Reason) { return ready, pinstall.ReasonInstalling }}
		s.ready.Store(true)
		rec := httptest.NewRecorder()
		s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 while installing", rec.Code)
		}
		ready = true
		rec = httptest.NewRecorder()
		s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 once the install completed; body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestHandleHealth_unready(t *testing.T) {
	s := &Server{} // ready defaults to false (zero value)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v; body=%s", err, rec.Body.String())
	}
	if got["status"] != "unready" {
		t.Errorf("status = %q, want unready (canonical wire shape)", got["status"])
	}
	if got["reason"] == "" {
		t.Errorf("unready response missing reason field")
	}
}

// TestDefaultCLITimeouts pins the production timeout budget. The expectations
// are whole-second / whole-minute magnitudes, so a wrong unit or arithmetic
// slip in defaultCLITimeouts (e.g. 5*time.Second collapsing to a sub-second
// duration) is caught.
func TestDefaultCLITimeouts(t *testing.T) {
	got := defaultCLITimeouts()
	if s := got.Version.Seconds(); s != 2 {
		t.Errorf("defaultCLITimeouts().Version = %v (%.0fs), want 2s", got.Version, s)
	}
	if s := got.Diagnostics.Seconds(); s != 20 {
		t.Errorf("defaultCLITimeouts().Diagnostics = %v (%.0fs), want 20s", got.Diagnostics, s)
	}
	if s := got.Settings.Seconds(); s != 3 {
		t.Errorf("defaultCLITimeouts().Settings = %v (%.0fs), want 3s", got.Settings, s)
	}
}

// TestSyncPushPreferences_permissionIsAFloor pins the removal of the
// notify_permission off switch at the WRITE path: the key is gone, so a body
// still carrying it — a hand-edited config.json, an older client, a replayed
// request — must not be able to silence a turn-blocking ask. The permission
// preference stays true whatever the patch says.
func TestSyncPushPreferences_permissionIsAFloor(t *testing.T) {
	bodies := map[string]string{
		"BareFalse":             `{"notify_permission":false}`,
		"FalseBesideAnotherKey": `{"notify_permission":false,"notify_agent_finished":false}`,
		"WrongType":             `{"notify_permission":"nonsense"}`,
		"NullValue":             `{"notify_permission":null}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			mp := &testPush{}
			s := &Server{push: mp, configDir: t.TempDir()}
			var patch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &patch); err != nil {
				t.Fatalf("unmarshal %s: %v", body, err)
			}

			s.syncPushPreferences(patch)

			if !mp.prefs[vibekit.PushKindPermission] {
				t.Errorf("syncPushPreferences(%s) -> prefs[Permission] = false, want true (the ask is a floor)", body)
			}
		})
	}
}

// TestNotifyPermissionKeyIsUnreachable pins the setting as GONE rather than
// merely hidden: the key is not in the vibekit-managed set, so a write carrying
// it is reported as unknown and no reader resolves it.
func TestNotifyPermissionKeyIsUnreachable(t *testing.T) {
	if _, known := settings.KnownKeys["notify_permission"]; known {
		t.Error("notify_permission is still a known settings key; the off switch was only hidden, not removed")
	}
	unknown := settings.WarnUnknownKeys([]string{"notify_permission"}, "TestNotifyPermissionKeyIsUnreachable")
	if len(unknown) != 1 || unknown[0] != "notify_permission" {
		t.Errorf("WarnUnknownKeys([notify_permission]) = %v, want [notify_permission]", unknown)
	}
}

// hasKiroConfigItem reports whether items contains an entry of the given Type
// and Name. Used to assert that each scan stage actually contributed an item.
func hasKiroConfigItem(items []kiroConfigItem, typ, name string) bool {
	for _, it := range items {
		if it.Type == typ && it.Name == name {
			return true
		}
	}
	return false
}

// TestScanKiroDirFS_returnsAllSections verifies that a single scan over a
// populated .kiro tree returns an item for every section it walks — steering
// docs, skills, and agents — under a live (non-cancelled) context. A
// regression that early-returned or skipped an append for any section would
// drop that section's items.
func TestScanKiroDirFS_returnsAllSections(t *testing.T) {
	mfs := fstest.MapFS{
		"steering":            &fstest.MapFile{Mode: fs.ModeDir},
		"steering/foo.md":     &fstest.MapFile{Data: []byte("---\ninclusion: manual\n---\n# Foo")},
		"skills":              &fstest.MapFile{Mode: fs.ModeDir},
		"skills/bar":          &fstest.MapFile{Mode: fs.ModeDir},
		"skills/bar/SKILL.md": &fstest.MapFile{Data: []byte("# Bar")},
		"agents":              &fstest.MapFile{Mode: fs.ModeDir},
		"agents/baz.md":       &fstest.MapFile{Data: []byte("# Baz")},
	}

	items := scanKiroDirFS(t.Context(), mfs, "P/.kiro")

	if !hasKiroConfigItem(items, "steering", "foo") {
		t.Errorf("scanKiroDirFS missing steering item 'foo'; got %+v", items)
	}
	if !hasKiroConfigItem(items, "skill", "bar") {
		t.Errorf("scanKiroDirFS missing skill item 'bar'; got %+v", items)
	}
	if !hasKiroConfigItem(items, "agent", "baz") {
		t.Errorf("scanKiroDirFS missing agent item 'baz'; got %+v", items)
	}
}
