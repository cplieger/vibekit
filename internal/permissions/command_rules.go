// Package permissions: per-command rules for shell tool calls.
//
// One store, two rule modes:
//
//   - allow: pattern auto-approves the command in "safe_commands"
//     mode, in addition to the built-in safe list. Historical name
//     "whitelist"; the wire format calls this mode "allow".
//   - deny:  pattern forces a manual prompt regardless of shell
//     policy or trust level. Safety net for destructive one-shots
//     like `git filter-repo` or `rm -rf /` that the user wants to
//     catch even in trust-all mode. Overrides everything else.
//
// Pattern syntax (same shape as ASAI / Claude Code):
//
//   - Exact:    pattern has no '*'; matches the command byte-for-byte.
//   - Wildcard: pattern has one or more '*'; each '*' matches any
//     sequence. Non-star segments must appear in order, the first
//     non-star anchors the start of command, the last non-star
//     anchors the end. Examples: "npm *" matches "npm install"
//     (prefix-style), "* --version" matches "node --version"
//     (suffix-style), "docker * build" matches "docker compose build"
//     (infix).
//
// Deny rules are a UX safety net, not a hard security control. They
// match the literal command string and are sidestepped by whitespace
// variants, absolute paths (/bin/rm), or wrapping shells (sudo, bash
// -c). Treat them as "catch my own typos and footguns", not as a
// defense against a motivated agent. Hard guarantees live in
// Supervised mode and in shell_policy=no_commands.
//
// Allow rules carry a metacharacter caveat: a metachar-free pattern
// only matches commands that are ALSO metachar-free. Without this,
// a trailing `*` would swallow `;`, `|`, $(...), backtick-subst, etc.
// and silently re-enable every shell primitive the safe_commands
// policy was rejecting. Use exact patterns ("npm install") or
// anchored metachar-free wildcards ("npm * install") when you want
// the safe_commands metacharacter guard to keep working. Patterns
// that deliberately include a metacharacter (e.g. "ls | grep foo")
// match metachar-bearing commands because both sides carry the
// metacharacter — the user has explicitly opted in.
//
// Storage: <configDir>/command-rules.json. On first run we auto-migrate
// the old <configDir>/command-whitelist.json (all entries become
// mode=allow) so nobody loses their existing safe list, then delete
// the legacy file. No migration code beyond that.

package permissions

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v2"
)

// RuleMode is "allow" (auto-approve under safe_commands) or "deny"
// (always prompt regardless of policy).
type RuleMode string

// RuleAllow and RuleDeny define the valid RuleMode values for command rules.
const (
	RuleAllow RuleMode = "allow"
	RuleDeny  RuleMode = "deny"
)

// Filename constants — single source of truth for the on-disk paths.
const (
	commandRulesFile     = "command-rules.json"
	commandWhitelistFile = "command-whitelist.json" // legacy, migration only
)

// Rule is one pattern + mode + priority. JSON wire format is the source
// of truth; changing field tags is a breaking change for anyone who
// hand-edited the file. Priority determines evaluation order: higher
// priority rules are checked first. At equal priority, deny wins over
// allow (safe default). Priority 0 is the default for rules created
// without an explicit priority.
type Rule struct {
	Pattern   string   `json:"pattern"`
	Mode      RuleMode `json:"mode"`
	Priority  int      `json:"priority,omitempty"`
	CreatedAt int64    `json:"created_at"`
}

// CommandRules manages the per-command allow/deny list. Replaces the
// split CommandWhitelist / CommandDenylist stores so the UI can
// render a single table and the user has one place to look.
type CommandRules struct {
	entriesPtr atomic.Pointer[[]Rule]
	configDir  string
	entries    []Rule
	mu         sync.Mutex
}

// NewCommandRules loads the rules file from configDir and returns a
// ready-to-use store. Auto-migrates command-whitelist.json on first
// run if command-rules.json doesn't exist yet.
func NewCommandRules(configDir string) *CommandRules {
	r := &CommandRules{configDir: configDir}
	r.load()
	r.entriesPtr.Store(&r.entries)
	return r
}

// normalizeRules trims whitespace from patterns, drops entries with
// empty patterns, and drops entries whose mode is neither allow nor
// deny. Previously unknown modes were coerced to allow; that silently
// converted a typo'd "deni" safety-net rule into an auto-approve
// rule — the worst possible fail direction. Dropping is the safer
// default; the operator sees a warn log on load.
func normalizeRules(in []Rule) []Rule {
	out := make([]Rule, 0, len(in))
	for _, e := range in {
		e.Pattern = strings.TrimSpace(e.Pattern)
		if e.Pattern == "" {
			continue
		}
		if e.Mode != RuleAllow && e.Mode != RuleDeny {
			slog.Warn("command rules: dropping entry with unknown mode",
				"pattern", e.Pattern, "mode", e.Mode)
			continue
		}
		out = append(out, e)
	}
	return out
}

// List returns all rules in insertion order. Returned slice is a copy.
func (r *CommandRules) List() []Rule {
	entries := *r.entriesPtr.Load()
	out := make([]Rule, len(entries))
	copy(out, entries)
	return out
}

// ErrInvalidMode is returned when the caller passes something other
// than "allow" or "deny". Surfaces as a 400 at the HTTP edge.
var ErrInvalidMode = errors.New("mode must be allow or deny")

