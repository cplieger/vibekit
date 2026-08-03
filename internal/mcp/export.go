package mcp

import (
	"context"
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

// There is no ACP wire builder here any more, and no ACPServers.
//
// vibekit sent its server set INLINE on session/new and session/load, as the
// `mcpServers` parameter. It now renders KAS's own config file instead
// (kasfile.go), and sends nothing — KAS merges `client > file-based`, so an
// inline entry would win over the file and make every file edit look like a
// no-op. The two had to swap in one change.
//
// What went with it: toACP, the per-transport stdio/remote builders,
// Transport.buildACP, argsArray and pairsArray. The ACP McpServerStdio /
// McpServerHttp / McpServerSse shapes they encoded are no longer vibekit's
// concern — KAS infers the transport from the fields present and negotiates
// HTTP vs SSE itself.
//
// EnabledNames stays: the hub still filters status notifications against the
// set of servers the user has enabled.

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
