package mcp

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// ACP export + secret masking helpers. Kept in a leaf file so store.go
// stays focused on persistence and life-cycle.

// copyServer returns a deep copy of s. When maskSecrets is true, every
// env/header value is replaced by SecretMask (safe to send to the
// browser). When false, values are preserved (for kiro-cli / pre-warm).
func copyServer(s *Server, maskSecrets bool) *Server {
	if s == nil {
		return nil
	}
	c := *s
	c.Args = append([]string(nil), s.Args...)
	c.DisabledTools = append([]string(nil), s.DisabledTools...)
	c.AutoApprove = append([]string(nil), s.AutoApprove...)
	if maskSecrets {
		if c.OAuthClientSecret != "" {
			c.OAuthClientSecret = SecretMask
		}
		c.Env = make([]KeyPair, len(s.Env))
		for i, kv := range s.Env {
			c.Env[i] = KeyPair{Name: kv.Name, Value: SecretMask}
		}
		c.Headers = make([]KeyPair, len(s.Headers))
		for i, kv := range s.Headers {
			c.Headers[i] = KeyPair{Name: kv.Name, Value: SecretMask}
		}
	} else {
		c.Env = copyPairs(s.Env)
		c.Headers = copyPairs(s.Headers)
	}
	return &c
}

// maskedCopy returns a deep copy of s with every env/header value
// replaced by SecretMask. Safe to send to the browser.
func maskedCopy(s *Server) *Server { return copyServer(s, true) }

// rawCopy returns a deep copy with secrets intact. Used to pass values
// to kiro-cli and to the npx pre-warm scheduler.
func rawCopy(s *Server) *Server { return copyServer(s, false) }

func copyPairs(in []KeyPair) []KeyPair {
	if in == nil {
		return nil
	}
	out := make([]KeyPair, len(in))
	copy(out, in)
	return out
}

// preserveNilSlice keeps the existing value when the update omitted the
// field entirely (nil patch) and otherwise takes the patch (including an
// explicit empty slice = clear). Used for auto_approve, which has no
// dedicated edit UI: a modal edit omits it and must not drop a value set
// via the raw-JSON panel, while a raw edit can still set or clear it.
func preserveNilSlice(patch, existing []string) []string {
	if patch == nil {
		return append([]string(nil), existing...)
	}
	return append([]string(nil), patch...)
}

// mergeSecret preserves the stored secret when the client re-submits the
// SecretMask sentinel; otherwise the patch value wins. Scalar counterpart
// to mergeSecrets (which operates on KeyPair slices).
func mergeSecret(patch, existing string) string {
	if patch == SecretMask {
		return existing
	}
	return patch
}

// mergeSecrets returns a new slice that mirrors `patch` in order and
// key-set, but substitutes the previously-stored value wherever the
// client sent SecretMask. Preserves the user's intended ordering while
// keeping secrets round-trip safe.
func mergeSecrets(patch, existing []KeyPair) []KeyPair {
	out := make([]KeyPair, len(patch))
	index := make(map[string]string, len(existing))
	for _, kv := range existing {
		index[kv.Name] = kv.Value
	}
	for i, kv := range patch {
		out[i] = kv
		if kv.Value == SecretMask {
			if prev, ok := index[kv.Name]; ok {
				out[i].Value = prev
			} else {
				out[i].Value = ""
			}
		}
	}
	return out
}

// acpServer is the on-wire shape passed to kiro-cli's session/new and
// session/load mcpServers parameter. Schema comes from the Agent Client
// Protocol spec (McpServerStdio / McpServerHttp / McpServerSse). We
// discriminate by "type" and emit only the fields relevant to each.
type acpServer map[string]any

// buildACP constructs the on-wire acpServer map for a server using this
// transport. Returns nil for unknown transports (unreachable if Transport
// is validated at the parse boundary via ParseTransport).
func (t Transport) buildACP(s *Server) acpServer {
	switch t {
	case TransportStdio:
		return stdioBuilder(s)
	case TransportHTTP, TransportSSE:
		// remoteBuilder emits "type": string(s.Transport), so an SSE
		// server produces {type:"sse", ...} and an HTTP server
		// {type:"http", ...} from the same code path (identical fields
		// per the ACP McpServerHttp / McpServerSse schemas).
		return remoteBuilder(s)
	default:
		return nil
	}
}

