package mcp

// KAS's own MCP config file is the source of truth for what the agent connects
// to. vibekit RENDERS it from its store and sends nothing inline.
//
// Why the file and not the `mcpServers` session parameter:
//
//   - It HOT-RELOADS. KAS watches `~/.kiro/settings/mcp.json` and re-merges
//     on change, so adding a server connects it mid-session. The inline
//     list was read once at session/new.
//   - It carries MORE. The inline path funnelled through KAS's
//     `acpServerToWire`, which drops `oauth`, `oauthScopes`, `autoApprove`,
//     `cwd` and `timeout`. The file delivers them.
//
// PRECEDENCE IS WHY THIS IS ATOMIC. KAS merges `client > file-based`, so as
// long as vibekit still sends an inline entry, the inline copy wins and
// edits to the file appear to do nothing.
//
// The file is shared: KAS also reads `powers.mcpServers` out of it, so a
// write re-reads the file and replaces ONLY the `mcpServers` key.
//
// Two losses, both real: `env`/`headers` are RECORDS on the wire (order not
// preserved, dup names collapse — the store keeps the ordered form for the
// editor), and `transport` stops being a wire distinction (KAS infers it
// from which fields are present, so this writer emits no `type`).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/workspace"
)

// kasServerKey is the top-level key vibekit owns in KAS's config file.
const kasServerKey = "mcpServers"

// kasPowersKey is the top-level key KAS fills from installed Powers. vibekit
// never writes it (readKASConfig preserves it verbatim) and reads it for one
// reason: to tell a Power's server apart from a server vibekit cannot see at
// all, so a runtime status row can say which.
const kasPowersKey = "powers"

// kasFileMaxBytes bounds the re-read of the existing file. The file is a handful
// of server declarations; a larger one is not something to merge into.
const kasFileMaxBytes = 4 << 20

