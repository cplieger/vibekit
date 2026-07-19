// Package policyfile reads and writes kiro-cli's native Cedar permission
// policy files (permissions.yaml) for the user and workspace scopes.
//
// vibekit is the sole programmatic writer of these files on the acp bridge:
// KAS only READS them there (its acp consent dialog offers allow_once /
// reject_once, never a persisted "always", so KAS's own addRuleToFile
// never runs on the acp path). KAS hot-reloads the file on change via a
// chokidar watcher, emitting _kiro/policy/changed — so a write here takes
// effect on every live session with no bridge restart (verified live).
//
// On-disk shape (verified against the KAS 2.12 acp-server bundle + live):
//
//	rules:
//	  - capability: fs_write        # required; one of the known capabilities
//	    effect: ask                 # required; allow | deny | ask
//	    match: ["src/**"]           # optional glob list
//	    exclude: ["**/secret.txt"]  # optional glob list
//
// Load tolerates BOTH block YAML (KAS's / a hand-editing user's format) and
// JSON (JSON is valid YAML 1.2). We MARSHAL block YAML so files stay human
// readable and consistent with KAS's own writer.
//
// Path facts (verified live — KAS resolves the base from $HOME via
// os.homedir(), NOT KIRO_HOME):
//
//	user:      <home>/.kiro/settings/permissions.yaml
//	workspace: <home>/.kiro/workspace-roots/<hash>/permissions.yaml
//	           hash = hex(sha256(abs(workDir)))[:16]   (KAS computeWorkspaceHash)
//
// POLICY_FILENAMES is ["permissions.yaml", "permissions.json"] and KAS's
// loadPolicyFromDir returns the FIRST that EXISTS, so we always target the
// .yaml name to stay authoritative.
package policyfile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v2"
	"gopkg.in/yaml.v3"
)

// filename is the policy file vibekit writes. Always .yaml (first in KAS's
// POLICY_FILENAMES, so it wins over any sibling .json).
const filename = "permissions.yaml"

// Limits guarding hand-edited / API-supplied rule payloads so a pathological
// input can't bloat the file or the CLI argv. Real rules are far smaller.
const (
	maxMatchEntries   = 128
	maxPatternLen     = 512
	maxRulesPerFile   = 512
	maxPolicyFileSize = 1 << 20 // 1 MiB — the policy file is tiny in practice
)

// Rule is one policy rule. yaml tags drive the on-disk block YAML; json tags
// keep it decodable from the REST layer. Field order (capability, effect,
// match, exclude) is the canonical KAS order.
type Rule struct {
	Capability string   `yaml:"capability" json:"capability"`
	Effect     string   `yaml:"effect" json:"effect"`
	Match      []string `yaml:"match,omitempty" json:"match,omitempty"`
	Exclude    []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
}

// File is the whole permissions.yaml document.
type File struct {
	Rules []Rule `yaml:"rules" json:"rules"`
}

// Scope names vibekit can write. Kiro/administration are read-only
// baselines; agent comes from the agent profile; session is runtime.
const (
	ScopeUser      = "user"
	ScopeWorkspace = "workspace"
)

// Effect values.
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
	EffectAsk   = "ask"
)

// validCapabilities is the KAS VALID_CAPABILITIES set (verified against the
// 2.12 bundle), including the meta capabilities (all/builtin/filesystem).
var validCapabilities = map[string]struct{}{
	"all": {}, "builtin": {}, "filesystem": {},
	"fs_read": {}, "fs_write": {}, "shell": {},
	"web_fetch": {}, "web_search": {}, "mcp": {},
	"subagent": {}, "skill": {}, "power": {},
	"context": {}, "diagnostics": {}, "sandbox_network": {},
}

