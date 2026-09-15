package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// registrySearchFixture is the envelope of testdata/registry_search_reply.json:
// the two bodies GET /api/mcp/registry/search can answer with, byte-pinned here
// and decoded by static-src/actions/mcp.node.test.ts through the generated
// decodeRegistrySearchResult and decodeRegistrySearchFailure.
type registrySearchFixture struct {
	Comment []string              `json:"_comment"`
	Search  RegistrySearchResult  `json:"search"`
	Failure RegistrySearchFailure `json:"failure"`
}

var registrySearchFixtureComment = []string{
	"The two bodies GET /api/mcp/registry/search answers with: the 200 search reply, produced",
	"by normalising the captured upstream reply in registry_search_upstream.json at a limit of",
	"five (so the sixth row is the sentinel that sets truncated), and the 502 failure body for",
	"an upstream 429 carrying a Retry-After.",
	"",
	"mcp.RegistrySearchResult, its entry family and mcp.RegistrySearchFailure are",
	"wiregen-registered; TestRegistrySearchWireContract (Go) asserts these bytes, and",
	"actions/mcp.node.test.ts (TypeScript) decodes both through the generated decoders.",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/mcp/ -run TestRegistrySearchWireContract",
	"then re-run the TS half: npx vitest --run actions/mcp.node.test.ts (from static-src/).",
}

// TestRegistrySearchWireContract pins the browser-facing shape of both registry
// search bodies to testdata/registry_search_reply.json.
func TestRegistrySearchWireContract(t *testing.T) {
	search, err := normaliseRegistryResponse(readUpstreamGolden(t), 5)
	if err != nil {
		t.Fatalf("normaliseRegistryResponse(upstream golden, 5): %v", err)
	}
	if len(search.Servers) != 5 || !search.Truncated {
		t.Fatalf("Servers = %d, Truncated = %v; want 5 rows with the sixth as the truncation sentinel",
			len(search.Servers), search.Truncated)
	}
	fx := registrySearchFixture{
		Comment: registrySearchFixtureComment,
		Search:  search,
		Failure: classifyRegistryFailure(&upstreamStatusError{status: 429, retryAfter: 30}),
	}
	if fx.Failure.Reason != ReasonRateLimited || fx.Failure.RetryAfter != 30 {
		t.Fatalf("failure = %+v, want rate_limited with retry_after 30", fx.Failure)
	}

	got, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	got = append(got, '\n')

	const path = "testdata/registry_search_reply.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 go test ./internal/mcp/ -run TestRegistrySearchWireContract): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reply drifted from %s.\n--- want (fixture)\n%s\n--- got\n%s\n"+
			"Regenerate with UPDATE_GOLDEN=1 go test ./internal/mcp/ -run TestRegistrySearchWireContract, "+
			"then re-run the TS half: npx vitest --run actions/mcp.node.test.ts (from static-src/).",
			path, want, got)
	}
}
