package mcp

// Coverage for the pasted-block path: the translation of a publisher README's
// JSON into vibekit records, the three-way key classification, and the batch
// create the HTTP route drives.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// postImport drives POST /api/mcp/import with a raw JSON body, so the tests
// exercise the real decode boundary rather than calling the translator directly
// (an unknown key is only interesting because it survives to a 400).
func postImport(t *testing.T, mux http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/import",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type importResponse struct {
	Results []ImportResult `json:"results"`
	Notes   []string       `json:"notes"`
}

func decodeImport(t *testing.T, rec *httptest.ResponseRecorder) importResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d, body %s", rec.Code, rec.Body.String())
	}
	var out importResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode import response: %v (body %s)", err, rec.Body.String())
	}
	return out
}

// The GitHub MCP server's README block, verbatim in shape: a stdio npx server
// with a token env var whose placeholder value is what the publisher printed.
const githubReadmeBlock = `{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "<YOUR_TOKEN>"
      }
    }
  }
}`

func TestImport_PublisherBlockRoundTripsIntoTheStore(t *testing.T) {
	s, mux := newRoutedStore(t)

	got := decodeImport(t, postImport(t, mux, githubReadmeBlock))
	if len(got.Results) != 1 || got.Results[0].Name != "github" ||
		got.Results[0].Outcome != ImportCreated {
		t.Fatalf("results = %#v, want one created github", got.Results)
	}
	if len(got.Notes) != 0 {
		t.Errorf("notes = %#v, want none for a clean block", got.Notes)
	}

	list := s.List(t.Context())
	if len(list) != 1 {
		t.Fatalf("stored servers = %d, want 1", len(list))
	}
	sv := list[0]
	if sv.Transport != TransportStdio {
		t.Errorf("transport = %q, want stdio (inferred from command)", sv.Transport)
	}
	if sv.Command != "npx" {
		t.Errorf("command = %q", sv.Command)
	}
	if want := []string{"-y", "@modelcontextprotocol/server-github"}; len(sv.Args) != 2 ||
		sv.Args[0] != want[0] || sv.Args[1] != want[1] {
		t.Errorf("args = %#v, want %#v", sv.Args, want)
	}
	if len(sv.Env) != 1 || sv.Env[0].Name != "GITHUB_PERSONAL_ACCESS_TOKEN" {
		t.Fatalf("env = %#v", sv.Env)
	}
	if sv.Env[0].Value != SecretMask {
		t.Errorf("env value = %q, want the mask on a public read", sv.Env[0].Value)
	}
	if !sv.Enabled {
		t.Error("a pasted server with no disabled flag should be enabled")
	}
	if !sv.Prewarm {
		t.Error("an npx server should default to prewarm, matching the npm form")
	}

	// The value the publisher printed is what got stored, not the mask.
	raw := s.EnabledRaw(t.Context())
	if len(raw) != 1 || raw[0].Env[0].Value != "<YOUR_TOKEN>" {
		t.Errorf("raw env = %#v, want the pasted placeholder", raw)
	}
}

func TestImport_MultiServerBlockInstallsEveryEntry(t *testing.T) {
	s, mux := newRoutedStore(t)

	// Two servers of different transports in one block, which is what a README
	// listing a local and a hosted option hands over. `type` decides http vs
	// sse; a "streamable-http" hint normalises to http.
	got := decodeImport(t, postImport(t, mux, `{
	  "mcpServers": {
	    "github":  {"command":"npx","args":["-y","@modelcontextprotocol/server-github"]},
	    "linear":  {"type":"streamable-http","url":"https://mcp.linear.app/mcp",
	                "headers":{"Authorization":"Bearer abc"}},
	    "legacyish":{"type":"sse","url":"https://sse.example.com/mcp"}
	  }
	}`))

	if len(got.Results) != 3 {
		t.Fatalf("results = %#v, want 3", got.Results)
	}
	for _, r := range got.Results {
		if r.Outcome != ImportCreated {
			t.Errorf("%s outcome = %q, want created", r.Name, r.Outcome)
		}
	}

	byName := map[string]*Server{}
	for _, sv := range s.List(t.Context()) {
		byName[sv.Name] = sv
	}
	if len(byName) != 3 {
		t.Fatalf("stored = %#v", byName)
	}
	if byName["github"].Transport != TransportStdio {
		t.Errorf("github transport = %q", byName["github"].Transport)
	}
	if byName["linear"].Transport != TransportHTTP {
		t.Errorf("linear transport = %q, want http (streamable-http normalised)",
			byName["linear"].Transport)
	}
	if byName["legacyish"].Transport != TransportSSE {
		t.Errorf("legacyish transport = %q, want sse", byName["legacyish"].Transport)
	}
	if h := byName["linear"].Headers; len(h) != 1 || h[0].Name != "Authorization" {
		t.Errorf("linear headers = %#v", h)
	}
	// An npx server prewarms; a hosted one has nothing to install.
	if byName["linear"].Prewarm {
		t.Error("a remote server must not be prewarm-flagged")
	}
}

