package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"vibekit/internal/fileutil"
)

// UnmarshalJSON validates the transport field at the JSON parse boundary,
// rejecting unknown transport values early rather than letting them flow
// through to runtime dispatch.
func (s *Server) UnmarshalJSON(data []byte) error {
	type serverAlias Server
	var raw serverAlias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Transport != "" {
		if _, err := ParseTransport(string(raw.Transport)); err != nil {
			return err
		}
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

func (s *Store) persist(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f := file{Version: fileVersion, Servers: s.servers}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("%w mcp.json: %w", ErrPersistMarshal, err)
	}
	if err := fileutil.SaveBytes(s.path, data, 0o600); err != nil {
		return fmt.Errorf("%w: %w", ErrPersistWrite, err)
	}
	return nil
}
