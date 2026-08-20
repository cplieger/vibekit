// Package vibekit is the app's wire and domain TYPE vocabulary: the shapes that
// cross the server's HTTP and SSE boundaries, and the shapes its components pass
// each other. It is named for the app because that is what it holds — vibekit's
// nouns, not a layer.
//
// It declares 158 types, 13 methods on those types, the constants that enumerate
// them, and eleven functions, every one of which either constructs one of these
// types (NewEvent, ChatSubject, PRSubject, the three PermissionOutcome*
// builders, TextBlock) or maps between them (DecisionKindForEvent,
// WorkingLabelForKind, ModelServed).
//
// Three things are deliberately absent, and each has a home elsewhere:
//
//   - NO INTERFACES. A Go interface belongs at its consumer, named for what that
//     consumer calls, so every contract is declared where it is used and each
//     removal is recorded at the site it left.
//   - NO BEHAVIOUR. Nothing here reads a file, opens a socket, spawns a process,
//     sanitizes a string or validates an identifier. Behaviour lives in packages
//     named for what they do — internal/sanitize, internal/ids,
//     internal/rpcerr, internal/modeltext, internal/procout, internal/ansitext —
//     or at its one consumer.
//   - NO HTTP PLUMBING. The reply helpers are internal/httpreply; atomic file
//     I/O is the external cplieger/atomicfile.
//
// So an addition here is a TYPE. A function that is not a constructor or a
// mapper over these types belongs in one of those packages instead.
//
// The types are what keep the dependency graph acyclic — vibekit.ServerEvent
// crosses the runtime/translate seam, and agent imports translate rather than the
// reverse, so relocating that type would close a real cycle. cmd/wire-codegen
// walks these types through internal/wirespec to generate the TypeScript
// client's decoders, which is the second reason they have one obvious home.
// Codegen is NOT a reason to keep them together, though: wiregen resolves a type
// wherever it lives, and eleven of vibekit's registrations already point outside
// this package.
//
// This package imports no other vibekit-internal package. Implementation
// packages import it, never the reverse.
package vibekit