// All-or-nothing: one bad entry means nothing lands, so the user is never left
// diffing the UI against the README to work out which half installed.
func TestImport_OneBadEntryInstallsNothing(t *testing.T) {
	s, mux := newRoutedStore(t)

	rec := postImport(t, mux, `{
	  "mcpServers": {
	    "good": {"command":"npx","args":["-y","pkg"]},
	    "bad":  {"description":"no command and no url"}
	  }
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `bad`) {
		t.Errorf("error should name the failing entry; got %s", rec.Body.String())
	}
	if got := len(s.List(t.Context())); got != 0 {
		t.Errorf("stored servers = %d, want 0 (nothing may land)", got)
	}
}

func TestImport_UnknownKeyIsNamedNotDropped(t *testing.T) {
	s, mux := newRoutedStore(t)

	// The whole point: `comand` used to yield Command == "" and an error naming
	// `command` as missing, which is not the thing that was wrong.
	rec := postImport(t, mux,
		`{"mcpServers":{"github":{"comand":"npx","args":["-y","pkg"]}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`comand`, `did you mean`, `command`, `github`} {
		if !strings.Contains(body, want) {
			t.Errorf("error %q missing %q", body, want)
		}
	}
	if got := len(s.List(t.Context())); got != 0 {
		t.Errorf("stored = %d, want 0", got)
	}
}

func TestImport_UnknownTopLevelKeyIsNamed(t *testing.T) {
	_, mux := newRoutedStore(t)

	rec := postImport(t, mux, `{"mcpSevrers":{"github":{"command":"npx"}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mcpServers") {
		t.Errorf("error should suggest mcpServers; got %s", rec.Body.String())
	}
}

// A publisher block legitimately carries keys vibekit has no field for. Those
// must not read as typos, which is why the classification is three-way rather
// than DisallowUnknownFields.
func TestImport_UnmodelledKeysAreAcceptedWithANote(t *testing.T) {
	s, mux := newRoutedStore(t)

	got := decodeImport(t, postImport(t, mux, `{
	  "$schema": "https://example.com/schema.json",
	  "mcpServers": {
	    "github": {"command":"npx","args":["-y","pkg"],
	               "cwd":"/tmp","timeout":60,"waitForReady":true,
	               "description":"GitHub tools"}
	  }
	}`))

	if len(got.Results) != 1 || got.Results[0].Outcome != ImportCreated {
		t.Fatalf("results = %#v, want the server created anyway", got.Results)
	}
	joined := strings.Join(got.Notes, "\n")
	for _, want := range []string{"cwd", "timeout", "waitForReady", "description", "$schema"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes %q should name %q", joined, want)
		}
	}
	if got := len(s.List(t.Context())); got != 1 {
		t.Errorf("stored = %d, want 1", got)
	}
}

// A re-paste of a block whose servers are already configured keeps the API keys
// the user typed in. This is the case the 409 made impossible: its only
// workaround was delete-then-re-add, which discarded them.
func TestImport_IdenticalReinstallPreservesEnvValues(t *testing.T) {
	s, mux := newRoutedStore(t)

	_ = decodeImport(t, postImport(t, mux, githubReadmeBlock))
	// The user fills the real token in.
	stored := s.List(t.Context())
	if len(stored) != 1 {
		t.Fatalf("setup: stored = %d", len(stored))
	}
	if _, err := s.Update(t.Context(), stored[0].ID, &Server{
		Transport: TransportStdio,
		Name:      "github",
		Command:   "npx",
		Args:      []string{"-y", "@modelcontextprotocol/server-github"},
		Env:       []KeyPair{{Name: "GITHUB_PERSONAL_ACCESS_TOKEN", Value: "ghp_real"}},
		Enabled:   true,
		Prewarm:   true,
	}); err != nil {
		t.Fatalf("fill token: %v", err)
	}

	// Same block again: the placeholder must NOT overwrite the real token.
	again := decodeImport(t, postImport(t, mux, githubReadmeBlock))
	if len(again.Results) != 1 || again.Results[0].Outcome != ImportUnchanged {
		t.Fatalf("results = %#v, want unchanged", again.Results)
	}
	raw := s.EnabledRaw(t.Context())
	if len(raw) != 1 {
		t.Fatalf("stored = %d, want 1 (a reinstall must not duplicate)", len(raw))
	}
	if raw[0].Env[0].Value != "ghp_real" {
		t.Errorf("env value = %q, want the token the user typed", raw[0].Env[0].Value)
	}
	if raw[0].ID != stored[0].ID {
		t.Errorf("id changed on reinstall: %q -> %q", stored[0].ID, raw[0].ID)
	}
}

// Same name, different connection is still a refusal — otherwise the import path
// silently becomes "POST overwrites".
func TestImport_SameNameDifferentSpecIs409(t *testing.T) {
	s, mux := newRoutedStore(t)

	_ = decodeImport(t, postImport(t, mux, githubReadmeBlock))
	rec := postImport(t, mux,
		`{"mcpServers":{"github":{"url":"https://evil.example.com/mcp"}}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	raw := s.EnabledRaw(t.Context())
	if len(raw) != 1 || raw[0].Transport != TransportStdio {
		t.Errorf("stored record was replaced: %#v", raw)
	}
}

func TestImport_ReorderedEnvIsStillTheSameSpec(t *testing.T) {
	s, mux := newRoutedStore(t)

	_ = decodeImport(t, postImport(t, mux,
		`{"mcpServers":{"x":{"command":"srv","env":{"A":"1","B":"2"}}}}`))
	got := decodeImport(t, postImport(t, mux,
		`{"mcpServers":{"x":{"command":"srv","env":{"B":"2","A":"1"}}}}`))
	if len(got.Results) != 1 || got.Results[0].Outcome != ImportUnchanged {
		t.Errorf("results = %#v; reordering env rows is not a new connection", got.Results)
	}
	if n := len(s.List(t.Context())); n != 1 {
		t.Errorf("stored = %d, want 1", n)
	}
}

func TestImport_SingleServerObjectShapeStillWorks(t *testing.T) {
	s, mux := newRoutedStore(t)

	// The raw panel's own template: one server, its name inside the object.
	got := decodeImport(t, postImport(t, mux, `{
	  "name": "local",
	  "command": "/usr/local/bin/my-server",
	  "args": ["--flag","value"],
	  "env": {"MY_TOKEN":"abc"},
	  "prewarm": false
	}`))
	if len(got.Results) != 1 || got.Results[0].Name != "local" {
		t.Fatalf("results = %#v", got.Results)
	}
	list := s.List(t.Context())
	if len(list) != 1 || list[0].Command != "/usr/local/bin/my-server" {
		t.Fatalf("stored = %#v", list)
	}
	if list[0].Prewarm {
		t.Error("explicit prewarm:false must win over the npx default")
	}
}

func TestImport_MissingNameOnASingleObject(t *testing.T) {
	_, mux := newRoutedStore(t)

	rec := postImport(t, mux, `{"command":"npx","args":["-y","pkg"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "name") {
		t.Errorf("error should name the missing field; got %s", rec.Body.String())
	}
}

func TestImport_DisabledBlockFlagInvertsEnabled(t *testing.T) {
	s, mux := newRoutedStore(t)

	_ = decodeImport(t, postImport(t, mux,
		`{"mcpServers":{"off":{"command":"srv","disabled":true}}}`))
	list := s.List(t.Context())
	if len(list) != 1 {
		t.Fatalf("stored = %d", len(list))
	}
	if list[0].Enabled {
		t.Error(`"disabled": true must land as enabled: false`)
	}
}

func TestImport_OAuthObjectReachesTheRecord(t *testing.T) {
	s, mux := newRoutedStore(t)

	_ = decodeImport(t, postImport(t, mux, `{
	  "mcpServers": {"slack": {"url":"https://mcp.slack.com/mcp",
	    "oauth": {"clientId":"cid-1","clientSecret":"csecret-1"}}}
	}`))
	raw := s.EnabledRaw(t.Context())
	if len(raw) != 1 {
		t.Fatalf("stored = %d", len(raw))
	}
	if raw[0].OAuthClientID != "cid-1" || raw[0].OAuthClientSecret != "csecret-1" {
		t.Errorf("oauth not carried: %#v", raw[0])
	}
	// The secret must not come back to the browser.
	if got := s.List(t.Context())[0].OAuthClientSecret; got != SecretMask {
		t.Errorf("oauth_client_secret = %q on a public read, want the mask", got)
	}
}

// The nested object needs its own classification pass. The outer one only sees
// that `oauth` is a key the translator consumes, so before this the object went
// straight into json.Unmarshal, which drops an unmatched member: a misspelt
// clientSecret installed a server with an EMPTY secret and said nothing.
func TestImport_UnknownOAuthKeyIsNamedNotDropped(t *testing.T) {
	s, mux := newRoutedStore(t)

	rec := postImport(t, mux, `{
	  "mcpServers": {"slack": {"url":"https://mcp.slack.com/mcp",
	    "oauth": {"clientId":"cid-1","clientSecrect":"csecret-1"}}}
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"clientSecrect", "did you mean", "clientSecret", "slack"} {
		if !strings.Contains(body, want) {
			t.Errorf("error %q missing %q", body, want)
		}
	}
	if got := len(s.List(t.Context())); got != 0 {
		t.Errorf("stored = %d, want 0; a typoed secret must not install an empty one", got)
	}
}

// The agent reads KAS's rendered file, not vibekit's record, so a paste that
// only updated mcp.json would install a server nothing could connect to. The
// batch goes through the shared persist path, which is what makes hot-reload
// free here; this pins that it really does.
func TestImport_ReachesTheAgentsConfigFile(t *testing.T) {
	s, mux := newRoutedStore(t)

	_ = decodeImport(t, postImport(t, mux, `{
	  "mcpServers": {
	    "github": {"command":"npx","args":["-y","pkg"],"env":{"TOKEN":"t"}},
	    "linear": {"url":"https://mcp.linear.app/mcp"}
	  }
	}`))

	rendered := readKASServers(t, s.kasPath)
	if len(rendered) != 2 {
		t.Fatalf("kas mcpServers = %#v, want both entries", rendered)
	}
	if got := rendered["github"]["command"]; got != "npx" {
		t.Errorf("github command = %v", got)
	}
	if got := rendered["linear"]["url"]; got != "https://mcp.linear.app/mcp" {
		t.Errorf("linear url = %v", got)
	}
	// The record's env reaches the agent with its real value, not the mask.
	env, ok := rendered["github"]["env"].(map[string]any)
	if !ok || env["TOKEN"] != "t" {
		t.Errorf("github env = %#v", rendered["github"]["env"])
	}
}

// The no-op path must skip the write: a persist re-renders KAS's file, whose
// watcher emits a status notification straight back into the hub.
func TestImport_UnchangedEntryDoesNotRewriteTheAgentsFile(t *testing.T) {
	s, mux := newRoutedStore(t)

	_ = decodeImport(t, postImport(t, mux, githubReadmeBlock))
	before, err := os.Stat(s.kasPath)
	if err != nil {
		t.Fatalf("stat kas file: %v", err)
	}
	firstBytes, err := os.ReadFile(s.kasPath)
	if err != nil {
		t.Fatalf("read kas file: %v", err)
	}

	got := decodeImport(t, postImport(t, mux, githubReadmeBlock))
	if got.Results[0].Outcome != ImportUnchanged {
		t.Fatalf("outcome = %q, want unchanged", got.Results[0].Outcome)
	}
	after, err := os.Stat(s.kasPath)
	if err != nil {
		t.Fatalf("re-stat kas file: %v", err)
	}
	// atomicfile renames a fresh inode into place, so a skipped write is visible
	// as an unchanged mtime AND unchanged bytes.
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("kas file was rewritten for a no-op (mtime %v -> %v)",
			before.ModTime(), after.ModTime())
	}
	secondBytes, err := os.ReadFile(s.kasPath)
	if err != nil {
		t.Fatalf("re-read kas file: %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Error("kas file content changed for a no-op")
	}
}

func TestImport_MethodNotAllowed(t *testing.T) {
	_, mux := newRoutedStore(t)

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/import", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/mcp/import = %d, want 405", rec.Code)
	}
}

func TestTranslate_TransportInference(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    Transport
		wantErr string
	}{
		{name: "command implies stdio", body: `{"command":"srv"}`, want: TransportStdio},
		{name: "url implies http", body: `{"url":"https://x/mcp"}`, want: TransportHTTP},
		{
			name: "streamable-http normalises to http",
			body: `{"type":"streamable-http","url":"https://x/mcp"}`, want: TransportHTTP,
		},
		{name: "sse stays sse", body: `{"type":"sse","url":"https://x/mcp"}`, want: TransportSSE},
		{
			name: "unrecognised type falls through to http, as KAS negotiates",
			body: `{"type":"streamableHttp","url":"https://x/mcp"}`, want: TransportHTTP,
		},
		{
			name:    "both is ambiguous",
			body:    `{"command":"srv","url":"https://x/mcp"}`,
			wantErr: "either local (command) or hosted (url)",
		},
		{name: "neither", body: `{"args":["-y"]}`, wantErr: `needs either "command"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := parseImportBody([]byte(`{"mcpServers":{"x":` + tc.body + `}}`))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got %#v", tc.wantErr, req.servers)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(req.servers) != 1 {
				t.Fatalf("servers = %#v", req.servers)
			}
			if req.servers[0].Transport != tc.want {
				t.Errorf("transport = %q, want %q", req.servers[0].Transport, tc.want)
			}
		})
	}
}

func TestTranslate_EnvKeepsTheReadmeOrder(t *testing.T) {
	// A Go map would lose this, which is why the pairs are decoded off the token
	// stream: the order is what the user reads in the form.
	req, err := parseImportBody([]byte(
		`{"mcpServers":{"x":{"command":"srv","env":{"ZED":"1","ALPHA":"2","MID":"3"}}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := make([]string, 0, 3)
	for _, kv := range req.servers[0].Env {
		got = append(got, kv.Name)
	}
	want := []string{"ZED", "ALPHA", "MID"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("env order = %#v, want %#v", got, want)
		}
	}
}

func TestTranslate_ScalarEnvValuesAreStringified(t *testing.T) {
	req, err := parseImportBody([]byte(
		`{"mcpServers":{"x":{"command":"srv","env":{"PORT":3000,"DEBUG":true}}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byName := map[string]string{}
	for _, kv := range req.servers[0].Env {
		byName[kv.Name] = kv.Value
	}
	if byName["PORT"] != "3000" {
		t.Errorf("PORT = %q, want %q (not scientific notation)", byName["PORT"], "3000")
	}
	if byName["DEBUG"] != "true" {
		t.Errorf("DEBUG = %q", byName["DEBUG"])
	}
}

func TestTranslate_NonScalarEnvValueIsNamed(t *testing.T) {
	_, err := parseImportBody([]byte(
		`{"mcpServers":{"x":{"command":"srv","env":{"NESTED":{"a":1}}}}}`))
	if err == nil {
		t.Fatal("expected an error for an object env value")
	}
	if !strings.Contains(err.Error(), "NESTED") {
		t.Errorf("error = %q, want it to name the field", err)
	}
}

func TestTranslate_NameIsAdjustedAndReported(t *testing.T) {
	req, err := parseImportBody([]byte(
		`{"mcpServers":{"@acme/my.server":{"command":"srv"}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := req.servers[0].Name
	if err := ValidateName(got); err != nil {
		t.Fatalf("name %q does not satisfy the store's name rule: %v", got, err)
	}
	if got != "acme-my-server" {
		t.Errorf("name = %q, want %q", got, "acme-my-server")
	}
	if len(req.notes) == 0 || !strings.Contains(strings.Join(req.notes, "\n"), got) {
		t.Errorf("the adjustment must be reported; notes = %#v", req.notes)
	}
}

func TestTranslate_NameWithNoLetters(t *testing.T) {
	_, err := parseImportBody([]byte(`{"mcpServers":{"123":{"command":"srv"}}}`))
	if err == nil {
		t.Fatal("expected an error for a name with no letters")
	}
}

func TestImport_TooManyServers(t *testing.T) {
	_, mux := newRoutedStore(t)

	var b strings.Builder
	b.WriteString(`{"mcpServers":{`)
	for i := 0; i <= maxImportServers; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		// Names must be valid so the cap is what fails, not validation.
		b.WriteString(`"srv`)
		b.WriteString(strings.Repeat("a", i%3))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(string(rune('a' + (i/26)%26)))
		b.WriteString(`":{"command":"x"}`)
	}
	b.WriteString(`}}`)

	rec := postImport(t, mux, b.String())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "max") {
		t.Errorf("error = %s, want the cap named", rec.Body.String())
	}
}

func TestImport_CaseOnlyDuplicateNamesAreRefused(t *testing.T) {
	s, mux := newRoutedStore(t)

	// A JSON object cannot repeat a key, but two keys differing only in case
	// survive decoding and then collide on the store's case-insensitive rule.
	rec := postImport(t, mux,
		`{"mcpServers":{"GitHub":{"command":"a"},"github":{"command":"a"}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if got := len(s.List(t.Context())); got != 0 {
		t.Errorf("stored = %d, want 0", got)
	}
}

func TestParseImportBody_Rejects(t *testing.T) {
	cases := map[string]string{
		"not an object":       `[1,2,3]`,
		"empty object":        `{}`,
		"empty block":         `{"mcpServers":{}}`,
		"block is not object": `{"mcpServers":[]}`,
		"entry is not object": `{"mcpServers":{"x":"npx"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseImportBody([]byte(body)); err == nil {
				t.Errorf("expected an error for %s", body)
			}
		})
	}
}

// FuzzParseImportBody guards the pasted-block parser, which is an untrusted-input
// boundary: the body comes straight from a user's clipboard. The invariants are
// the ones the store depends on downstream — a name it will accept, a valid
// transport, and exactly one of command/url — plus determinism, which matters
// because the translator walks decoded JSON maps and must sort to be stable.
func FuzzParseImportBody(f *testing.F) {
	f.Add([]byte(githubReadmeBlock))
	f.Add([]byte(`{"mcpServers":{"x":{"command":"srv","env":{"A":"1"}}}}`))
	f.Add([]byte(`{"mcpServers":{"x":{"type":"sse","url":"https://h/mcp"}}}`))
	f.Add([]byte(`{"mcpServers":{"@a/b.c":{"command":"srv"}}}`))
	f.Add([]byte(`{"name":"x","command":"srv"}`))
	f.Add([]byte(`{"mcpServers":{"x":{"comand":"srv"}}}`))
	f.Add([]byte(`{"mcpServers":{"x":{"url":"https://h/mcp","oauth":{"clientSecrect":"s"}}}}`))
	f.Add([]byte(`{"mcpServers":{"x":{"url":"https://h/mcp","oauth":"nope"}}}`))
	f.Add([]byte(`{"mcpServers":{"x":{"command":"srv","url":"https://h"}}}`))
	f.Add([]byte(`{"mcpServers":{"x":{"command":"srv","env":{"A":3000}}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		req, err := parseImportBody(data)
		if err != nil {
			return
		}
		if len(req.servers) == 0 {
			t.Fatal("a successful parse must yield at least one server")
		}
		for i, sv := range req.servers {
			if err := ValidateName(sv.Name); err != nil {
				t.Errorf("servers[%d].Name = %q, which the store will reject: %v", i, sv.Name, err)
			}
			if !sv.Transport.Valid() {
				t.Errorf("servers[%d].Transport = %q", i, sv.Transport)
			}
			hasCmd := strings.TrimSpace(sv.Command) != ""
			hasURL := strings.TrimSpace(sv.URL) != ""
			if hasCmd == hasURL {
				t.Errorf("servers[%d] has command=%q url=%q; exactly one is required",
					i, sv.Command, sv.URL)
			}
			if (sv.Transport == TransportStdio) != hasCmd {
				t.Errorf("servers[%d] transport %q disagrees with its fields",
					i, sv.Transport)
			}
		}
		// Determinism: the same bytes must translate to the same order, or a
		// re-paste would reshuffle the configured list.
		second, secondErr := parseImportBody(data)
		if secondErr != nil {
			t.Fatalf("second parse of the same bytes failed: %v", secondErr)
		}
		if len(second.servers) != len(req.servers) {
			t.Fatalf("non-deterministic server count: %d vs %d",
				len(req.servers), len(second.servers))
		}
		for i := range req.servers {
			if second.servers[i].Name != req.servers[i].Name {
				t.Fatalf("non-deterministic order at %d: %q vs %q",
					i, req.servers[i].Name, second.servers[i].Name)
			}
		}
		if strings.Join(second.notes, "\n") != strings.Join(req.notes, "\n") {
			t.Errorf("non-deterministic notes: %#v vs %#v", req.notes, second.notes)
		}
	})
}
