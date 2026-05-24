// Manager — discovers and lists configured forge providers from
// the CLI tool config files. The CLIs are the source of truth for
// auth state; this manager is a thin read-through with caching.

package forges

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ConfiguredForge is one connected forge backend. Constructed from
// the CLI config files.
type ConfiguredForge struct {
	ID         string `json:"id"`
	Kind       Kind   `json:"kind"`
	Host       string `json:"host"`
	Username   string `json:"username,omitempty"`
	Email      string `json:"email,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	LastProbed int64  `json:"last_probed,omitempty"`
	Connected  bool   `json:"connected"`
}

// Manager owns the list of configured forges.
type Manager struct {
	cacheAt time.Time
	forges  map[string]*ConfiguredForge
	ttl     time.Duration
	mu      sync.RWMutex
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
		_ = m.Refresh(ctx)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ConfiguredForge, 0, len(m.forges))
	for _, f := range m.forges {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Host < out[j].Host
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

// Refresh re-reads all CLI config files and rebuilds the forge list.
func (m *Manager) Refresh(_ context.Context) error {
	root, err := configHome()
	if err != nil {
		return err
	}
	out := make(map[string]*ConfiguredForge)

	// gh: ~/.config/gh/hosts.yml
	if hosts, err := loadGHHosts(filepath.Join(root, "gh", "hosts.yml")); err == nil {
		for host, entry := range hosts {
			if entry.OAuthToken == "" {
				continue
			}
			id := MakeID(KindGitHub, host)
			out[id] = &ConfiguredForge{
				ID:        id,
				Kind:      KindGitHub,
				Host:      host,
				Username:  entry.User,
				Connected: true,
			}
		}
	}

	// glab: ~/.config/glab-cli/config.yml
	if cfg, err := loadGLabConfig(filepath.Join(root, "glab-cli", "config.yml")); err == nil {
		for host, entry := range cfg.Hosts {
			if entry.Token == "" {
				continue
			}
			id := MakeID(KindGitLab, host)
			out[id] = &ConfiguredForge{
				ID:        id,
				Kind:      KindGitLab,
				Host:      host,
				Username:  entry.User,
				Connected: true,
			}
		}
	}

	// tea: ~/.config/tea/config.yml — both Gitea and Codeberg.
	if cfg, err := loadTeaConfig(filepath.Join(root, "tea", "config.yml")); err == nil {
		for _, login := range cfg.Logins {
			if login.Token == "" {
				continue
			}
			kind := KindGitea
			if login.Name == KindCodeberg.DefaultHost() ||
				login.URL == "https://"+KindCodeberg.DefaultHost() {
				kind = KindCodeberg
			}
			id := MakeID(kind, login.Name)
			out[id] = &ConfiguredForge{
				ID:        id,
				Kind:      kind,
				Host:      login.Name,
				Username:  login.User,
				Connected: true,
			}
		}
	}

	m.mu.Lock()
	// Preserve Email + LastProbed across Refresh — they're populated
	// only by Probe (from Whoami), not from the CLI config files. A
	// raw replacement would lose them every 30s on the cache TTL.
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
	m.mu.Unlock()
	return nil
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
	user, err := provider.Whoami(ctx)
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
