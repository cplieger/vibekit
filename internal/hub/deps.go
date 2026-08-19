package hub

import (
	"context"
)

// The interfaces below are the hub's DEPENDENCY contracts: what it calls on the
// collaborators the composition root hands it. Each is declared HERE, at the
// consumer, rather than in a shared contract package, and each names only the
// methods this package invokes — which is what keeps a test fake small enough to
// be obviously correct and stops an unrelated method from widening a contract
// every package in the build then has to import.
//
// The width arithmetic is stated per interface. Where the hub genuinely uses all
// of what an implementation offers, that is said plainly: the placement is the
// win in that case, because a contract with one consumer has no business in a
// package everything imports.

// mcpNameSets is the MCP server-name census: which of the user's servers are
// switched on, which its config holds at all, and which are reachable through
// the config file including the powers block vibekit never writes. *mcp.Store
// satisfies it.
//
// 3 of the 3 methods the store exports for this, and the hub needs all three
// because it reasons from their NESTING rather than from any one of them:
// enabled means record and tag OriginUser; configured-but-not-enabled is the
// only case that drops a frame; in AllNames but not configured can only have
// come from a Power; in none of them is a source vibekit cannot see. One set
// alone cannot separate "the user turned this off" from "vibekit never
// configured this", and those two need opposite treatment.
//
// AllNames is the only member that touches disk, which is why the classifier
// consults it last.
type mcpNameSets interface {
	// EnabledNames returns the set of enabled server names.
	EnabledNames(ctx context.Context) map[string]struct{}
	// ConfiguredNames returns every server name vibekit's OWN config holds,
	// enabled or disabled.
	ConfiguredNames(ctx context.Context) map[string]struct{}
	// AllNames returns every server name reachable through the config file
	// vibekit renders, including the powers block KAS reads out of it.
	// Best-effort: a hand-edit can make that file unparseable, so a name this
	// set misses is reported OriginUnknown rather than dropped.
	AllNames(ctx context.Context) map[string]struct{}
}
