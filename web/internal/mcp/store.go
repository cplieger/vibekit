// Package mcp persists and serves the user's configured MCP (Model
// Context Protocol) servers.
//
// # Storage model
//
// A single file at <configDir>/mcp.json, mode 0600, written atomically
// (temp + rename) via api.SaveBytes. The file holds an ordered array of
// Server records. Order is the display order; no separate index.
//
// # Scope
//
// One scope only: user-global. Vibekit runs one container per user; per-
// chat or per-workspace MCP sets would add schema churn with no clear
// benefit. This matches how kiro-cli's mcpServers parameter is scoped
// to a session (not a chat), and we intentionally use the same set for
// every bridge spawned within the same container.
//
// # Secrets
//
// Env values and header values often contain API keys. Store returns
// them masked ("***") from public reads; Update preserves the
// stored value when the client sends "***" so the UI can round-trip
// without re-submitting secrets. On disk the file is plaintext with
// 0600 perms — the threat model is the same as the chat files that
// already live in the same directory (user's own container).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vibekit/internal/api"
)

// Compile-time interface assertions.
var (
	_ api.MCPConfig    = (*Store)(nil)
	_ api.RouteHandler = (*Store)(nil)
)

// Transport names the MCP transports vibekit accepts in mcp.json.
// "stdio" is universal; "http" is the Streamable HTTP transport
// (2025-03-26 MCP spec). The legacy "sse" value is accepted on parse
// and silently normalized to "http" — kiro-cli's client-side fallback
// logic handles servers that only speak the old HTTP+SSE protocol.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// ParseTransport validates a raw string as a known transport. The
// legacy "sse" value is accepted and normalized to TransportHTTP per
// the 2025-03-26 MCP spec (Streamable HTTP replaces HTTP+SSE; kiro-cli
// implements the backwards-compat fallback at connection time).
func ParseTransport(s string) (Transport, error) {
	switch Transport(s) {
	case TransportStdio, TransportHTTP:
		return Transport(s), nil
	case "sse":
		return TransportHTTP, nil
	default:
		return "", fmt.Errorf("unknown transport: %q", s)
	}
}

// Valid reports whether t is one of the known transport values.
func (t Transport) Valid() bool {
	switch t {
	case TransportStdio, TransportHTTP:
		return true
	default:
		return false
	}
}

// SecretMask references the shared api.SecretMask constant.
const SecretMask = api.SecretMask

// Server is one user-configured MCP server. ID is a short stable
// identifier used in URLs and events (generated at create time);
// Name is the user-visible label that also becomes the kiro-cli
// mcpServer name (must be unique across the configured set).
type Server struct {
	URL           string    `json:"url,omitempty"`
	ID            ServerID  `json:"id"`
	Name          string    `json:"name"`
	Command       string    `json:"command,omitempty"`
	Transport     Transport `json:"transport"`
	Args          []string  `json:"args,omitempty"`
	Env           []KeyPair `json:"env,omitempty"`
	Headers       []KeyPair `json:"headers,omitempty"`
	DisabledTools []string  `json:"disabled_tools,omitempty"`
	KnownTools    []string  `json:"known_tools,omitempty"`
	CreatedAt     int64     `json:"created_at"`
	UpdatedAt     int64     `json:"updated_at"`
	Prewarm       bool      `json:"prewarm,omitempty"`
	Enabled       bool      `json:"enabled"`
}

// KeyPair is an ordered env-var or header entry. Ordered (vs map) so
// the UI can edit entries without dropping duplicates; the on-wire ACP
// format is a JSON object so we flatten on export.
type KeyPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Store holds the persisted list in memory plus the coordination
// needed to serialise writes and notify watchers on changes.
type Store struct {
	path     string
	onChange func(context.Context)
	servers  []*Server
	mu       sync.RWMutex
}

// New loads the file (or initialises empty) and returns a ready store.
// onChange is invoked on a fresh goroutine (without the store mutex
// held) whenever the persisted set is mutated; nil is valid if no one
// cares. The callback is free to call back into the store — it won't
// deadlock on the caller's write lock.
func New(configDir string, onChange func(context.Context)) (*Store, error) {
	s := &Store{
		path:     filepath.Join(configDir, "mcp.json"),
		onChange: onChange,
		servers:  []*Server{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetOnChange replaces the change callback.
func (s *Store) SetOnChange(fn func(context.Context)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read mcp.json: %w", err)
	}
	if info, statErr := os.Stat(s.path); statErr == nil && info.Mode().Perm() != 0o600 {
		if chErr := os.Chmod(s.path, 0o600); chErr != nil {
			slog.Warn("mcp: tighten mcp.json perms failed", "path", s.path, "error", chErr)
		}
	}
	var f file
	if uErr := json.Unmarshal(data, &f); uErr != nil {
		corruptPath := fmt.Sprintf("%s.corrupt.%s.%d",
			s.path,
			time.Now().UTC().Format("20060102-150405"),
			os.Getpid())
		if rErr := os.Rename(s.path, corruptPath); rErr != nil {
			slog.Error("mcp: preserve corrupt mcp.json failed",
				"path", s.path, "error", rErr, "parse_error", uErr)
		} else {
			slog.Warn("mcp: mcp.json unparseable, moved aside",
				"from", s.path, "to", corruptPath, "parse_error", uErr)
		}
		return nil
	}
	if f.Servers == nil {
		return nil
	}
	s.servers = f.Servers
	return nil
}

func (s *Store) notifyChange(ctx context.Context) {
	s.mu.RLock()
	cb := s.onChange
	s.mu.RUnlock()
	if cb == nil {
		return
	}
	go cb(ctx)
}

func (s *Store) indexLocked(id ServerID) int {
	for i, sv := range s.servers {
		if sv.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) hasNameLocked(name string, ignoreID ServerID) bool {
	for _, sv := range s.servers {
		if sv.ID == ignoreID {
			continue
		}
		if strings.EqualFold(sv.Name, name) {
			return true
		}
	}
	return false
}
