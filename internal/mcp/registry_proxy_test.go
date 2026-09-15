package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fuzzLimit is the caller's limit for the fuzz target: small, so a body of a
// few rows exercises the cut as well as the filter.
const fuzzLimit = 3

func FuzzNormaliseRegistryResponse(f *testing.F) {
	f.Add([]byte(`{"servers":[{"server":{"name":"x","packages":[{"registryType":"npm","identifier":"y","transport":{"type":"stdio"}}]}}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"servers":[]}`))
	f.Add([]byte(`{"servers":null}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"servers":[{"server":{"name":"","remotes":[{"type":"streamable-http","url":"u"}]}}]}`))
	f.Add(buildRegistryPayload(fuzzLimit + 1))
	f.Add([]byte(`{"servers":[],"metadata":{"nextCursor":"more"}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := normaliseRegistryResponse(data, fuzzLimit)
		if err != nil {
			if len(got.Servers) != 0 || got.Filtered != 0 || got.Truncated {
				t.Fatalf("normaliseRegistryResponse(%q) failed with %v but returned %+v, want the zero result", data, err, got)
			}
			return
		}
		if len(got.Servers) > fuzzLimit {
			t.Fatalf("normaliseRegistryResponse(%q) = %d servers, want at most the limit %d", data, len(got.Servers), fuzzLimit)
		}
		if len(got.Servers)+got.Filtered > fuzzLimit {
			t.Fatalf("normaliseRegistryResponse(%q) = %d servers + %d filtered, want at most the limit %d: the sentinel row must be counted nowhere",
				data, len(got.Servers), got.Filtered, fuzzLimit)
		}
		for i := range got.Servers {
			if len(got.Servers[i].Packages) == 0 && len(got.Servers[i].Remotes) == 0 {
				t.Errorf("entry %d has no packages and no remotes but was not filtered", i)
			}
			for j, r := range got.Servers[i].Remotes {
				if r.Type != "http" && r.Type != "sse" {
					t.Errorf("entry[%d].remotes[%d].Type = %q; want http|sse", i, j, r.Type)
				}
			}
		}
	})
}

// The lifecycle status lives in the `_meta` SIBLING of `server`, not on the
// server object — the fixture below is the real shape a live
// registry.modelcontextprotocol.io response uses. A deprecated entry is still
// returned by search (only `deleted` is filtered upstream, behind
// include_deleted), so before this reached the row a dead entry looked live.
func TestNormaliseRegistryResponse_CarriesTheDeprecatedFlag(t *testing.T) {
	body := []byte(`{
		"servers": [
			{"server":{"name":"ex/live","version":"2.0.0",
			  "remotes":[{"type":"streamable-http","url":"https://live"}]},
			 "_meta":{"io.modelcontextprotocol.registry/official":{
			   "status":"active","statusChangedAt":"2026-04-13T17:32:20Z",
			   "publishedAt":"2026-04-13T17:32:20Z","isLatest":true}}},
			{"server":{"name":"ex/dead","version":"0.9.0",
			  "packages":[{"registryType":"npm","identifier":"@ex/dead",
			   "transport":{"type":"stdio"}}]},
			 "_meta":{"io.modelcontextprotocol.registry/official":{
			   "status":"deprecated",
			   "statusMessage":"unmaintained; use @ex/live instead",
			   "isLatest":true}}},
			{"server":{"name":"ex/nometa","version":"1.0.0",
			  "remotes":[{"type":"sse","url":"https://nometa"}]}}
		]
	}`)

	res, err := normaliseRegistryResponse(body, maxSearchLimit)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse: %v", err)
	}
	got := res.Servers
	if len(got) != 3 {
		t.Fatalf("entries = %d, want 3", len(got))
	}
	// An active entry carries no status: the badge is the only consumer and an
	// absent status already reads as active.
	if got[0].Status != "" || got[0].StatusMessage != "" {
		t.Errorf("active entry = %+v, want no status fields", got[0])
	}
	if got[1].Status != "deprecated" {
		t.Errorf("deprecated entry status = %q", got[1].Status)
	}
	if got[1].StatusMessage != "unmaintained; use @ex/live instead" {
		t.Errorf("status message = %q, want the publisher's reason", got[1].StatusMessage)
	}
	if got[2].Status != "" {
		t.Errorf("entry with no _meta = %q, want empty", got[2].Status)
	}
}

// The wire tag is a namespaced literal, so a typo in it would silently yield no
// badge for every row. Assert the exact key upstream publishes.
func TestNormaliseRegistryResponse_MetaKeyIsTheNamespacedOne(t *testing.T) {
	deprecated := []byte(`{"servers":[{"server":{"name":"x",
	  "remotes":[{"type":"http","url":"https://x"}]},
	  "_meta":{"io.modelcontextprotocol.registry/official":{"status":"deprecated"}}}]}`)
	wrongKey := []byte(`{"servers":[{"server":{"name":"x",
	  "remotes":[{"type":"http","url":"https://x"}]},
	  "_meta":{"official":{"status":"deprecated"}}}]}`)

	if got, err := normaliseRegistryResponse(deprecated, maxSearchLimit); err != nil || len(got.Servers) != 1 || got.Servers[0].Status != "deprecated" {
		t.Errorf("namespaced key not read: %+v, %v", got, err)
	}
	if got, err := normaliseRegistryResponse(wrongKey, maxSearchLimit); err != nil || len(got.Servers) != 1 || got.Servers[0].Status != "" {
		t.Errorf("an unnamespaced key must not be read: %+v, %v", got, err)
	}
}

func TestNormaliseRegistryResponse_SkipsUnsupportedPackages(t *testing.T) {
	body := []byte(`{
		"servers": [
			{"server":{
				"name":"io.github.ex/stdio-npm","version":"1.0.0",
				"packages":[
					{"registryType":"npm","identifier":"@foo/bar","version":"1.2.3",
					 "transport":{"type":"stdio"},
					 "environmentVariables":[{"name":"T","isRequired":true,"isSecret":true}]},
					{"registryType":"oci","identifier":"docker.io/foo/bar:1.0"},
					{"registryType":"pypi","identifier":"irrelevant"}
				]
			}},
			{"server":{"name":"io.github.ex/remote","version":"0.1.0",
				"remotes":[{"type":"streamable-http","url":"https://x","headers":[
					{"name":"Authorization","value":"Bearer {key}","isSecret":true}
				]}]
			}},
			{"server":{"name":"io.github.ex/empty"}}
		]
	}`)

	res, err := normaliseRegistryResponse(body, maxSearchLimit)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse: %v", err)
	}
	got := res.Servers
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after filter, got %d", len(got))
	}
	// The dropped row is counted, not forgotten: it is what lets the browser
	// say "matched, but not installable here" instead of "no results".
	if res.Filtered != 1 {
		t.Errorf("Filtered = %d, want 1 (the schema-only publication)", res.Filtered)
	}
	if res.Truncated {
		t.Error("Truncated = true for three rows under the limit with no cursor, want false")
	}

	// First: stdio + npm package only (pypi dropped).
	first := got[0]
	if first.Name != "io.github.ex/stdio-npm" {
		t.Errorf("first name = %q", first.Name)
	}
	if len(first.Packages) != 1 || first.Packages[0].RegistryType != "npm" {
		t.Errorf("expected exactly 1 npm package, got %#v", first.Packages)
	}
	if len(first.Packages[0].EnvVars) != 1 {
		t.Errorf("expected 1 env var, got %d", len(first.Packages[0].EnvVars))
	}

	// Second: streamable-http normalised to "http".
	second := got[1]
	if len(second.Remotes) != 1 || second.Remotes[0].Type != "http" {
		t.Errorf("streamable-http not normalised; got %#v", second.Remotes)
	}
	if len(second.Remotes[0].Headers) != 1 || second.Remotes[0].Headers[0].Name != "Authorization" {
		t.Errorf("header not preserved; got %#v", second.Remotes[0].Headers)
	}
}

// A body that is not JSON, or is JSON with no servers list, is an ERROR the
// handler answers with a 5xx, never a 200 with zero servers: on the wire that
// is byte-identical to a real empty result, so the browser tells the user the
// registry had nothing matching their query and offers no Retry, and the
// empty answer would sit in the 60-second cache on top. Only an empty ARRAY is
// an empty result. The presence check is on the key, not the count.
func TestNormaliseRegistryResponse_BadJSON(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "malformed_json", body: `{broken`, wantErr: true},
		{name: "cdn_error_page", body: `<html><body>502 Bad Gateway</body></html>`, wantErr: true},
		{name: "renamed_top_level_key", body: `{"results":[{"server":{"name":"x"}}]}`, wantErr: true},
		{name: "empty_object", body: `{}`, wantErr: true},
		{name: "servers_null", body: `{"servers":null}`, wantErr: true},
		{name: "servers_empty_array", body: `{"servers":[]}`, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normaliseRegistryResponse([]byte(tc.body), maxSearchLimit)
			if (err != nil) != tc.wantErr {
				t.Fatalf("normaliseRegistryResponse(%q) err = %v, wantErr %v", tc.body, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(got.Servers) != 0 || got.Filtered != 0 || got.Truncated {
				t.Errorf("normaliseRegistryResponse(%q) = %+v, want zero servers, nothing filtered, not truncated", tc.body, got)
			}
		})
	}
}

// A body that is JSON but carries no servers list is the shape-drift case
// (an upstream rename), and it gets its own sentinel so the handler's Warn
// names it rather than reporting a decode of valid JSON as a failure to parse.
func TestNormaliseRegistryResponse_ShapeDriftIsTheShapeError(t *testing.T) {
	_, err := normaliseRegistryResponse([]byte(`{"results":[]}`), maxSearchLimit)
	if !errors.Is(err, errRegistryShape) {
		t.Errorf("normaliseRegistryResponse({\"results\":[]}) err = %v, want errRegistryShape", err)
	}
}

func TestNormaliseRegistryResponse_NoUsablePaths(t *testing.T) {
	body := []byte(`{"servers":[
		{"server":{"name":"x","packages":[{"registryType":"pypi","identifier":"y"}]}}
	]}`)
	got, err := normaliseRegistryResponse(body, maxSearchLimit)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse: %v", err)
	}
	if len(got.Servers) != 0 {
		t.Errorf("expected entry to be skipped; got %d", len(got.Servers))
	}
	if got.Filtered != 1 {
		t.Errorf("Filtered = %d, want 1: the skipped entry is reported, not lost", got.Filtered)
	}
}

// The sentinel row is the proof of a cut and nothing else: it is neither
// shown nor counted as filtered, whatever it holds. The cut is decided
// before the filter runs, so a limit of two over three rows answers two.
func TestNormaliseRegistryResponse_SentinelRowSetsTruncated(t *testing.T) {
	// Three installable rows for a caller who asked for two.
	body := buildRegistryPayload(3)

	got, err := normaliseRegistryResponse(body, 2)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse: %v", err)
	}
	if !got.Truncated {
		t.Error("Truncated = false with a row past the limit, want true")
	}
	if len(got.Servers) != 2 {
		t.Errorf("Servers = %d, want the 2 asked for", len(got.Servers))
	}
	if got.Filtered != 0 {
		t.Errorf("Filtered = %d, want 0: the sentinel is not a dropped row", got.Filtered)
	}

	// Exactly the limit, no cursor: nothing says more exist.
	got, err = normaliseRegistryResponse(body, 3)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse at the limit: %v", err)
	}
	if got.Truncated {
		t.Error("Truncated = true for exactly limit rows and no cursor, want false")
	}
	if len(got.Servers) != 3 {
		t.Errorf("Servers = %d, want all 3", len(got.Servers))
	}
}

// An uninstallable sentinel row must not leak into Filtered: the caller never
// asked for it, so it is not a row the filter took away from them.
func TestNormaliseRegistryResponse_UninstallableSentinelIsNotFiltered(t *testing.T) {
	body := []byte(`{"servers":[
		{"server":{"name":"a","remotes":[{"type":"sse","url":"https://a"}]}},
		{"server":{"name":"b","packages":[{"registryType":"pypi","identifier":"b"}]}}
	]}`)
	got, err := normaliseRegistryResponse(body, 1)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse: %v", err)
	}
	if !got.Truncated || len(got.Servers) != 1 || got.Filtered != 0 {
		t.Errorf("got %+v, want 1 server, truncated, nothing filtered", got)
	}
}

// metadata.nextCursor is a second witness: it sets Truncated on its own
// when upstream answers fewer rows than the sentinel needs, and its absence
// never clears a cut the row count proved.
func TestNormaliseRegistryResponse_NextCursorIsASecondWitness(t *testing.T) {
	withCursor := []byte(`{"servers":[
		{"server":{"name":"a","remotes":[{"type":"sse","url":"https://a"}]}}
	],"metadata":{"nextCursor":"a:1.0.0","count":1}}`)
	got, err := normaliseRegistryResponse(withCursor, 5)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse: %v", err)
	}
	if !got.Truncated {
		t.Error("Truncated = false with a nextCursor and rows under the limit, want true")
	}

	emptyCursor := []byte(`{"servers":[
		{"server":{"name":"a","remotes":[{"type":"sse","url":"https://a"}]}}
	],"metadata":{"nextCursor":"","count":1}}`)
	got, err = normaliseRegistryResponse(emptyCursor, 5)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse: %v", err)
	}
	if got.Truncated {
		t.Error("Truncated = true for an empty nextCursor and rows under the limit, want false")
	}
}

// upstreamGolden is one real reply from registry.modelcontextprotocol.io,
// captured 2026-09-12 with `GET /v0.1/servers?search=filesystem&limit=6` and
// re-indented; nothing else about it was edited. It is the upstream shape
// vibekit does not own: a field the decoder stops carrying, or a key upstream
// renames, fails here rather than decoding to an empty list.
const upstreamGolden = "registry_search_upstream.json"

func readUpstreamGolden(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", upstreamGolden))
	if err != nil {
		t.Fatalf("read %s: %v", upstreamGolden, err)
	}
	return body
}

// The golden has six rows and a nextCursor, so at a limit of six the row count
// proves nothing and only the cursor says more exist; at five the sixth row is
// the sentinel. Every field the browser renders is asserted off the same reply.
func TestNormaliseRegistryResponse_DecodesTheUpstreamGolden(t *testing.T) {
	body := readUpstreamGolden(t)

	got, err := normaliseRegistryResponse(body, 6)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse(golden, 6): %v", err)
	}
	if len(got.Servers) != 6 {
		t.Fatalf("Servers = %d, want all 6 rows of the golden", len(got.Servers))
	}
	if got.Filtered != 0 {
		t.Errorf("Filtered = %d, want 0: every row in the golden has an installable path", got.Filtered)
	}
	if !got.Truncated {
		t.Error("Truncated = false at exactly the golden's row count, want true from its nextCursor")
	}

	first := got.Servers[0]
	if first.Name != "com.pulsemcp/remote-filesystem" || first.Version != "0.1.2" {
		t.Errorf("first row = %q %q, want com.pulsemcp/remote-filesystem 0.1.2", first.Name, first.Version)
	}
	if first.Repository != "https://github.com/pulsemcp/mcp-servers" {
		t.Errorf("first repository = %q", first.Repository)
	}
	if first.Status != "" {
		t.Errorf("first status = %q, want empty for an active row", first.Status)
	}
	if len(first.Packages) != 1 {
		t.Fatalf("first packages = %d, want 1", len(first.Packages))
	}
	pkg := first.Packages[0]
	if pkg.RegistryType != "npm" || pkg.Identifier != "remote-filesystem-mcp-server" || pkg.Version != "0.1.2" {
		t.Errorf("first package = %+v", pkg)
	}
	if len(pkg.EnvVars) != 8 {
		t.Fatalf("first package env vars = %d, want 8", len(pkg.EnvVars))
	}
	if v := pkg.EnvVars[0]; v.Name != "GCS_BUCKET" || !v.Required || v.Secret {
		t.Errorf("env var 0 = %+v, want GCS_BUCKET required and not secret", v)
	}
	if v := pkg.EnvVars[3]; v.Name != "GCS_PRIVATE_KEY" || v.Required || !v.Secret {
		t.Errorf("env var 3 = %+v, want GCS_PRIVATE_KEY secret and not required", v)
	}

	// Row 3 declares an npm AND an oci package: the oci one is dropped, the
	// row itself survives on its npm package.
	mixed := got.Servers[3]
	if mixed.Name != "io.github.Digital-Defiance/mcp-filesystem" || mixed.Version != "0.1.0" {
		t.Errorf("row 3 = %q %q", mixed.Name, mixed.Version)
	}
	if len(mixed.Packages) != 1 || mixed.Packages[0].Identifier != "@ai-capabilities-suite/mcp-filesystem" {
		t.Errorf("row 3 packages = %+v, want the npm package alone", mixed.Packages)
	}

	// The last row is an sse remote with a secret header.
	remote := got.Servers[5]
	if remote.Name != "io.github.Evozim/chroot-filesystem-jail-mcp" {
		t.Errorf("row 5 = %q", remote.Name)
	}
	if len(remote.Remotes) != 1 || remote.Remotes[0].Type != "sse" ||
		remote.Remotes[0].URL != "https://api.m2mcent.com/chroot-filesystem-jail-mcp/sse" {
		t.Fatalf("row 5 remotes = %+v", remote.Remotes)
	}
	if h := remote.Remotes[0].Headers; len(h) != 1 || h[0].Name != "Payment-Signature" || !h[0].Secret || h[0].Required {
		t.Errorf("row 5 headers = %+v, want one secret optional Payment-Signature", h)
	}

	cut, err := normaliseRegistryResponse(body, 5)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse(golden, 5): %v", err)
	}
	if !cut.Truncated || len(cut.Servers) != 5 {
		t.Errorf("at limit 5: %d servers, truncated=%v; want 5 and true from the sentinel row", len(cut.Servers), cut.Truncated)
	}
	if cut.Servers[4].Name == remote.Name {
		t.Error("the sixth row was shown at a limit of five")
	}
}

func buildRegistryPayload(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"servers":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"server":{"name":"io.github.example/server-%d","title":"Server %d","description":"A test MCP server","version":"1.0.%d","repository":{"url":"https://github.com/example/server-%d"},"packages":[{"registryType":"npm","identifier":"@example/server-%d","version":"1.0.%d","transport":{"type":"stdio"},"environmentVariables":[{"name":"API_KEY","description":"API key","isRequired":true,"isSecret":true}]},{"registryType":"pypi","identifier":"server-%d"}],"remotes":[{"type":"streamable-http","url":"https://server-%d.example.com","headers":[{"name":"Authorization","value":"Bearer {key}","isRequired":true,"isSecret":true}]}]}}`, i, i, i, i, i, i, i, i)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func BenchmarkNormaliseRegistryResponse(b *testing.B) {
	for _, n := range []int{5, 20, 50} {
		payload := buildRegistryPayload(n)
		b.Run(fmt.Sprintf("servers=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = normaliseRegistryResponse(payload, maxSearchLimit)
			}
		})
	}
}

// retryAfterSeconds is the one place upstream's header reaches the browser,
// so its three forms and its ceiling are pinned here.
func TestRetryAfterSeconds(t *testing.T) {
	now := mustParseHTTPTime(t, "Sat, 12 Sep 2026 22:00:00 GMT")
	cases := []struct {
		name   string
		header string
		want   int
	}{
		{name: "absent", header: "", want: 0},
		{name: "delay_seconds", header: "37", want: 37},
		{name: "delay_seconds_padded", header: " 37 ", want: 37},
		{name: "zero", header: "0", want: 0},
		{name: "negative", header: "-5", want: 0},
		{name: "clamped_to_ceiling", header: "86400", want: 300},
		{name: "http_date_ahead", header: "Sat, 12 Sep 2026 22:00:45 GMT", want: 45},
		{name: "http_date_past", header: "Sat, 12 Sep 2026 21:59:00 GMT", want: 0},
		{name: "http_date_far_ahead", header: "Sun, 13 Sep 2026 22:00:00 GMT", want: 300},
		{name: "garbage", header: "soon", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryAfterSeconds(tc.header, now); got != tc.want {
				t.Errorf("retryAfterSeconds(%q) = %d, want %d", tc.header, got, tc.want)
			}
		})
	}
}

func mustParseHTTPTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := http.ParseTime(s)
	if err != nil {
		t.Fatalf("Setup: parse %q: %v", s, err)
	}
	return at
}
