// Manager — discovers and lists configured forge providers from
// the CLI tool config files. The CLIs are the source of truth for
// auth state; this manager is a thin read-through with caching.

package forges

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/forges/cliexec"
	"golang.org/x/sync/singleflight"
)

// ConfiguredForge is one connected forge backend, discovered through
// the CLIs' own status subcommands (see discover.go).
type ConfiguredForge struct {
	ID         string `json:"id"`
	Kind       Kind   `json:"kind"`
	Host       string `json:"host"`
	Username   string `json:"username,omitempty"`
	Email      string `json:"email,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	LastProbed int64  `json:"last_probed,omitempty"`
	Connected  bool   `json:"connected"`
	// CLIMissing marks a connection whose backing CLI binary is absent
	// (uninstalled/disabled in Settings → Tools, or a fresh tools volume
	// against a kept config volume) while a configuration for it still
	// exists. The row renders as a warning with a reinstall pointer; it
	// is never probed and cannot be disconnected until the CLI returns.
	CLIMissing bool `json:"cli_missing,omitempty"`
}

// Manager owns the list of configured forges.
type Manager struct {
	cacheAt   time.Time
	refreshSF singleflight.Group
	probeSF   singleflight.Group
	forges    map[string]*ConfiguredForge
	ttl       time.Duration
	mu        sync.RWMutex
}

// NewManager constructs a Manager.
func NewManager() *Manager {
	return &Manager{
		forges: make(map[string]*ConfiguredForge),
		ttl:    30 * time.Second,
	}
}

// MakeID returns the canonical ID for a kind+host pair.
func MakeID(kind Kind, host string) string {
	if host == "" {
		host = kind.DefaultHost()
	}
	return fmt.Sprintf("%s:%s", kind, host)
}

// List returns all configured forges, refreshing from CLI configs if
// the cache is stale.
func (m *Manager) List(ctx context.Context) []ConfiguredForge {
	m.mu.RLock()
	stale := time.Since(m.cacheAt) > m.ttl
	m.mu.RUnlock()
	if stale {
		ch := m.refreshSF.DoChan("refresh", func() (any, error) {
			return nil, m.Refresh(ctx)
		})
		select {
		case <-ch:
		case <-ctx.Done():
			return nil
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ConfiguredForge, 0, len(m.forges))
	for _, f := range m.forges {
		out = append(out, *f)
	}
	slices.SortStableFunc(out, func(a, b ConfiguredForge) int {
		return cmp.Or(
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Host, b.Host),
		)
	})
	return out
}

// Get returns the configured forge with the given ID, or nil.
func (m *Manager) Get(id string) *ConfiguredForge {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.forges[id]
	if !ok {
		return nil
	}
	cp := *f
	return &cp
}

// Refresh re-queries every CLI's own status output and rebuilds the
// forge list. Presence-only: gh's network-tested per-account "state"
// is deliberately not consulted (Probe is the sole network path), and
// gh's own connection test fails fast offline, so the subprocess cost
// per stale TTL stays bounded by CmdTimeout in the worst case.
func (m *Manager) Refresh(ctx context.Context) error {
	out := make(map[string]*ConfiguredForge)

	addGHForges(ctx, out)
	if err := ctx.Err(); err != nil {
		return err
	}
	addGLabForges(out)
	if err := ctx.Err(); err != nil {
		return err
	}
	addTeaForges(ctx, out)

	m.mergeForges(out)
	return nil
}

// addGHForges queries `gh auth status --json hosts` and adds each known
// account to out. A missing gh binary with a surviving configuration
// yields a cli_missing row instead; any other failure logs and leaves
// gh unconfigured for this refresh.
func addGHForges(ctx context.Context, out map[string]*ConfiguredForge) {
	hosts, err := ghAuthHosts(ctx)
	if err != nil {
		if errors.Is(err, cliexec.ErrNotInstalled) {
			addCLIMissingRow(KindGitHub, out)
		} else {
			slog.Warn("forges: gh discovery failed", "error", err)
		}
		return
	}
	for host, login := range hosts {
		id := MakeID(KindGitHub, host)
		out[id] = &ConfiguredForge{
			ID:        id,
			Kind:      KindGitHub,
			Host:      host,
			Username:  login,
			Connected: true,
		}
	}
}

// addGLabForges reads glab's config.yml (read-only — glab ships no JSON
// status output) and adds each authenticated host to out. When the glab
// binary itself is absent, the rows demote to cli_missing: the
// configuration is real, but nothing can act on it.
func addGLabForges(out map[string]*ConfiguredForge) {
	path := cliConfigPath(KindGitLab)
	if path == "" {
		return
	}
	cfg, err := loadGLabConfig(path)
	if err != nil {
		return
	}
	_, lookErr := exec.LookPath("glab")
	cliMissing := lookErr != nil
	for host, entry := range cfg.Hosts {
		if entry.Token == "" {
			continue
		}
		id := MakeID(KindGitLab, host)
		f := &ConfiguredForge{
			ID:        id,
			Kind:      KindGitLab,
			Host:      host,
			Username:  entry.User,
			Connected: !cliMissing,
		}
		if cliMissing {
			f.CLIMissing = true
			f.LastError = cliMissingError("glab")
		}
		out[id] = f
	}
}

// addTeaForges queries `tea logins list -o json` and adds each login to
// out, classifying a codeberg.org login as Codeberg and the rest as
// Gitea. A missing tea binary with a surviving configuration yields a
// cli_missing row.
func addTeaForges(ctx context.Context, out map[string]*ConfiguredForge) {
	logins, err := teaLogins(ctx)
	if err != nil {
		if errors.Is(err, cliexec.ErrNotInstalled) {
			addCLIMissingRow(KindGitea, out)
		} else {
			slog.Warn("forges: tea discovery failed", "error", err)
		}
		return
	}
	for _, login := range logins {
		host := login.host()
		if host == "" {
			host = login.Name
		}
		if host == "" {
			continue
		}
		kind := KindGitea
		if host == KindCodeberg.DefaultHost() {
			kind = KindCodeberg
		}
		id := MakeID(kind, host)
		out[id] = &ConfiguredForge{
			ID:        id,
			Kind:      kind,
			Host:      host,
			Username:  login.User,
			Connected: true,
		}
	}
}

// addCLIMissingRow adds the "configured but the CLI binary is missing"
// warning row for a kind — only when a configuration actually exists on
// disk (stat-only probe; no config parsing), so a never-connected forge
// stays absent from the list.
func addCLIMissingRow(kind Kind, out map[string]*ConfiguredForge) {
	if !cliConfigExists(kind) {
		return
	}
	id := string(kind) + ":cli-missing"
	out[id] = &ConfiguredForge{
		ID:         id,
		Kind:       kind,
		CLIMissing: true,
		LastError:  cliMissingError(kind.CLI()),
	}
}

// cliMissingError is the user-facing explanation on a cli_missing row.
func cliMissingError(cli string) string {
	return cli + " CLI is not installed — reinstall it in Settings → Tools to restore this connection"
}

// mergeForges swaps in the freshly-read forge set, preserving Email +
// LastProbed across the refresh. Those fields are populated only by Probe
// (from Whoami), not the CLI config files, so a raw replacement would lose
// them every 30s on the cache TTL.
func (m *Manager) mergeForges(out map[string]*ConfiguredForge) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, f := range out {
		if prev, ok := m.forges[id]; ok {
			if prev.Email != "" && f.Email == "" {
				f.Email = prev.Email
			}
			if prev.LastProbed > f.LastProbed {
				f.LastProbed = prev.LastProbed
			}
		}
	}
	m.forges = out
	m.cacheAt = time.Now()
}

// Invalidate clears the cache so the next List/Get reloads.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	m.cacheAt = time.Time{}
	m.mu.Unlock()
}

// Probe runs a Whoami against the forge to verify auth still works
// and updates the Connected/LastProbed/LastError fields.
func (m *Manager) Probe(ctx context.Context, id string) error {
	m.mu.RLock()
	f, ok := m.forges[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("forges: unknown id %q", id)
	}
	provider, err := New(f.Kind, f.Host)
	if err != nil {
		return err
	}
	v, err, _ := m.probeSF.Do(id, func() (any, error) {
		return provider.Whoami(ctx)
	})
	now := time.Now().UnixMilli()
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok = m.forges[id]
	if !ok {
		return nil
	}
	if err != nil {
		f.Connected = false
		f.LastError = err.Error()
		f.LastProbed = now
		return err
	}
	user, _ := v.(*User)
	f.Connected = true
	f.LastError = ""
	f.LastProbed = now
	if user.Login != "" {
		f.Username = user.Login
	}
	if user.Email != "" {
		f.Email = user.Email
	}
	return nil
}

// Provider returns a ForgeOps for the given configured forge ID.
// Returns an error if the ID is unknown.
func (m *Manager) Provider(id string) (ForgeOps, error) {
	f := m.Get(id)
	if f == nil {
		return nil, fmt.Errorf("forges: unknown id %q", id)
	}
	return New(f.Kind, f.Host)
}
