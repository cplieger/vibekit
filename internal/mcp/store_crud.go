package mcp

// CRUD operations on the MCP server store.
//
// These methods change when the API surface evolves (new fields, new
// operations). Extracted from store.go to keep that file focused on
// construction, persistence, and internal helpers.

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/mcp/prewarm"
)

// List returns a deep copy of every server with secrets masked. Safe to
// serve directly over the wire.
func (s *Store) List(ctx context.Context) []*Server {
	if ctx.Err() != nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Server, 0, len(s.servers))
	for _, sv := range s.servers {
		out = append(out, maskedCopy(sv))
	}
	return out
}

// EnabledRaw returns a deep copy of every enabled server with secrets
// intact. Used when constructing the mcpServers parameter for kiro-cli.
// Not exposed over HTTP.
func (s *Store) EnabledRaw(ctx context.Context) []*Server {
	if ctx.Err() != nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Server, 0, len(s.servers))
	for _, sv := range s.servers {
		if !sv.Enabled {
			continue
		}
		out = append(out, rawCopy(sv))
	}
	return out
}

// Get returns a masked copy of one server, or nil.
func (s *Store) Get(_ context.Context, id ServerID) *Server {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sv := range s.servers {
		if sv.ID == id {
			return maskedCopy(sv)
		}
	}
	return nil
}

