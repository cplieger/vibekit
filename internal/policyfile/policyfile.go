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
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/envx/yamlenv/v2"
	"go.yaml.in/yaml/v3"
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

// Rule is one policy rule. Field order (capability, effect, match, exclude) is
// the canonical KAS order.
//
// YAML TAGS ONLY. This type had json tags too, on the stated grounds that they
// "keep it decodable from the REST layer" — measured false: nothing in the
// workspace ever encodes or decodes a Rule or a File as JSON. The REST layer
// decodes into its own policyRuleBody and maps the fields across by name, so the
// tags were dead. They also carried a latent surprise: `omitempty` on a slice
// changes the wire under encoding/json/v2, where a nil slice emits `[]` instead
// of being omitted, and sanitizePatterns returns nil for the empty case that the
// workspace relaxation writes for every rule. Deleting them removes both.
type Rule struct {
	Capability string   `yaml:"capability"`
	Effect     string   `yaml:"effect"`
	Match      []string `yaml:"match,omitempty"`
	Exclude    []string `yaml:"exclude,omitempty"`
}

// File is the whole permissions.yaml document.
type File struct {
	Rules []Rule `yaml:"rules"`
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

// suggestedCapabilities seeds the UI picker, and that is now its ONLY job.
//
// It is a hand-copied snapshot of KAS's VALID_CAPABILITIES, and there is no
// discovery method to derive it from — VALID_CAPABILITIES is internal to KAS and
// no `_kiro/*` method exposes it. So it goes stale by construction the day the
// agent server gains a capability, and it used to drive TWO things: this picker
// and rule VALIDATION. That second job made the staleness a refusal: a rule
// naming a capability KAS had gained but this list had not was rejected with a
// 400, so vibekit would refuse to write the very rule the new capability
// existed for, and never offer it in the picker either.
//
// Validation is gone (see SanitizeRule) and the picker is no longer limited to
// this set (see the view handler, which unions in every capability the rules KAS
// reports already use). What is left is a suggestion list: being incomplete now
// costs a dropdown entry, not a write.
// capAll is KAS's umbrella alias meaning every capability it resolves through
// META_CAPABILITIES. One spelling because three separate sets name it — the
// suggestion list, the umbrella set and the relaxation's own first element — and
// a divergence between them would make the relaxation claim a grant it never
// wrote.
const capAll = "all"

var suggestedCapabilities = map[string]struct{}{
	capAll: {}, "builtin": {}, "filesystem": {},
	"fs_read": {}, "fs_write": {}, "shell": {},
	"web_fetch": {}, "web_search": {}, "mcp": {},
	"subagent": {}, "skill": {}, "power": {},
	"context": {}, "diagnostics": {}, "sandbox_network": {},
}

// Capabilities returns the suggested capability set, sorted, for the UI picker.
// It is a starting point, not a permitted set: a caller may write a rule naming
// a capability that is not in here.
func Capabilities() []string {
	out := make([]string, 0, len(suggestedCapabilities))
	for c := range suggestedCapabilities {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// umbrellas are the capability names that stand for a SET rather than for one
// thing. KAS resolves each against META_CAPABILITIES; vibekit only needs to know
// which names are aliases, not what two of them expand to.
var umbrellas = map[string]struct{}{
	capAll: {}, "builtin": {}, "filesystem": {},
}

// allMembers is what KAS's `all` alias expands to, snapshotted off the 2.19.1
// bundle (META_CAPABILITIES.all = BUILTIN + mcp) and verified on the live engine
// through explain.
//
// It is here for ONE purpose: to compute what `all` does NOT cover, so the
// relaxation can name the remainder explicitly. It validates nothing, so going
// stale cannot fail closed — a member KAS adds later is simply also covered by
// the `all` rule the switch writes, and a member KAS removes would leave that
// capability asking, which TestRelaxCapabilities_ExactSet turns into a visible
// decision rather than a silent gap.
//
// sandbox_network is absent on purpose: it is a real capability in KAS's
// VALID_CAPABILITIES and genuinely not a member of `all`, which is the whole
// reason the relaxation cannot be the single word "all".
var allMembers = map[string]struct{}{
	"fs_read": {}, "fs_write": {}, "shell": {},
	"web_fetch": {}, "web_search": {}, "mcp": {},
	"subagent": {}, "skill": {}, "power": {},
	"context": {}, "diagnostics": {},
}

// RelaxCapabilities returns the capability set the Settings -> Permissions
// workspace relaxation writes broad allow rules for, sorted. It is the broadest
// grant a permissions file can express.
//
// DERIVED as `all` plus every non-umbrella capability that alias does not cover,
// which today is exactly {all, sandbox_network}, rather than listed — so it
// cannot name a capability the vocabulary snapshot does not have, and a change to
// either input is a visible decision.
//
// TWO rules rather than one, because `all` is an alias over a fixed table and not
// a wildcard: writing only `all` would leave sandbox_network asking while a
// switch that says it allows everything claimed otherwise. Two rather than the
// twelve discrete names for the opposite reason: eleven of them would be pure
// noise in the Active policy list, and keeping the alias is what makes a
// capability a later KAS version adds to BUILTIN allowed with no vibekit release,
// which is what someone who pressed this switch meant.
//
// It writes one bare rule per member, which is what makes it exactly reversible:
// Signature keys on capability + effect + globs, so removing the same bare rules
// removes precisely what was written and leaves any hand-authored narrower rule
// for the same capability untouched.
//
// ONE switch, not a ladder. An "everyday" rung that withheld `power` was built
// and then removed (2026-08-25) on the user's call: two controls whose only
// difference is one capability cost a reveal rule, a cascade rule and a second
// status line, and the narrow shape stays expressible by hand as `all: allow`
// plus `power: ask` — which works cleanly, since deny > ask > allow and `power`
// has none of shell's parse-driven ask paths. What the switch grants that a user
// should know about is therefore stated in its confirm rather than withheld by
// omission: a power runs its author's code at the user's privilege, and Cedar is
// the only guard, because a power's manifest carries no permissions field.
//
// What it CANNOT do, because effects resolve by restrictiveness and a hardcoded
// scope sits above every file: the kiro scope still denies writes under
// ~/.kiro/settings, .kiro/settings and ~/.kiro/workspace-roots, and still asks
// before writing .git/**, .kiro/agents/**, .kiro/hooks/**, .vscode/** and
// **/*.code-workspace. Measured on the live engine with an all=allow rule in
// force. The UI says so beside the switch; a control that implied otherwise would
// be the same defect as one that silently does nothing.
func RelaxCapabilities() []string {
	out := []string{capAll}
	for c := range suggestedCapabilities {
		if _, alias := umbrellas[c]; alias {
			continue
		}
		if _, covered := allMembers[c]; covered {
			continue
		}
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// maxCapabilityLen bounds a capability token. KAS's own names are under 16
// characters; this is generous headroom, and its job is only to keep a
// pathological value out of the file (see SanitizeRule).
const maxCapabilityLen = 128

// Errors surfaced to the HTTP edge.
//
// There is deliberately no "unknown capability" error. The capability VOCABULARY
// is KAS's to define and KAS's to enforce, and duplicating it here bought nothing
// while costing the ability to write a rule for any capability newer than this
// file.
//
// How KAS reports one, read off 2.18.0, because the obvious guess is wrong: an
// unrecognised capability is NOT fatal. validateRule returns
// `{rule: null, warning: "Skipping rule N in <source>: unknown capability …"}`,
// so that ONE rule is dropped and the rest of the file still loads — unlike a bad
// effect, which throws PolicyParseError and fails the whole file (vibekit cannot
// write that: ValidEffect gates it). Because the entry is `fatal: false`, it does
// NOT arrive on _kiro/policy/error, which KAS emits only `if (hasFatalErrors)`.
// It rides _kiro/policy/changed instead, with `status: "success"` and the warning
// in that notification's `errors` array — which vibekit decodes
// (translate/policy.go) into the permissions_changed SSE, and the client renders
// from `payload.errors` in permissions-ui.ts. So the user IS told; the channel is
// just not the one named "error".
var (
	ErrInvalidScope    = errors.New("scope must be user or workspace")
	ErrInvalidEffect   = errors.New("effect must be allow, deny, or ask")
	ErrCapabilityShape = errors.New("capability must be a non-empty token with no control characters")
	ErrTooManyRules    = errors.New("policy file has too many rules")
	ErrPatternTooLong  = errors.New("match/exclude pattern too long")
	ErrPatternInvalid  = errors.New("match/exclude pattern contains invalid characters")
)

// ValidScope reports whether scope is writable by vibekit.
func ValidScope(scope string) bool {
	return scope == ScopeUser || scope == ScopeWorkspace
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

// Roots are the two filesystem roots a permissions.yaml path resolves against.
//
// A struct because PathFor used to take them as `(scope, home, workDir string)`
// and two of those three were paths: a transposition compiled, resolved, and
// wrote a security policy file under the wrong root — user-scope rules landing
// beneath a workspace hash KAS reads for a different workspace, or workspace
// rules at the user path where they apply everywhere. Nothing detects that; the
// file is valid YAML at a valid location and KAS loads it. Named fields make the
// swap unrepresentable, and a runtime guard could not have caught it at all
// (both values are absolute directories that exist).
type Roots struct {
	// Home is the base KAS resolves both scopes from ($HOME).
	Home string
	// WorkDir is the bridge's cwd, needed only for the workspace scope.
	WorkDir string
}

// PathFor returns the permissions.yaml path for the given scope.
//
// scope stays a separate parameter: it is the discriminator this switches on,
// and confusing it with a root is already loud (a path is not "user" or
// "workspace", so it returns ErrInvalidScope). The silent mistake was the pair.
func PathFor(scope string, roots Roots) (string, error) {
	switch scope {
	case ScopeUser:
		return filepath.Join(roots.Home, ".kiro", "settings", filename), nil
	case ScopeWorkspace:
		return filepath.Join(roots.Home, ".kiro", "workspace-roots",
			WorkspaceHash(roots.WorkDir), filename), nil
	default:
		return "", ErrInvalidScope
	}
}

// Load reads and parses a permissions file. A missing file yields an empty
// File and nil error (the common "no rules yet" case). Parse failures and
// oversize files are errors so the caller never silently clobbers a
// hand-authored file it couldn't understand.
//
// The open is atomicfile.OpenRegular rather than os.ReadFile for two reasons,
// both measured. A FIFO at the name blocks os.ReadFile in open(2) with no
// context deadline able to rescue it (still blocked past 2s on go1.27.0), and
// this file lives under $HOME/.kiro, which the agent's own shell can write — so
// one mkfifo wedged the whole permissions REST surface permanently. And
// os.ReadFile sized its buffer from the file and read all of it BEFORE the
// maxPolicyFileSize check ran on the result, so the 1 MiB bound was enforced
// after an arbitrarily large file had been pulled into memory; ReadBoundedFile
// stats the descriptor first. OpenRegular also refuses a symlink at the final
// component, which matches Save: atomicfile's write entry points already refuse
// to write through one, so a policy vibekit would not write is now a policy it
// will not read either.
//
// The path is made absolute first because OpenRegular requires that. os.ReadFile
// resolved a relative path against the process cwd, and filepath.Abs preserves
// exactly that, so no caller's meaning changes.
func Load(path string) (*File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", filename, err)
	}
	fh, _, err := atomicfile.OpenRegular(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &File{Rules: []Rule{}}, nil
		}
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	data, err := atomicfile.ReadBoundedFile(context.Background(), fh, maxPolicyFileSize)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return &File{Rules: []Rule{}}, nil
	}
	// Reject a multi-document file loudly: yaml.Unmarshal reads only the
	// first document, so everything below a stray "---" would half-load
	// here and then be silently dropped by the next Save — silent rule
	// loss in a security policy file.
	if err := yamlenv.CheckSingleDocument(data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
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
	// WithMaxBytes mirrors Load's size guard: Save can never persist a
	// policy file its own Load would reject as oversize.
	_, err = atomicfile.WriteFile(ctx, path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700),
		atomicfile.WithMaxBytes(maxPolicyFileSize))
	return err
}

// SanitizeRule validates + normalizes a rule for writing. It trims and de-dups
// match/exclude, enforces the length/count caps, checks the effect, and checks
// the capability's SHAPE. It does NOT default the effect — the caller applies
// the conservative "default to ask" policy so the choice is explicit at the
// edge.
//
// The split on capability is deliberate. Its VOCABULARY is not checked: an
// unrecognised name is written through, because KAS's loader is the authority on
// which capabilities exist and reports the skip on _kiro/policy/changed's
// `errors` array (see the Errors block above — it is non-fatal, so it does not
// reach _kiro/policy/error). Its SHAPE is checked, in the same class as the
// pattern checks below: an empty, oversized or control-character-bearing token
// is not a capability KAS could ever have, so forwarding one only puts a rule in
// a security policy file that the user then has to hand-edit out.
func SanitizeRule(r *Rule) (Rule, error) {
	capability := strings.TrimSpace(r.Capability)
	if capability == "" || len(capability) > maxCapabilityLen ||
		!utf8.ValidString(capability) || strings.ContainsFunc(capability, isCtrl) {
		return Rule{}, ErrCapabilityShape
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
	return Rule{Capability: capability, Effect: r.Effect, Match: match, Exclude: exclude}, nil
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

// isCtrl reports whether r is a control character, which a capability token or a
// match pattern may not contain.
//
// unicode.IsControl rather than the `r < 0x20 || r == 0x7f` this used to be: that
// form covered C0 and DEL and left the whole C1 block (U+0080-U+009F) through,
// 32 runes the gate's own doc says it rejects. Measured on go1.27.0 —
// unicode.Cc has 65 members and the hand-rolled predicate matched 33 of them.
// The direction of that gap is what makes it worth closing: this is a REFUSE
// gate, so a missed rune fails OPEN and the pattern lands in permissions.yaml,
// where U+0085 NEXT LINE is a line break to a good many renderers.
//
// Version-stable by construction, so this does not trade one exposure for
// another: Cc is the fixed set U+0000-U+001F plus U+007F-U+009F, it cannot gain
// members, and the changelog's Unicode 15-to-17 diff measures it unmoved at 65.
func isCtrl(r rune) bool { return unicode.IsControl(r) }

// Signature is the dedup/equality key for a rule: capability + effect +
// sorted match + sorted exclude. Mirrors KAS ruleSignature so vibekit's
// notion of "same rule" matches the engine's.
func Signature(r *Rule) string {
	m := slices.Clone(r.Match)
	e := slices.Clone(r.Exclude)
	slices.Sort(m)
	slices.Sort(e)
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

// SetProfileRules makes rules the security profile's whole contribution to this
// file, preserving every rule the profile mechanism did not write.
//
// It drops every rule whose Signature appears in [ProfileOwnedRules] and then
// Upserts each of rules, so a selection replaces the OUTGOING profile's rules and
// nothing else. Returns ErrTooManyRules when the file is already at the cap.
// SetProfileRules(nil) is the remove-and-add-nothing case, which is what a
// restrictive rung and the workspace file both want.
//
// MERGE by ownership, and Signature is the only ownership handle there is: a rule
// carries no provenance in the file, and no RPC reports which writer produced one.
// The alternative was the blanket overwrite this replaced, which destroyed a
// hand-authored rule on every profile click without saying so.
//
// Three consequences, all deliberate:
//
//   - A hand-authored rule that happens to be byte-identical to one a profile
//     writes — `capability: all, effect: allow` with no globs — is indistinguishable
//     from the profile's own and IS removed. Removal is the right side to err on for
//     that exact shape, because "the profile is the policy" is what the panel
//     promises about it.
//   - A NARROWER rule for the same capability survives (`all: allow` with a match
//     list is a different Signature), which is the property
//     TestRelaxCapabilities_RulesAreExactlyReversible already pins.
//   - A surviving deny or ask still beats the profile's allow, because effects
//     resolve by restrictiveness. So a hand-authored rule can outlive a profile
//     change and grant more or less than the profile's name suggests; the panel copy
//     says so, and the Active policy table lists it.
func (f *File) SetProfileRules(rules []Rule) error {
	owned := ProfileOwnedRules()
	sigs := make(map[string]struct{}, len(owned))
	for i := range owned {
		sigs[Signature(&owned[i])] = struct{}{}
	}
	// A fresh slice rather than an in-place filter: reusing the backing array would
	// leave a removed rule's Match and Exclude reachable through the tail of a
	// security policy this file is about to hand to Save. One pass rather than
	// repeated Remove calls, so a hand-edited file holding the same rule twice loses
	// both copies — leaving one would leave the outgoing profile's grant in force.
	kept := make([]Rule, 0, len(f.Rules))
	for i := range f.Rules {
		if _, isProfileRule := sigs[Signature(&f.Rules[i])]; isProfileRule {
			continue
		}
		kept = append(kept, f.Rules[i])
	}
	f.Rules = kept
	for i := range rules {
		if _, err := f.Upsert(&rules[i]); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes the first rule matching r by Signature. Returns true if a
// rule was removed.
func (f *File) Remove(r *Rule) bool {
	sig := Signature(r)
	for i := range f.Rules {
		if Signature(&f.Rules[i]) == sig {
			// slices.Delete, not append(a[:i], a[i+1:]...): it zeroes the vacated
			// tail, so the removed rule's Match and Exclude slices are not still
			// reachable through the backing array of a security policy the caller
			// is about to hand to Save.
			f.Rules = slices.Delete(f.Rules, i, i+1)
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
			f.Rules = slices.Delete(f.Rules, idx, idx+1)
			return true
		}
	}
	f.Rules[idx].Effect = effect
	return true
}
