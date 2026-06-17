package mcp

// Mutant-killing tests for unit vibekit-u15 (internal/mcp).
//
// Targets three CONDITIONALS_BOUNDARY mutants in validate.go, all of
// which flip a `>` length check to `>=`. Each test pins the EXACT
// boundary (len == cap): the original `>` accepts the at-cap value
// (nil error) while the mutated `>=` rejects it, so the at-cap
// assertion flips under mutation. A just-over case fixes the direction.
// Identifiers are prefixed gk_vibekit_u15_ to avoid colliding with the
// sibling unit (vibekit-u14) that shares this package. Tests only; no
// production code is modified.

import (
	"strings"
	"testing"
)

// validate.go:191:27 CONDITIONALS_BOUNDARY — `len(s.OAuthClientID) > oauthClientIDMax`
// in validateRemote. An OAuthClientID of exactly oauthClientIDMax bytes
// must be accepted: original `>` (256 > 256 is false) lets it through;
// mutated `>=` (256 >= 256 is true) rejects it as "too long".
func Test_gk_vibekit_u15_OAuthClientIDLengthBoundary(t *testing.T) {
	atMax := &Server{
		Transport:     TransportHTTP,
		Name:          "gkok",
		URL:           "https://x.example/mcp",
		OAuthClientID: strings.Repeat("a", oauthClientIDMax),
	}
	if err := Validate(atMax); err != nil {
		t.Errorf("Validate(OAuthClientID len=%d) = %v, want nil (boundary value must be accepted; mutation > to >= rejects it)",
			oauthClientIDMax, err)
	}

	over := &Server{
		Transport:     TransportHTTP,
		Name:          "gkok",
		URL:           "https://x.example/mcp",
		OAuthClientID: strings.Repeat("a", oauthClientIDMax+1),
	}
	err := Validate(over)
	if err == nil {
		t.Fatalf("Validate(OAuthClientID len=%d) = nil, want too-long error", oauthClientIDMax+1)
	}
	if !strings.Contains(err.Error(), "oauth_client_id too long") {
		t.Errorf("Validate(OAuthClientID len=%d) = %q, want substring %q",
			oauthClientIDMax+1, err.Error(), "oauth_client_id too long")
	}
}

// validate.go:210:16 CONDITIONALS_BOUNDARY — `len(pairs) > maxEntries`
// in validateKeyPairs. Exactly maxEntries valid pairs must be accepted:
// original `>` (2 > 2 is false) proceeds and returns nil; mutated `>=`
// (2 >= 2 is true) returns "too many entries". Called directly so the
// boundary is pinned by the maxEntries parameter, not a package cap.
func Test_gk_vibekit_u15_KeyPairsEntryCountBoundary(t *testing.T) {
	const maxEntries = 2
	const maxValue = 1024

	atMax := []KeyPair{
		{Name: "Ga", Value: "v"},
		{Name: "Gb", Value: "v"},
	}
	if err := validateKeyPairs("env", atMax, maxEntries, maxValue, false); err != nil {
		t.Errorf("validateKeyPairs(%d pairs, max %d) = %v, want nil (count at cap must be accepted; mutation > to >= rejects it)",
			len(atMax), maxEntries, err)
	}

	over := []KeyPair{
		{Name: "Ga", Value: "v"},
		{Name: "Gb", Value: "v"},
		{Name: "Gc", Value: "v"},
	}
	err := validateKeyPairs("env", over, maxEntries, maxValue, false)
	if err == nil {
		t.Fatalf("validateKeyPairs(%d pairs, max %d) = nil, want too-many-entries error",
			len(over), maxEntries)
	}
	if !strings.Contains(err.Error(), "too many entries") {
		t.Errorf("validateKeyPairs(%d pairs) = %q, want substring %q",
			len(over), err.Error(), "too many entries")
	}
}

// validate.go:231:20 CONDITIONALS_BOUNDARY — `len(kv.Value) > maxValue`
// in validateKeyPairs. A value of exactly maxValue bytes must be
// accepted: original `>` (4 > 4 is false) returns nil; mutated `>=`
// (4 >= 4 is true) returns "value too long". maxEntries is kept above
// the pair count so the line 210 check passes and the loop reaches 231.
func Test_gk_vibekit_u15_KeyPairsValueLengthBoundary(t *testing.T) {
	const maxEntries = 8
	const maxValue = 4

	atMax := []KeyPair{{Name: "Gk", Value: strings.Repeat("v", maxValue)}}
	if err := validateKeyPairs("env", atMax, maxEntries, maxValue, false); err != nil {
		t.Errorf("validateKeyPairs(value len=%d, max %d) = %v, want nil (value at cap must be accepted; mutation > to >= rejects it)",
			maxValue, maxValue, err)
	}

	over := []KeyPair{{Name: "Gk", Value: strings.Repeat("v", maxValue+1)}}
	err := validateKeyPairs("env", over, maxEntries, maxValue, false)
	if err == nil {
		t.Fatalf("validateKeyPairs(value len=%d, max %d) = nil, want value-too-long error",
			maxValue+1, maxValue)
	}
	if !strings.Contains(err.Error(), "value too long") {
		t.Errorf("validateKeyPairs(value len=%d) = %q, want substring %q",
			maxValue+1, err.Error(), "value too long")
	}
}
