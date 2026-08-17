package mcp

import (
	"fmt"
	"strings"
	"testing"
)

func FuzzNormaliseRegistryResponse(f *testing.F) {
	f.Add([]byte(`{"servers":[{"server":{"name":"x","packages":[{"registryType":"npm","identifier":"y","transport":{"type":"stdio"}}]}}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"servers":[]}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"servers":[{"server":{"name":"","remotes":[{"type":"streamable-http","url":"u"}]}}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		got := normaliseRegistryResponse(data)
		for i := range got {
			if len(got[i].Packages) == 0 && len(got[i].Remotes) == 0 {
				t.Errorf("entry %d has no packages and no remotes but was not filtered", i)
			}
			for j, r := range got[i].Remotes {
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

	got := normaliseRegistryResponse(body)
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

	if got := normaliseRegistryResponse(deprecated); len(got) != 1 || got[0].Status != "deprecated" {
		t.Errorf("namespaced key not read: %+v", got)
	}
	if got := normaliseRegistryResponse(wrongKey); len(got) != 1 || got[0].Status != "" {
		t.Errorf("an unnamespaced key must not be read: %+v", got)
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

	got := normaliseRegistryResponse(body)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after filter, got %d", len(got))
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

func TestNormaliseRegistryResponse_BadJSON(t *testing.T) {
	got := normaliseRegistryResponse([]byte("{broken"))
	if len(got) != 0 {
		t.Errorf("expected empty slice on parse failure, got %d", len(got))
	}
}

func TestNormaliseRegistryResponse_NoUsablePaths(t *testing.T) {
	body := []byte(`{"servers":[
		{"server":{"name":"x","packages":[{"registryType":"pypi","identifier":"y"}]}}
	]}`)
	got := normaliseRegistryResponse(body)
	if len(got) != 0 {
		t.Errorf("expected entry to be skipped; got %d", len(got))
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
			for range b.N {
				normaliseRegistryResponse(payload)
			}
		})
	}
}