// Capabilities returns the writable capability set, sorted, for the UI
// picker. The meta caps come first (broadest), then the concrete ones.
func Capabilities() []string {
	out := make([]string, 0, len(validCapabilities))
	for c := range validCapabilities {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Errors surfaced to the HTTP edge.
var (
	ErrInvalidScope      = errors.New("scope must be user or workspace")
	ErrInvalidCapability = errors.New("unknown capability")
	ErrInvalidEffect     = errors.New("effect must be allow, deny, or ask")
	ErrTooManyRules      = errors.New("policy file has too many rules")
	ErrPatternTooLong    = errors.New("match/exclude pattern too long")
	ErrPatternInvalid    = errors.New("match/exclude pattern contains invalid characters")
)

// ValidScope reports whether scope is writable by vibekit.
func ValidScope(scope string) bool {
	return scope == ScopeUser || scope == ScopeWorkspace
}

// ValidCapability reports whether capability is a known KAS capability.
func ValidCapability(capability string) bool {
	_, ok := validCapabilities[capability]
	return ok
}

// ValidEffect reports whether effect is allow/deny/ask.
func ValidEffect(effect string) bool {
	return effect == EffectAllow || effect == EffectDeny || effect == EffectAsk
}

// WorkspaceHash mirrors KAS computeWorkspaceHash on Linux: the first 16 hex
// chars of sha256 over the workspace root. The root is canonicalized here —
// absolute, lexically cleaned, no "." / ".." segments, no trailing slash —
// to match the path.resolve output KAS hashes on its side. Canonicalizing at
// the hash (rather than trusting workDir verbatim) keeps vibekit's
// workspace-roots/<hash> directory in lockstep with KAS's for any
// non-canonical KIRO_WORK_DIR — a trailing slash, a "/a/../b" form, or a
// relative value. A divergent hash would silently write workspace-scope rules
// to a directory KAS never reads, so the rules would persist yet never be
// enforced.
func WorkspaceHash(workDir string) string {
	sum := sha256.Sum256([]byte(canonicalWorkDir(workDir)))
	return hex.EncodeToString(sum[:])[:16]
}

// canonicalWorkDir reduces workDir to its absolute, cleaned form (filepath.Abs
// then filepath.Clean, applied exactly once). filepath.Abs already Cleans on
// success — collapsing ".", "..", duplicate slashes, and any trailing slash;
// the explicit Clean guards the error branch (Abs only fails for a relative
// workDir when the process cwd is unavailable) so the fallback stays canonical.
func canonicalWorkDir(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return filepath.Clean(workDir)
	}
	return filepath.Clean(abs)
}

// PathFor returns the permissions.yaml path for the given scope. home is the
// base KAS resolves from ($HOME); workDir is the bridge's cwd (only needed
// for the workspace scope).
func PathFor(scope, home, workDir string) (string, error) {
	switch scope {
	case ScopeUser:
		return filepath.Join(home, ".kiro", "settings", filename), nil
	case ScopeWorkspace:
		return filepath.Join(home, ".kiro", "workspace-roots", WorkspaceHash(workDir), filename), nil
	default:
		return "", ErrInvalidScope
	}
}

// Load reads and parses a permissions file. A missing file yields an empty
// File and nil error (the common "no rules yet" case). Parse failures and
// oversize files are errors so the caller never silently clobbers a
// hand-authored file it couldn't understand.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{Rules: []Rule{}}, nil
		}
		return nil, err
	}
	if len(data) > maxPolicyFileSize {
		return nil, fmt.Errorf("policy file too large: %d bytes", len(data))
	}
	if strings.TrimSpace(string(data)) == "" {
		return &File{Rules: []Rule{}}, nil
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}
	if f.Rules == nil {
		f.Rules = []Rule{}
	}
	return &f, nil
}

// Save writes the file atomically (temp → fsync → rename → dir-fsync) via
// cplieger/atomicfile, mode 0600 (policy is sensitive), creating parent dirs
// at 0700. A crash mid-write can never leave a truncated policy file (which
// would silently drop every rule at the next KAS reload).
func Save(ctx context.Context, path string, f *File) error {
	if f.Rules == nil {
		f.Rules = []Rule{}
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	_, err = atomicfile.WriteFile(ctx, path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700))
	return err
}