// Create inserts a new server, assigning a fresh ID and timestamps.
// Validates shape before persisting. Returns a masked copy of the
// stored record.
func (s *Store) Create(ctx context.Context, in *Server) (*Server, error) {
	now := time.Now().UnixMilli()
	rec := &Server{
		ID:                newID(),
		Transport:         in.Transport,
		Name:              strings.TrimSpace(in.Name),
		Command:           in.Command,
		Args:              append([]string(nil), in.Args...),
		Env:               copyPairs(in.Env),
		URL:               in.URL,
		Headers:           copyPairs(in.Headers),
		DisabledTools:     append([]string(nil), in.DisabledTools...),
		AutoApprove:       append([]string(nil), in.AutoApprove...),
		OAuthClientID:     strings.TrimSpace(in.OAuthClientID),
		OAuthClientSecret: strings.TrimSpace(in.OAuthClientSecret),
		Prewarm:           in.Prewarm,
		Enabled:           in.Enabled,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := Validate(rec); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.hasNameLocked(rec.Name, "") {
		s.mu.Unlock()
		return nil, ErrNameConflict
	}
	s.servers = append(s.servers, rec)
	if err := s.persist(ctx); err != nil {
		// Roll back the in-memory append so memory matches disk.
		s.servers = s.servers[:len(s.servers)-1]
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	slog.Info("mcp: server created", "id", rec.ID, "name", rec.Name,
		"transport", rec.Transport, "enabled", rec.Enabled)
	s.notifyChange(ctx)
	return maskedCopy(rec), nil
}

// Update replaces one server by id. Fields whose secret value equals
// SecretMask are preserved from the existing record, so a client can
// edit non-secret fields without re-submitting the secret. Returns a
// masked copy of the stored record.
func (s *Store) Update(ctx context.Context, id ServerID, in *Server) (*Server, error) {
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	existing := s.servers[idx]
	rec := &Server{
		ID:                existing.ID,
		Transport:         in.Transport,
		Name:              strings.TrimSpace(in.Name),
		Command:           in.Command,
		Args:              append([]string(nil), in.Args...),
		Env:               mergeSecrets(in.Env, existing.Env),
		URL:               in.URL,
		Headers:           mergeSecrets(in.Headers, existing.Headers),
		DisabledTools:     append([]string(nil), in.DisabledTools...),
		AutoApprove:       preserveNilSlice(in.AutoApprove, existing.AutoApprove),
		OAuthClientID:     strings.TrimSpace(in.OAuthClientID),
		OAuthClientSecret: mergeSecret(strings.TrimSpace(in.OAuthClientSecret), existing.OAuthClientSecret),
		Prewarm:           in.Prewarm,
		Enabled:           in.Enabled,
		CreatedAt:         existing.CreatedAt,
		UpdatedAt:         time.Now().UnixMilli(),
	}
	// Validate runs under s.mu because rec.Env and rec.Headers were just
	// resolved against `existing` via mergeSecrets, which only makes
	// sense with the current stored set. Cheap regex + length checks; no I/O.
	if err := Validate(rec); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if s.hasNameLocked(rec.Name, id) {
		s.mu.Unlock()
		return nil, ErrNameConflict
	}
	s.servers[idx] = rec
	if err := s.persist(ctx); err != nil {
		s.servers[idx] = existing
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	slog.Info("mcp: server updated", "id", rec.ID, "name", rec.Name,
		"transport", rec.Transport, "enabled", rec.Enabled)
	s.notifyChange(ctx)
	return maskedCopy(rec), nil
}

// SetEnabled flips the enabled flag for one server. Returns the updated
// masked copy.
func (s *Store) SetEnabled(ctx context.Context, id ServerID, enabled bool) (*Server, error) {
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil, ErrNotFound
	}
	if s.servers[idx].Enabled == enabled {
		out := maskedCopy(s.servers[idx])
		s.mu.Unlock()
		return out, nil
	}
	// Snapshot the struct by value so rollback restores the pre-mutation
	// scalar fields (Enabled, UpdatedAt). The Args/Env/Headers slices
	// aren't mutated here, so sharing the backing arrays with the live
	// record is safe; any future mutation that rewrites those slices
	// must deep-copy first, matching Update's mergeSecrets pattern.
	before := *s.servers[idx]
	s.servers[idx].Enabled = enabled
	s.servers[idx].UpdatedAt = time.Now().UnixMilli()
	if err := s.persist(ctx); err != nil {
		s.servers[idx] = &before
		s.mu.Unlock()
		return nil, err
	}
	out := maskedCopy(s.servers[idx])
	s.mu.Unlock()
	slog.Info("mcp: server enabled toggled", "id", id, "enabled", enabled)
	s.notifyChange(ctx)
	return out, nil
}

// Delete removes a server by id. No-op if not found.
func (s *Store) Delete(ctx context.Context, id ServerID) error {
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return nil
	}
	removed := s.servers[idx]
	s.servers = slices.Delete(s.servers, idx, idx+1)
	if err := s.persist(ctx); err != nil {
		// Roll back.
		s.servers = slices.Insert(s.servers, idx, removed)
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	slog.Info("mcp: server deleted", "id", id)
	s.notifyChange(ctx)
	return nil
}

// SetKnownTools updates the cached tool list for the named server.
// Called when kiro-cli reports commands/available with per-server tool
// names. Persists to mcp.json so the UI can show suggestions even when
// the server is disconnected. No-op if the server name isn't found, or
// if the incoming set is identical to what's already stored.
func (s *Store) SetKnownTools(ctx context.Context, name string, tools []string) {
	s.mu.Lock()
	var found *Server
	for _, srv := range s.servers {
		if srv.Name == name {
			found = srv
			break
		}
	}
	if found == nil {
		s.mu.Unlock()
		return
	}
	// Change-detection: kiro-cli re-reports the same known-tools set on
	// every bridge spawn/reconnect, so without this guard opening N chats
	// would fire N identical disk writes + mcp_config_changed broadcasts +
	// npx prewarm passes for unchanged data. Skip when the set is
	// unchanged; only a real change persists + notifies (comparison runs
	// under the lock, before the mutation).
	if slices.Equal(found.KnownTools, tools) {
		s.mu.Unlock()
		return
	}
	found.KnownTools = tools
	s.mu.Unlock()

	// Persist outside the lock: KnownTools is a non-critical UI cache
	// field, so concurrent readers are not blocked during disk I/O.
	// On failure the in-memory state is ahead of disk — acceptable for
	// suggestion data that will be re-populated on next bridge start.
	if err := s.persist(ctx); err != nil {
		slog.Warn("mcp: persist after SetKnownTools failed", "server", name, "error", err)
		return
	}
	s.notifyChange(ctx)
}

// EnabledServers returns the prewarm-relevant view of enabled servers.
// Satisfies prewarm.ServerLister so the Store can be passed directly
// to prewarm.NewRunner without an adapter.
func (s *Store) EnabledServers(ctx context.Context) []prewarm.ServerInfo {
	servers := s.EnabledRaw(ctx)
	out := make([]prewarm.ServerInfo, len(servers))
	for i, srv := range servers {
		out[i] = prewarm.ServerInfo{
			Prewarm:   srv.Prewarm,
			Enabled:   srv.Enabled,
			Transport: string(srv.Transport),
			Command:   srv.Command,
			Args:      srv.Args,
		}
	}
	return out
}