// Add inserts a rule with the given priority. If the same pattern
// already exists, its mode and priority are updated in place; the
// existing CreatedAt timestamp is preserved so the "first seen"
// chronology in the Permissions UI doesn't reset on a mode flip.
// Empty patterns are silently ignored. On save failure, the in-memory
// state is rolled back to match disk so a subsequent List() doesn't
// lie about what's persisted.
func (r *CommandRules) Add(pattern string, mode RuleMode, priority ...int) error {
	if mode != RuleAllow && mode != RuleDeny {
		return ErrInvalidMode
	}
	prio := 0
	if len(priority) > 0 {
		prio = priority[0]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	if !utf8.ValidString(pattern) {
		return errors.New("pattern contains invalid UTF-8")
	}
	for i, e := range r.entries {
		if e.Pattern != pattern {
			continue
		}
		if e.Mode == mode && e.Priority == prio {
			return nil
		}
		prevMode := e.Mode
		prevPrio := e.Priority
		r.entries[i].Mode = mode
		r.entries[i].Priority = prio
		if err := r.saveLocked(); err != nil {
			r.entries[i].Mode = prevMode
			r.entries[i].Priority = prevPrio
			slog.Warn("command rules: save failed, rolled back mode/priority change",
				"pattern", pattern, "error", err)
			return err
		}
		r.entriesPtr.Store(&r.entries)
		return nil
	}
	r.entries = append(r.entries, Rule{
		Pattern:   pattern,
		Mode:      mode,
		Priority:  prio,
		CreatedAt: time.Now().UnixMilli(),
	})
	if err := r.saveLocked(); err != nil {
		r.entries = r.entries[:len(r.entries)-1]
		slog.Warn("command rules: save failed, rolled back add",
			"pattern", pattern, "mode", mode, "error", err)
		return err
	}
	r.entriesPtr.Store(&r.entries)
	return nil
}

// Remove drops a rule by exact pattern match. Unknown patterns are a
// silent no-op. On save failure, the removed rule is re-inserted at
// its original index so in-memory state matches disk.
func (r *CommandRules) Remove(pattern string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.entries {
		if e.Pattern != pattern {
			continue
		}
		saved := e
		r.entries = append(r.entries[:i], r.entries[i+1:]...)
		if err := r.saveLocked(); err != nil {
			r.entries = slices.Insert(r.entries, i, saved)
			slog.Warn("command rules: save failed, rolled back remove",
				"pattern", pattern, "mode", saved.Mode, "error", err)
			return err
		}
		r.entriesPtr.Store(&r.entries)
		return nil
	}
	return nil
}

func (r *CommandRules) path() string {
	return filepath.Join(r.configDir, commandRulesFile)
}

func (r *CommandRules) legacyPath() string {
	return filepath.Join(r.configDir, commandWhitelistFile)
}

func (r *CommandRules) load() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Current-format file: parse and done. We do NOT fall through
	// to the legacy migration on a read error — that would silently
	// overwrite a broken-but-recoverable file. Operators inspect
	// and repair the current file instead.
	data, err := os.ReadFile(r.path())
	if err == nil {
		var entries []Rule
		if jsonErr := json.Unmarshal(data, &entries); jsonErr != nil {
			slog.Warn("command rules: parse failed, ignoring file",
				"path", r.path(), "error", jsonErr)
			return
		}
		r.entries = normalizeRules(entries)
		r.entriesPtr.Store(&r.entries)
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("command rules: read failed",
			"path", r.path(), "error", err)
		return
	}

	// No current-format file: try legacy migration.
	r.migrateFromLegacy()
}

// migrateFromLegacy reads the old command-whitelist.json, converts
// each entry to mode=allow, and saves the new format. On save success
// the legacy file is deleted so the auto-migration runs at most once
// per install; on save failure the legacy file stays on disk so the
// next boot re-migrates. Must be called with r.mu held.
func (r *CommandRules) migrateFromLegacy() {
	data, err := os.ReadFile(r.legacyPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("command rules: legacy read failed",
				"path", r.legacyPath(), "error", err)
		}
		return
	}
	var legacy []struct {
		Pattern   string `json:"pattern"`
		CreatedAt int64  `json:"created_at"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		slog.Warn("command rules: legacy parse failed",
			"path", r.legacyPath(), "error", err)
		return
	}
	r.entries = make([]Rule, 0, len(legacy))
	for _, e := range legacy {
		r.entries = append(r.entries, Rule{
			Pattern:   e.Pattern,
			Mode:      RuleAllow,
			CreatedAt: e.CreatedAt,
		})
	}
	slog.Info("command rules: migrated legacy whitelist",
		"count", len(r.entries))
	if err := r.saveLocked(); err != nil {
		// Legacy file stays on disk so the next boot re-migrates.
		// Without this log the operator has no breadcrumb for why
		// hand-edits to command-rules.json keep coming back from
		// the dead (each successful save would overwrite them).
		slog.Warn("command rules: migration save failed, will retry next boot",
			"error", err)
		return
	}
	// Save succeeded: remove the legacy file so next boot sees
	// only the current-format store. os.Remove failures beyond
	// ErrNotExist are harmless (orphan file; next boot's load
	// sees the current file and never touches it) but worth a
	// breadcrumb so operators don't wonder why the old file
	// lingers three months later.
	if err := os.Remove(r.legacyPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("command rules: migrated but legacy whitelist removal failed, harmless",
			"path", r.legacyPath(), "error", err)
		return
	}
	slog.Info("command rules: legacy whitelist removed, migration complete",
		"path", r.legacyPath())
}

// saveLocked writes the rules file atomically via write-temp-then-rename
// so a crash between truncate and close can't drop the whole store (which
// would silently lose every deny rule at the next boot's empty-file
// parse).
func (r *CommandRules) saveLocked() error {
	data, err := json.MarshalIndent(r.entries, "", "  ")
	if err != nil {
		return err
	}
	_, err = atomicfile.WriteFile(context.Background(), r.path(), data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700))
	return err
}
