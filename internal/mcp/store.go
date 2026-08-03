// Package mcp persists and serves the user's configured MCP (Model
// Context Protocol) servers.
//
// # Storage model
//
// A single file at <configDir>/mcp.json, mode 0600, written atomically
// (temp + rename) via fileutil.SaveBytes. The file holds an ordered array of
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

	"github.com/cplieger/vibekit/internal/api"
)

// Compile-time interface assertions.
var (
	_ api.MCPConfig    = (*Store)(nil)
	_ api.RouteHandler = (*Store)(nil)
)

// Transport names the MCP transports vibekit accepts in mcp.json.
// "stdio" is universal; "http" is the Streamable HTTP transport
// (2025-03-26 MCP spec); "sse" is the legacy HTTP+SSE remote transport.
//
// SSE is a first-class, stored transport (not normalized to "http"):
// kiro-cli v3 (KAS) re-advertises mcpCapabilities.sse:true and accepts
// a distinct {type:"sse", url, headers} entry on session/new
// (verified against the KAS 2.12 acp-server bundle + a live session/new
// probe; a bogus transport is rejected, so "sse" is genuinely accepted,
// not merely tolerated). "sse" and "http" share the same remote wire
// shape (url + headers) and differ only in the ACP `type` discriminator,
// so both are validated as remote transports and both round-trip through
// the store verbatim.
type Transport string

// TransportStdio, TransportHTTP, and TransportSSE define the valid
// Transport values for MCP server connections.
const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
	TransportSSE   Transport = "sse"
)

// ParseTransport validates a raw string as a known transport. All three
// transports (stdio, http, sse) are first-class: "sse" is preserved as
// TransportSSE, not folded into "http" — KAS accepts a distinct SSE
// mcpServers entry over the v3 wire (see the Transport doc).
func ParseTransport(s string) (Transport, error) {
	switch Transport(s) {
	case TransportStdio, TransportHTTP, TransportSSE:
		return Transport(s), nil
	default:
		return "", fmt.Errorf("unknown transport: %q", s)
	}
}

// Valid reports whether t is one of the known transport values.
func (t Transport) Valid() bool {
	switch t {
	case TransportStdio, TransportHTTP, TransportSSE:
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
	URL               string    `json:"url,omitempty"`
	Name              string    `json:"name"`
	Command           string    `json:"command,omitempty"`
	OAuthClientID     string    `json:"oauth_client_id,omitempty"`
	OAuthClientSecret string    `json:"oauth_client_secret,omitempty"`
	ID                ServerID  `json:"id"`
	Transport         Transport `json:"transport"`
	Args              []string  `json:"args,omitempty"`
	Env               []KeyPair `json:"env,omitempty"`
	Headers           []KeyPair `json:"headers,omitempty"`
	DisabledTools     []string  `json:"disabled_tools,omitempty"`
	AutoApprove       []string  `json:"auto_approve,omitempty"`
	CreatedAt         int64     `json:"created_at"`
	UpdatedAt         int64     `json:"updated_at"`
	Prewarm           bool      `json:"prewarm,omitempty"`
	Enabled           bool      `json:"enabled"`
}

// NewServer constructs a Server with validated transport-specific fields.
// Fields inappropriate for the given transport are rejected at creation
// time rather than deferred to Validate(). Returns an error if the
// transport is unknown or if transport-incompatible fields are populated.
func NewServer(transport Transport, name string, opts ...ServerOption) (*Server, error) {
	if !transport.Valid() {
		return nil, fmt.Errorf("unknown transport: %q", transport)
	}
	s := &Server{
		Transport: transport,
		Name:      name,
		Enabled:   true,
	}
	for _, opt := range opts {
		opt(s)
	}
	if err := Validate(s); err != nil {
		return nil, err
	}
	return s, nil
}

// ServerOption configures a Server during construction via NewServer.
type ServerOption func(*Server)

// WithCommand sets the command for stdio transport servers.
func WithCommand(cmd string, args ...string) ServerOption {
	return func(s *Server) {
		s.Command = cmd
		s.Args = args
	}
}

// WithURL sets the URL for HTTP transport servers.
func WithURL(url string) ServerOption {
	return func(s *Server) { s.URL = url }
}

// WithOAuthClientID sets the OAuth client ID for HTTP transport servers.
func WithOAuthClientID(id string) ServerOption {
	return func(s *Server) { s.OAuthClientID = id }
}

// WithEnv sets environment variables for stdio transport servers.
func WithEnv(env []KeyPair) ServerOption {
	return func(s *Server) { s.Env = env }
}

// WithHeaders sets HTTP headers for HTTP transport servers.
func WithHeaders(headers []KeyPair) ServerOption {
	return func(s *Server) { s.Headers = headers }
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
	ctx      context.Context
	path     string
	kasPath  string
	onChange func(context.Context)
	servers  []*Server
	mu       sync.RWMutex
}

// New loads the file (or initialises empty) and returns a ready store.
// onChange is invoked on a fresh goroutine (without the store mutex
// held) whenever the persisted set is mutated; nil is valid if no one
// cares. The callback is free to call back into the store — it won't
// deadlock on the caller's write lock. The ctx is stored for use in
// fire-and-forget persist paths so writes are cancellable on shutdown.
//
// Two files, one source of truth. `<configDir>/mcp.json` is vibekit's own record
// — ordered KeyPairs, the transport enum, ids and timestamps, everything the
// editor round-trips. KAS's `~/.kiro/settings/mcp.json` is RENDERED from it and
// is what the agent actually reads (see kasfile.go). The render runs on every
// persist and once at construction, so a config that predates this code, or a
// KAS file deleted out from under us, is reconciled at boot rather than on the
// next edit.
func New(ctx context.Context, configDir string, onChange func(context.Context), opts ...Option) (*Store, error) {
	s := &Store{
		ctx:      ctx,
		path:     filepath.Join(configDir, "mcp.json"),
		kasPath:  kasConfigPath(),
		onChange: onChange,
		servers:  []*Server{},
	}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	// Boot reconcile, before anything can race on the store. A failure here must
	// not stop the server from starting: the user needs the UI to fix whatever is
	// wrong with the path or the disk, and a stale KAS file degrades to the
	// previous server set rather than to nothing.
	if err := s.writeKASConfig(ctx, s.servers); err != nil {
		slog.Error("mcp: initial kas config write failed; the agent may use a stale server set",
			"path", s.kasPath, "error", err)
	}
	return s, nil
}

// Option configures a Store at construction.
type Option func(*Store)

// WithKASConfigPath overrides where KAS's config file is rendered.
//
// This exists for TESTS, and it is a functional option rather than a package
// var for a specific reason: the default resolves under $HOME, so a test that
// constructs a store without isolating it writes the developer's own
// ~/.kiro/settings/mcp.json — which is exactly what happened once. A global
// override has to be remembered; a required-by-convention option is visible at
// every call site, and `TestNew_DefaultKASPathIsUnderKiroHome` pins that
// production still gets the real path.
func WithKASConfigPath(path string) Option {
	return func(s *Store) { s.kasPath = path }
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
