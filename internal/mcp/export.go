package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
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

// sameSpec reports whether two records describe the same CONNECTION, which
// is what makes a re-paste of an already-configured server a no-op
// instead of a conflict. Deliberately excluded: ID/CreatedAt/UpdatedAt
// (store-owned), secret VALUES (a pasted README carries a placeholder),
// and Enabled/Prewarm/DisabledTools/AutoApprove (the user's own policy).
//
// Env and header NAMES are compared as sets, not sequences: they are
// records on KAS's wire, so a user who dragged two env rows around has
// not changed the connection. Args stay order-sensitive, since argv order
// is.
func sameSpec(a, b *Server) bool {
	if a.Transport != b.Transport ||
		strings.TrimSpace(a.Command) != strings.TrimSpace(b.Command) ||
		strings.TrimSpace(a.URL) != strings.TrimSpace(b.URL) ||
		a.OAuthClientID != b.OAuthClientID {
		return false
	}
	if !slices.Equal(a.Args, b.Args) {
		return false
	}
	if !slices.Equal(sortedPairNames(a.Env, false), sortedPairNames(b.Env, false)) {
		return false
	}
	return slices.Equal(sortedPairNames(a.Headers, true), sortedPairNames(b.Headers, true))
}

// sortedPairNames returns the pair names in sorted order, lowercased when the
// field dedupes case-insensitively (headers do, env does not — the same split
// validateKeyPairs makes).
func sortedPairNames(pairs []KeyPair, fold bool) []string {
	out := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		if fold {
			out = append(out, strings.ToLower(kv.Name))
			continue
		}
		out = append(out, kv.Name)
	}
	slices.Sort(out)
	return out
}

// guardOriginChange refuses to re-attach a preserved secret to a new origin.
//
// A PUT may change `url` while masked header rows survive, and mergeSecrets
// keys its index on the header NAME alone. So a bearer issued for the old
// origin was silently re-attached, persisted, and rendered into KAS's
// config file, whose watcher hands it to the new origin.
//
// It refuses rather than silently dropping the value to "": a silent drop
// is indistinguishable from a successful save.
//
// The comparison is scheme+host, not the whole string: a path edit on the
// same origin is not a new party.
func guardOriginChange(in, existing *Server) error {
	if !changesOrigin(existing.URL, in.URL) {
		return nil
	}
	for _, kv := range in.Headers {
		if kv.Value != SecretMask {
			continue
		}
		// Exact-name lookup mirrors mergeSecrets: a name it would not match
		// preserves nothing, so there is nothing to refuse.
		if idx := slices.IndexFunc(existing.Headers, func(p KeyPair) bool {
			return p.Name == kv.Name && p.Value != ""
		}); idx >= 0 {
			return fmt.Errorf(
				"url points at a new origin, so the stored %q header was not carried over: re-enter its value for %s",
				kv.Name, originLabel(in.URL),
			)
		}
	}
	if in.OAuthClientSecret == SecretMask && existing.OAuthClientSecret != "" {
		return fmt.Errorf(
			"url points at a new origin, so the stored oauth_client_secret was not carried over: re-enter it for %s",
			originLabel(in.URL),
		)
	}
	return nil
}

// changesOrigin reports whether next names a different scheme+host than prev.
// An unparseable or empty value counts as different: the conservative answer is
// the one that refuses to hand a credential over.
func changesOrigin(prev, next string) bool {
	return originLabel(prev) != originLabel(next)
}

// originLabel renders a URL's scheme+host lowercased, or the whole trimmed
// string when it does not parse (so two identical unparseable values still
// compare equal and an edit elsewhere in the record is not blocked).
func originLabel(raw string) string {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.ToLower(trimmed)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// errImportDuplicate names two entries of one paste that would become the same
// server. A JSON object cannot hold a duplicate key, but two keys differing only
// in case survive decoding and collide on the store's case-insensitive name rule.
var errImportDuplicate = errors.New("names the same server twice")

// There is no ACP wire builder here any more, and no ACPServers.
//
// vibekit sent its server set INLINE on session/new and session/load. It
// now renders KAS's own config file instead (kasfile.go) and sends
// nothing — KAS merges `client > file-based`, so an inline entry would
// win over the file and make every file edit look like a no-op.
//
// EnabledNames stays: the runtime still filters status notifications
// against the set of servers the user has enabled.

// EnabledNames returns the set of enabled server names, for the runtime's
// defensive filtering of init notifications.
func (s *Store) EnabledNames(_ context.Context) map[string]struct{} {
	return s.namesWhere(func(sv *Server) bool { return sv.Enabled })
}

// ConfiguredNames returns every server name this store holds regardless of
// its enabled flag. The runtime subtracts EnabledNames
// from it to identify the one case that still drops a status frame: a server
// vibekit configured and the user switched off.
func (s *Store) ConfiguredNames(_ context.Context) map[string]struct{} {
	return s.namesWhere(func(*Server) bool { return true })
}

// AllNames returns every name reachable through the config file vibekit renders, which is its own servers plus the `powers.mcpServers` block
// KAS reads out of the same file. A name in here that ConfiguredNames does not
// hold came from an installed Power.
//
// The powers read happens outside the store lock — it is file I/O, and holding
// the lock across it would let a slow disk block every CRUD call.
func (s *Store) AllNames(ctx context.Context) map[string]struct{} {
	out := s.powerNames()
	for name := range s.ConfiguredNames(ctx) {
		out[name] = struct{}{}
	}
	return out
}

// namesWhere collects the names of the stored servers matching keep. One helper
// for the two name sets so they cannot drift in how they read the store.
func (s *Store) namesWhere(keep func(*Server) bool) map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]struct{}, len(s.servers))
	for _, sv := range s.servers {
		if keep(sv) {
			out[sv.Name] = struct{}{}
		}
	}
	return out
}
