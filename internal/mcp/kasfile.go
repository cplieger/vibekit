package mcp

// KAS's own MCP config file is the source of truth for what the agent connects
// to. vibekit RENDERS it from its store and sends nothing inline.
//
// Why the file and not the `mcpServers` session parameter:
//
//   - It HOT-RELOADS. KAS watches `~/.kiro/settings/mcp.json` (an
//     `MCPConfigManager` with a `ConfigFileWatcher`) and re-merges on change, so
//     adding a server connects it mid-session and disabling one drops it in
//     place. The inline list is read once at session/new, which is where the
//     "configuration changes apply on the next new chat" wart came from.
//   - It carries MORE. The inline path funnels through KAS's
//     `acpServerToWire`, which reads only `command`/`args`/`env` or
//     `url`/`headers` plus `_meta.kiro.{disabledTools,waitForReady}` — it DROPS
//     `oauth`, `oauthScopes`, `autoApprove`, `cwd` and `timeout`. So vibekit's
//     pre-registered OAuth client id and secret, the fields that exist because
//     Slack, GitHub and Figma do not support dynamic client registration, never
//     reached the agent on v3 at all. The file delivers them.
//
// PRECEDENCE IS WHY THIS IS ATOMIC. KAS merges `client > file-based`, so as long
// as vibekit still sends an inline entry for a server, the inline copy wins and
// edits to the file appear to do nothing. The inline path had to go in the same
// change, not after it.
//
// # The file is shared, so unknown keys are preserved
//
// KAS reads two blocks out of this one file: `mcpServers` (the user level) and
// `powers.mcpServers` (installed Powers). vibekit owns the first and must not
// clobber the second, so a write re-reads the file and replaces ONLY the
// `mcpServers` key. Anything else — `powers`, a comment-bearing key a future KAS
// adds, a hand-edit — survives.
//
// # Two losses, both real
//
//   - `env` and `headers` are RECORDS here, where vibekit stores ordered
//     `KeyPair` slices. So duplicate names collapse (last wins) and ordering is
//     not preserved on the wire. The store keeps the ordered form, which is what
//     the editor round-trips; only the rendered file is flattened. The inline
//     path had exactly the same collapse one layer later (`acpServerToWire`
//     builds a record too), so nothing regressed — it is just visible now.
//   - `transport` stops being a wire distinction. KAS infers the transport from
//     which fields are present and auto-negotiates HTTP vs SSE at connect time;
//     `type` is accepted and then ignored ("a server declared `type: http`
//     connects exactly as one with no `type` at all"). So this writer emits no
//     `type`, and an `sse` server and an `http` server render identically. The
//     enum stays in the store because it still drives validation and the UI.

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
// McpServerWireSchema. Only the fields vibekit has a value for are emitted:
// every one is `omitempty`, because an explicit null or zero is a different
// declaration than an absent field (`timeout: 0` fails its `min(1)`).
//
// Deliberately absent: `type` (an ignored hint — see the package comment),
// `cwd` and `timeout` and `waitForReady` (vibekit's store has no field for them;
// they are the schema's superset, reachable by hand-editing the file, and adding
// UI for them is not this change).
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

// writeKASConfig renders the server set into KAS's config file, preserving every
// top-level key it does not own.
//
// Best-effort on the READ: an unparseable or unreadable existing file is
// replaced rather than treated as fatal. That direction is deliberate — the
// alternative is refusing to write, which leaves the agent connected to a stale
// server set with no way for the user to fix it from the UI. A parse failure is
// logged by the caller.
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
// declares. Empty when the file is absent, oversized, unparseable, or carries no
// powers block — every one of which means "vibekit cannot attribute this name",
// which is OriginUnknown rather than an error.
//
// This reads the file rather than caching it, and the call site is why that is
// affordable: AllNames is consulted only for a name vibekit's own config does
// NOT hold, so a status frame for a configured server (the overwhelming
// majority) never reaches the disk. Caching instead would trade a rare
// few-kilobyte read for a staleness window on exactly the event this exists to
// observe — a Power installed mid-session.
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
// HOME, not the workspace. KAS reads BOTH `~/.kiro/settings/mcp.json` and
// `<workspace>/.kiro/settings/mcp.json`, and the workspace one sits inside the
// user's repo where it is a plausible accidental commit of a file holding OAuth
// client secrets. The home path is also the one that does not depend on which
// workspace folders a session declares.
func kasConfigPath() string {
	return workspace.KiroSettingsPath("mcp.json")
}

// slogWarnKAS logs a KAS-config read problem. The path is safe to log; the
// file's CONTENT is not, so no branch here touches it.
func slogWarnKAS(msg, path string, err error) {
	slog.Warn("mcp: kas config "+msg, "path", path, "error", err)
}
