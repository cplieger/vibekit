// Package api is vibekit's wire and domain TYPE surface: the shapes that cross
// the server's HTTP and SSE boundaries and the shapes its components pass each
// other. It declares no interfaces.
//
// The types are what keep the dependency graph acyclic — api.ServerEvent crosses
// the hub/translate seam, and hub imports translate rather than the reverse, so
// relocating that type would close a real cycle. cmd/wire-codegen walks these
// types through internal/wirespec to generate the TypeScript client's decoders,
// which is the second reason they have one obvious home. Codegen is NOT a reason
// to keep them together, though: wiregen resolves a type wherever it lives, and
// eleven of vibekit's registrations already point outside this package.
//
// The CONTRACTS are declared at their consumers instead, which is where a Go
// interface belongs. Each named the whole of what its widest implementation
// offered; each consumer now names only what it calls:
//
//	ChatStore       -> server 1, translate 3, hub's coordinator 4, command 5, of 9
//	ACPBridge       -> hub, at seven widths from 1 to 14
//	PushService     -> hub 4, server 2, forges 2, of 8
//	CommandBridge   -> command, split three ways from 1 to 12
//	RouteHandler    -> server, its one real consumer, at 1
//	MCPConfig       -> hub, 3 of 3
//	PolicyProvider  -> server, 2 of 2
//	Hub             -> server, 3 of *hub.Hub
//	Broadcaster     -> chat, forges and command, 1 each
//	UtilityPrompter -> server and git, 1 each
//
// Each removal is recorded at the site it left, with what nobody was calling.
//
// HTTP request and response plumbing is NOT here: WriteJSON, BadRequest,
// MethodNotAllowed, DecodeJSON and the rest live in internal/httpreply, which
// imports nothing from this package. Atomic file I/O (SaveBytes, bounded reads)
// lives in the external cplieger/atomicfile package.
//
// This package imports no other vibekit-internal package. Implementation
// packages import it, never the reverse.
package api