func stdioBuilder(s *Server) acpServer {
	entry := acpServer{
		"type":          string(TransportStdio),
		api.JSONKeyName: s.Name,
		"command":       s.Command,
		"args":          argsArray(s.Args),
		"env":           pairsArray(s.Env),
	}
	if len(s.DisabledTools) > 0 {
		entry["disabledTools"] = s.DisabledTools
	}
	if len(s.AutoApprove) > 0 {
		entry["autoApprove"] = s.AutoApprove
	}
	return entry
}

func remoteBuilder(s *Server) acpServer {
	entry := acpServer{
		"type":          string(s.Transport),
		api.JSONKeyName: s.Name,
		"url":           s.URL,
		"headers":       pairsArray(s.Headers),
	}
	if len(s.DisabledTools) > 0 {
		entry["disabledTools"] = s.DisabledTools
	}
	if len(s.AutoApprove) > 0 {
		entry["autoApprove"] = s.AutoApprove
	}
	// kiro-cli 2.3+ accepts a pre-registered OAuth client ID via
	// `oauth.clientId`; 2.12+ adds `oauth.clientSecret` for confidential
	// clients that authenticate at the token endpoint. Required for HTTP
	// MCP servers that don't support Dynamic Client Registration (Slack,
	// GitHub, Figma). Empty clientId falls back to DCR.
	if s.OAuthClientID != "" || s.OAuthClientSecret != "" {
		oauth := make(map[string]any, 2)
		if s.OAuthClientID != "" {
			oauth["clientId"] = s.OAuthClientID
		}
		if s.OAuthClientSecret != "" {
			oauth["clientSecret"] = s.OAuthClientSecret
		}
		entry["oauth"] = oauth
	}
	return entry
}

// toACP converts enabled servers to the ACP mcpServers array. Skips any
// server with an unknown transport so a bad record can't crash the
// bridge start. Secrets are passed through as-is; callers must hold
// raw copies, not masked.
//
// Wire shape per the ACP spec (agentclientprotocol.com/protocol/schema):
//
//	McpServerStdio = { type:"stdio", name, command, args: string[], env: {name,value}[] }
//	McpServerHttp  = { type:"http",  name, url, headers: {name,value}[] }
//	McpServerSse   = { type:"sse",   name, url, headers: {name,value}[] }
//
// args, env, and headers are REQUIRED arrays (possibly empty). We emit
// [] for the empty case, never null — kiro-cli would reject a schema
// violation at session/new / session/load time.
func toACP(servers []*Server) []acpServer {
	out := make([]acpServer, 0, len(servers))
	for _, s := range servers {
		entry := s.Transport.buildACP(s)
		if entry == nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// argsArray normalises a potentially-nil slice into a non-nil empty
// slice so the JSON encoding is [] instead of null.
func argsArray(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// pairsArray returns the ACP-spec array-of-objects shape for env vars
// and HTTP headers: [{api.JSONKeyName:..., "value":...}, ...]. Always emits at
// least [] for the empty case so required-but-empty JSON is valid.
func pairsArray(in []KeyPair) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, kv := range in {
		out = append(out, map[string]string{api.JSONKeyName: kv.Name, "value": kv.Value})
	}
	return out
}

// ACPServers satisfies api.MCPConfig by returning every enabled server
// shaped for kiro-cli's session/new / session/load mcpServers parameter.
// Secrets are included; the slice must not be logged.
func (s *Store) ACPServers(ctx context.Context) []map[string]any {
	enabled := s.EnabledRaw(ctx)
	acp := toACP(enabled)
	out := make([]map[string]any, len(acp))
	for i, entry := range acp {
		out[i] = entry
	}
	return out
}

// EnabledNames satisfies api.MCPConfig, returning the set of enabled
// server names for the hub's defensive filtering of init notifications.
func (s *Store) EnabledNames(_ context.Context) map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]struct{}, len(s.servers))
	for _, sv := range s.servers {
		if sv.Enabled {
			out[sv.Name] = struct{}{}
		}
	}
	return out
}
