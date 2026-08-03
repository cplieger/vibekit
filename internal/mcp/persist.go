package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cplieger/atomicfile/v2"
)

// UnmarshalJSON validates the transport field at the JSON parse boundary,
// rejecting unknown transport values early rather than letting them flow
// through to runtime dispatch. All three transports (stdio, http, sse) are
// accepted as first-class values and preserved verbatim — "sse" is no
// longer folded into "http", since KAS accepts a distinct SSE entry over
// the v3 wire (see the Transport doc in store.go).
func (s *Server) UnmarshalJSON(data []byte) error {
	type serverAlias Server
	var raw serverAlias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Transport != "" {
		parsed, err := ParseTransport(string(raw.Transport))
		if err != nil {
			return err
		}
		raw.Transport = parsed
	}
	*s = Server(raw)
	return nil
}

// file is the on-disk schema: versioned envelope + ordered list.
type file struct {
	Servers []*Server `json:"servers"`
	Version int       `json:"version"`
}

const fileVersion = 1

// persist writes vibekit's own record and then RENDERS KAS's config file from
// it. Both or neither: a successful vibekit write followed by a failed KAS write
// would leave the UI showing a server the agent cannot see, which is the exact
// confusion the file adoption removes. The caller rolls its in-memory mutation
// back on error.
//
// MUST be called with s.mu held for writing. It reads s.servers directly and
// takes no lock of its own — sync.RWMutex is not reentrant, and every call site
// already holds the write lock across its mutation and this write so that memory
// and disk cannot disagree.
func (s *Store) persist(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f := file{Version: fileVersion, Servers: s.servers}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("%w mcp.json: %w", ErrPersistMarshal, err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("%w: %w", ErrPersistWrite, err)
	}
	return s.writeKASConfig(ctx, s.servers)
}