// SanitizeRule validates + normalizes a rule for writing. It trims and
// de-dups match/exclude, enforces the length/count caps, and validates the
// capability + effect. It does NOT default the effect — the caller applies
// the conservative "default to ask" policy so the choice is explicit at the
// edge.
func SanitizeRule(r *Rule) (Rule, error) {
	if !ValidCapability(r.Capability) {
		return Rule{}, fmt.Errorf("%w: %q", ErrInvalidCapability, r.Capability)
	}
	if !ValidEffect(r.Effect) {
		return Rule{}, ErrInvalidEffect
	}
	match, err := sanitizePatterns(r.Match)
	if err != nil {
		return Rule{}, err
	}
	exclude, err := sanitizePatterns(r.Exclude)
	if err != nil {
		return Rule{}, err
	}
	return Rule{Capability: r.Capability, Effect: r.Effect, Match: match, Exclude: exclude}, nil
}

func sanitizePatterns(in []string) ([]string, error) {
	if len(in) > maxMatchEntries {
		return nil, ErrTooManyRules
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > maxPatternLen {
			return nil, ErrPatternTooLong
		}
		if !utf8.ValidString(p) || strings.ContainsFunc(p, isCtrl) {
			return nil, ErrPatternInvalid
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func isCtrl(r rune) bool { return r < 0x20 || r == 0x7f }

// Signature is the dedup/equality key for a rule: capability + effect +
// sorted match + sorted exclude. Mirrors KAS ruleSignature so vibekit's
// notion of "same rule" matches the engine's.
func Signature(r *Rule) string {
	m := append([]string(nil), r.Match...)
	e := append([]string(nil), r.Exclude...)
	sort.Strings(m)
	sort.Strings(e)
	var b strings.Builder
	b.WriteString(r.Capability)
	b.WriteByte('|')
	b.WriteString(r.Effect)
	b.WriteString("|m:")
	b.WriteString(strings.Join(m, ","))
	b.WriteString("|x:")
	b.WriteString(strings.Join(e, ","))
	return b.String()
}

// Has reports whether an identical rule (by Signature) already exists.
func (f *File) Has(r *Rule) bool {
	sig := Signature(r)
	for i := range f.Rules {
		if Signature(&f.Rules[i]) == sig {
			return true
		}
	}
	return false
}

// Upsert appends r if no identical rule exists. Returns true if the file
// changed. Discrete-rule semantics (no auto-merge into match arrays) so each
// editor rule maps to exactly one removable YAML rule — predictable for a
// security-sensitive file.
func (f *File) Upsert(r *Rule) (bool, error) {
	if len(f.Rules) >= maxRulesPerFile {
		return false, ErrTooManyRules
	}
	if f.Has(r) {
		return false, nil
	}
	f.Rules = append(f.Rules, *r)
	return true, nil
}

// Remove deletes the first rule matching r by Signature. Returns true if a
// rule was removed.
func (f *File) Remove(r *Rule) bool {
	sig := Signature(r)
	for i := range f.Rules {
		if Signature(&f.Rules[i]) == sig {
			f.Rules = append(f.Rules[:i], f.Rules[i+1:]...)
			return true
		}
	}
	return false
}

// ReplaceEffect changes the effect of the rule matching old (by Signature)
// IN PLACE, preserving its position in the file — one atomic mutation, so
// an in-place edit can never half-apply the way a client-side remove+add
// could. When the resulting rule would duplicate an existing one, the old
// rule is removed instead (the duplicate already expresses the target
// state). Returns true if the file changed: false means the old rule is
// absent or the effect is already effect.
func (f *File) ReplaceEffect(old *Rule, effect string) bool {
	sig := Signature(old)
	idx := -1
	for i := range f.Rules {
		if Signature(&f.Rules[i]) == sig {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	next := f.Rules[idx]
	next.Effect = effect
	nextSig := Signature(&next)
	if nextSig == sig {
		return false // same effect — nothing to change
	}
	for i := range f.Rules {
		if i != idx && Signature(&f.Rules[i]) == nextSig {
			f.Rules = append(f.Rules[:idx], f.Rules[idx+1:]...)
			return true
		}
	}
	f.Rules[idx].Effect = effect
	return true
}
