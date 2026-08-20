package mcp

// CRUD operations on the MCP server store.
//
// These methods change when the API surface evolves (new fields, new
// operations). Extracted from store.go to keep that file focused on
// construction, persistence, and internal helpers.

import (
	"context"
	"errors"
	"fmt"
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
	// A reinstall of an identical spec is a no-op, not a conflict. The 409 it
	// replaces had exactly one workaround — delete, then re-add — which threw
	// away every API key the user had typed in, because DELETE removes the record
	// and its stored secrets outright. Returning the existing record instead
	// preserves them, and skipping the persist matters beyond tidiness: a write
	// re-renders KAS's config file, whose watcher emits a status notification
	// back into the agent.
	if existing := s.findByNameLocked(rec.Name); existing != nil {
		if !sameSpec(existing, rec) {
			s.mu.Unlock()
			return nil, ErrNameConflict
		}
		out := maskedCopy(existing)
		s.mu.Unlock()
		slog.Info("mcp: server already configured with this spec; keeping the stored one",
			"id", existing.ID, "name", existing.Name)
		return out, nil
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
	s.notifyChange()
	return maskedCopy(rec), nil
}

// ImportOutcome names what one entry of a pasted block did.
type ImportOutcome string

// ImportCreated and ImportUnchanged are the two outcomes of one import entry.
// There is no "updated": an entry naming a configured server either matches its
// spec (unchanged) or conflicts with it (the whole paste fails), because
// silently rewriting a server the user has since edited is not what pasting a
// README asked for.
const (
	ImportCreated   ImportOutcome = "created"
	ImportUnchanged ImportOutcome = "unchanged"
)

// ImportResult is one entry's outcome, in the order the block declared it.
type ImportResult struct {
	Name    string        `json:"name"`
	Outcome ImportOutcome `json:"outcome"`
}

// ImportServers creates every server of one pasted block, or none of them.
//
// ALL-OR-NOTHING is the decision, and it is about the artifact rather than the
// store: the block is one thing the user copied out of one README, so installing
// three of five and reporting the other two leaves them diffing the UI against
// the document to work out what landed. The store gives no reason to prefer
// partial success either — one atomicfile write covers N servers — and because
// an identical re-paste is a no-op (see Create), correcting the block and
// pasting it again costs nothing and re-lands the entries that were fine.
//
// A name already configured with the SAME spec is `unchanged`, which is what
// makes the common case work: a block naming three servers where one is already
// installed. A name configured with a DIFFERENT spec fails the paste, because
// the alternative is a POST that silently overwrites.
func (s *Store) ImportServers(ctx context.Context, in []*Server) ([]ImportResult, error) {
	if len(in) == 0 {
		return nil, errors.New("no servers to connect")
	}
	if len(in) > maxImportServers {
		return nil, fmt.Errorf("too many servers in one paste (%d, max %d)", len(in), maxImportServers)
	}
	// Validate and de-duplicate before taking the lock: both are pure, and a
	// block that cannot land should not have made every other caller wait.
	seen := make(map[string]struct{}, len(in))
	for _, sv := range in {
		if err := Validate(sv); err != nil {
			return nil, fmt.Errorf("server %q: %w", sv.Name, err)
		}
		key := strings.ToLower(sv.Name)
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("%q %w", sv.Name, errImportDuplicate)
		}
		seen[key] = struct{}{}
	}

	now := time.Now().UnixMilli()
	s.mu.Lock()
	before := s.servers
	results := make([]ImportResult, 0, len(in))
	created := 0
	for _, sv := range in {
		res, err := s.importOneLocked(sv, now)
		if err != nil {
			s.servers = before
			s.mu.Unlock()
			return nil, err
		}
		if res.Outcome == ImportCreated {
			created++
		}
		results = append(results, res)
	}
	if created == 0 {
		s.mu.Unlock()
		slog.Info("mcp: import matched the stored set; nothing written", "servers", len(in))
		return results, nil
	}
	if err := s.persist(ctx); err != nil {
		s.servers = before
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	slog.Info("mcp: servers imported", "created", created, "entries", len(in))
	s.notifyChange()
	return results, nil
}

// importOneLocked appends one entry, or reports that the stored record already
// says the same thing. Caller must hold the write lock.
func (s *Store) importOneLocked(sv *Server, now int64) (ImportResult, error) {
	if existing := s.findByNameLocked(sv.Name); existing != nil {
		if !sameSpec(existing, sv) {
			return ImportResult{}, fmt.Errorf(
				"%w: %q is configured with a different command or url; rename the entry or edit the existing integration",
				ErrNameConflict, existing.Name,
			)
		}
		return ImportResult{Name: existing.Name, Outcome: ImportUnchanged}, nil
	}
	rec := *sv
	rec.ID = newID()
	rec.Name = strings.TrimSpace(sv.Name)
	rec.Args = append([]string(nil), sv.Args...)
	rec.Env = copyPairs(sv.Env)
	rec.Headers = copyPairs(sv.Headers)
	rec.DisabledTools = append([]string(nil), sv.DisabledTools...)
	rec.AutoApprove = append([]string(nil), sv.AutoApprove...)
	rec.CreatedAt = now
	rec.UpdatedAt = now
	s.servers = append(s.servers, &rec)
	return ImportResult{Name: rec.Name, Outcome: ImportCreated}, nil
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
	// Before mergeSecrets, not after: it substitutes a stored value for every
	// masked row it can match, and once that has happened the record is
	// indistinguishable from one the user retyped. See guardOriginChange.
	if err := guardOriginChange(in, existing); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	rec := &Server{
		ID:        existing.ID,
		Transport: in.Transport,
		Name:      strings.TrimSpace(in.Name),
		Command:   in.Command,
		Args:      append([]string(nil), in.Args...),
		Env:       mergeSecrets(in.Env, existing.Env),
		URL:       in.URL,
		Headers:   mergeSecrets(in.Headers, existing.Headers),
		// Both list fields go through preserveNilSlice: an omitted list means
		// "unchanged", not "empty". DisabledTools used to take a raw copy while
		// AutoApprove on the very next line preserved, so any PUT that left
		// disabled_tools out silently re-enabled every tool the user had turned
		// off — the more dangerous direction of the two.
		DisabledTools:     preserveNilSlice(in.DisabledTools, existing.DisabledTools),
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
	s.notifyChange()
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
	s.notifyChange()
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
	// Log the STORED record's id, not the request parameter: identical
	// bytes (the index lookup matched them), but the stored value is
	// vibekit-generated at Create (newID base32), which breaks the
	// user-input taint chain a raw path segment would carry into the log.
	slog.Info("mcp: server deleted", "id", removed.ID)
	s.notifyChange()
	return nil
}

// There is no SetKnownTools. A connected server's tool names arrive on the same
// _kiro/mcp/status notification as its prompts and resources, and they are
// RUNTIME state — they describe what is connected right now, not what the user
// configured. They used to be written into this config file on every status
// notification, which made a notification path do disk I/O and put agent-derived
// data in a user-intent file. They live in the hub's runtime registry now and
// reach the UI through /api/mcp/status, beside the prompts and resources that
// were already there.
//
// Deleting the write also closed a loop the KAS-file adoption would have opened:
// persisting here re-rendered KAS's config, whose watcher re-emitted status,
// which called back in. The identical-set guard stopped it after one extra
// cycle, but the cycle had no reason to exist.

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