// kasServer is one entry of KAS's `mcpServers` map, matching its
// McpServerWireSchema. Only the fields vibekit has a value for are
// emitted: every one is `omitempty`, because an explicit null or zero is
// a different declaration than an absent field.
//
// Deliberately absent: `type` (an ignored hint), `cwd`, `timeout` and
// `waitForReady` (no field for them; reachable by hand-editing the file).
type kasServer struct {
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	URL           string            `json:"url,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	OAuth         *kasOAuth         `json:"oauth,omitempty"`
	DisabledTools []string          `json:"disabledTools,omitempty"`
	AutoApprove   []string          `json:"autoApprove,omitempty"`
	// Disabled keeps a switched-off server IN the file rather than omitting it.
	// KAS then reports it with status "disabled" instead of not knowing about it,
	// which is the difference between "off" and "gone" in the UI.
	Disabled bool `json:"disabled,omitempty"`
}

// kasOAuth carries a pre-registered OAuth client for a server that cannot do
// dynamic client registration.
type kasOAuth struct {
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// renderKASServers maps the store's servers onto KAS's map shape. Secrets are
// included, so the result must not be logged.
//
// EVERY server is rendered, enabled or not — a disabled one carries
// `disabled: true`. Filtering them out would make a disabled server
// indistinguishable from a deleted one to KAS, and its status would go missing
// rather than reading "disabled".
func renderKASServers(servers []*Server) map[string]kasServer {
	out := make(map[string]kasServer, len(servers))
	for _, s := range servers {
		if s == nil || s.Name == "" {
			continue
		}
		entry := kasServer{
			DisabledTools: s.DisabledTools,
			AutoApprove:   s.AutoApprove,
			Disabled:      !s.Enabled,
		}
		switch s.Transport {
		case TransportStdio:
			entry.Command = s.Command
			entry.Args = s.Args
			entry.Env = pairsRecord(s.Env)
		case TransportHTTP, TransportSSE:
			// One branch for both: KAS negotiates HTTP vs SSE itself.
			entry.URL = s.URL
			entry.Headers = pairsRecord(s.Headers)
			if s.OAuthClientID != "" || s.OAuthClientSecret != "" {
				entry.OAuth = &kasOAuth{ClientID: s.OAuthClientID, ClientSecret: s.OAuthClientSecret}
			}
		default:
			// An unknown transport has neither a command nor a url, so KAS would
			// reject it as "must specify a command or url". Skip it rather than
			// write a declaration that can only produce a warning.
			continue
		}
		out[s.Name] = entry
	}
	return out
}

// pairsRecord flattens ordered KeyPairs into KAS's record shape. Later entries
// win on a duplicate name, which is the same resolution the inline path's own
// record build produced.
func pairsRecord(in []KeyPair) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for _, kv := range in {
		out[kv.Name] = kv.Value
	}
	return out
}

// writeKASConfig renders the server set into KAS's config file, preserving
// every top-level key it does not own.
//
// Best-effort on the READ: an unparseable or unreadable existing file is
// replaced rather than treated as fatal — the alternative leaves the
// agent connected to a stale server set with no way for the user to fix
// it from the UI.
func (s *Store) writeKASConfig(ctx context.Context, servers []*Server) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	doc := s.readKASConfig()
	rendered, err := json.Marshal(renderKASServers(servers))
	if err != nil {
		return fmt.Errorf("%w kas mcp.json: %w", ErrPersistMarshal, err)
	}
	doc[kasServerKey] = json.RawMessage(rendered)

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("%w kas mcp.json: %w", ErrPersistMarshal, err)
	}
	// 0600: the file holds header values and OAuth client secrets. KAS reads it
	// as the same user, so nothing needs broader access.
	if _, err := atomicfile.WriteFile(ctx, s.kasPath, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("%w: %w", ErrPersistWrite, err)
	}
	return nil
}

// readKASConfig returns the existing document's top-level keys minus the one
// vibekit owns, so a write can put ours back without touching `powers` or
// anything else. An absent, oversized or malformed file yields an empty
// document.
func (s *Store) readKASConfig() map[string]json.RawMessage {
	empty := map[string]json.RawMessage{}
	info, err := os.Stat(s.kasPath)
	if err != nil || info.Size() > kasFileMaxBytes {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			slogWarnKAS("stat failed", s.kasPath, err)
		}
		return empty
	}
	data, err := os.ReadFile(s.kasPath)
	if err != nil {
		slogWarnKAS("read failed", s.kasPath, err)
		return empty
	}
	var doc map[string]json.RawMessage
	// KAS parses this with JSONC, so a hand-written file may carry comments that
	// encoding/json rejects. Losing an unknown key is the cost of not vendoring a
	// JSONC parser to preserve keys vibekit does not write; it is logged.
	if err := json.Unmarshal(data, &doc); err != nil {
		slogWarnKAS("existing file unparseable, its non-mcpServers keys will be dropped", s.kasPath, err)
		return empty
	}
	delete(doc, kasServerKey)
	return doc
}

// powerNames returns the server names the file's `powers.mcpServers` block
// declares. Empty when the file is absent, oversized, unparseable, or
// carries no powers block — every one of which means "vibekit cannot
// attribute this name", which is OriginUnknown rather than an error.
//
// This reads the file rather than caching it: AllNames is consulted only
// for a name vibekit's own config does NOT hold, so a status frame for a
// configured server never reaches the disk.
func (s *Store) powerNames() map[string]struct{} {
	out := map[string]struct{}{}
	// readKASConfig deletes the key vibekit owns and keeps the rest, so the
	// powers block arrives here untouched.
	raw, ok := s.readKASConfig()[kasPowersKey]
	if !ok {
		return out
	}
	var block struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		slogWarnKAS("powers block unparseable; its servers will report an unknown origin", s.kasPath, err)
		return out
	}
	for name := range block.MCPServers {
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

// kasConfigPath is the file KAS reads for user-level MCP servers.
//
// HOME, not the workspace. KAS reads BOTH the home and workspace paths,
// and the workspace one sits inside the user's repo where it is a
// plausible accidental commit of a file holding OAuth client secrets.
func kasConfigPath() string {
	return workspace.KiroSettingsPath("mcp.json")
}

// slogWarnKAS logs a KAS-config read problem. The path is safe to log; the
// file's CONTENT is not, so no branch here touches it.
func slogWarnKAS(msg, path string, err error) {
	slog.Warn("mcp: kas config "+msg, "path", path, "error", err)
}
