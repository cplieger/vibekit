package policyfile

import "slices"

// Named security profiles for the Settings -> Permissions picker.
//
// A profile is a POSTURE, expressed as a set of KAS policy presets rather than as
// rules vibekit authors. That indirection is the whole design:
// `_meta.kiro.policyPreset` takes preset ids at the session door and KAS resolves
// each against its own registry, injecting the rules at SESSION scope with
// `source: preset:<id>`. So a named profile writes nothing to disk, cannot go
// stale against upstream's judgement about which commands are safe, and applies
// to every session vibekit starts.
//
// Upstream's judgement is the reason not to hand-roll the equivalent rule sets.
// `dev-shell` allows `git add`/`commit`/`pull` and `go build`/`test` while
// deliberately excluding `git push` as irreversible, `reset`/`clean`/`branch -D`
// as destructive, `git config` for secrets, `sed` for `-i`, `awk` for `system()`,
// `find` for `-exec` and `go run` as an arbitrary-path executor. Copying that
// list here would be copying a security review that upstream maintains.
//
// FOUR constraints on the mechanism, all measured off the 2.19.1 bundle and
// recorded in vibekit-acp.md. A preset can only be SELECTED, never authored, and
// `validatePresetIds` fails `session/new` OUTRIGHT on an unknown id — hence
// TestPresetIDs_MatchKAS. No RPC enumerates a preset's rules, so materializing a
// profile into the editable table means reading them back off a live session's
// `_kiro/permissions/list` filtered to `source: preset:*`. A live session's policy
// cannot be changed by the client at all (no `set_config_option` id, no setter),
// so a profile change takes effect when a session next starts. And every shipped
// preset is allow-only, so a profile can only ever widen: "Reads" means writes
// ASK, never that writes are impossible.

// Preset ids KAS's registry accepts. Pinned because an unknown id is not a
// degraded grant but a failed session: validatePresetIds throws InvalidParamsError
// and `session/new` never completes, so an upstream rename would take every chat
// down at its first prompt rather than quietly granting less.
const (
	PresetReadWorkspace = "read-workspace"
	PresetReadOnlyShell = "read-only-shell"
	PresetReadAll       = "read-all"
	PresetEditWorkspace = "edit-workspace"
	PresetDevShell      = "dev-shell"
	PresetAllowAll      = "allow-all"
)

// Profile ids. vibekit's own vocabulary, persisted as a setting, so free to
// differ from KAS's preset names — and they do differ, because a profile is a
// POSTURE while a preset is a rule bundle.
//
// The ladder mirrors KiroCrew's four levels (its picker offers normal /
// trust_reads / trust / yolo) but deliberately not its NAMES. Each of ours is an
// adjective describing the agent's standing, which keeps one part of speech across
// the set and makes the ordering readable: guarded, read-only, trusted,
// unrestricted. "Normal" named a default rather than a behaviour, "reads" was a
// verb doing an adjective's job, and "yolo" is a joke in a security control — the
// honest word for it is what it does, which is remove every restriction vibekit
// can remove.
//
// Crew time-boxes its loosest level with a duration and a one-time
// acknowledgement. Ours deliberately does NOT, by decision: it stays on until it
// is changed, matching the switch it replaces.
const (
	ProfileGuarded      = "guarded"
	ProfileReadOnly     = "read-only"
	ProfileTrusted      = "trusted"
	ProfileUnrestricted = "unrestricted"
	// ProfileCustom sends NO presets, which is what makes the files the whole
	// policy. Note what that costs and why it is still right: without
	// read-workspace there is no fs_read floor, so an EMPTY custom policy makes
	// the agent ask even to read a file. The UI reaches Custom either through the
	// Customize button, which pre-populates the file from the outgoing profile, or
	// by direct selection, which starts blank on purpose.
	ProfileCustom = "custom"
)

// Profile is one entry in the picker.
type Profile struct {
	// ID is the persisted value.
	ID string
	// Presets are the KAS preset ids sent as _meta.kiro.policyPreset. Empty for
	// Custom, which is the difference that makes the files authoritative.
	Presets []string
	// FileRules are the rules this profile writes to the USER-scope permissions
	// file, in ADDITION to sending its presets at the session door — the half that
	// reaches a session vibekit did not open.
	//
	// A preset arrives at SESSION scope bound to the one session it was sent on,
	// and KAS creates a workflow step's session itself with no _meta, so a preset
	// can never reach one. A user-scope file rule is evaluated for every session in
	// the process, step sessions included, which is why the loosest rung carries
	// both halves. Empty on every other rung: a durable allow at user scope
	// survives a restart and applies to every ACP client sharing this HOME, so
	// materialising a restrictive rung's posture would widen it rather than
	// describe it.
	//
	// THE PICKER'S COPY DUPLICATES THIS DISTRIBUTION and the wire does not carry
	// it: vibekit.SecurityProfile ships ID and Presets only, so
	// profileDescription in static-src/permissions-ui.ts states "the only profile
	// that also covers workflow steps" from a hand-maintained copy of which rung
	// holds these rules. A rung gaining FileRules must change that copy in the same
	// commit. What catches the omission is
	// TestProfiles_OnlyTheLoosestRungWritesFileRules, which tables all five rungs
	// against a wantRules boolean — editing that table is the moment to re-read the
	// client text.
	FileRules []Rule
}

// profiles is the ordered ladder, loosest last. Order is part of the contract:
// the picker renders it in this order, so a reader scans from cautious to
// permissive rather than alphabetically.
var profiles = []Profile{
	{
		// The baseline vibekit already ships: reading THIS workspace is free and
		// everything else asks. Named rather than implicit so "no profile" is not
		// a state the picker has to render.
		ID:      ProfileGuarded,
		Presets: []string{PresetReadWorkspace},
	},
	{
		// Read any file on the machine, run read-only commands, reach the web.
		// Every write and every other command still asks.
		//
		// read-all is what separates this from guarded, and it is worth stating
		// plainly because the name could imply otherwise: it grants fs_read
		// OUTSIDE the workspace, so an SSH key or a sibling project is readable
		// without a prompt. The presets are atomic, so this cannot be split from
		// the web access bundled with it. A profile called read-only that still
		// prompted for reads would contradict itself, so the grant stays and the
		// picker's description says where it reaches.
		ID:      ProfileReadOnly,
		Presets: []string{PresetReadWorkspace, PresetReadOnlyShell, PresetReadAll},
	},
	{
		// Edit inside the workspace and run ordinary development commands. The
		// destructive and irreversible ones still ask, which is upstream's line
		// rather than ours.
		ID: ProfileTrusted,
		Presets: []string{
			PresetReadWorkspace, PresetReadOnlyShell, PresetReadAll,
			PresetEditWorkspace, PresetDevShell,
		},
	},
	{
		// capability: all. Not silence: the kiro scope still denies writes under
		// ~/.kiro/settings and still asks before writing .git/**, .kiro/agents/**,
		// .kiro/hooks/** and .vscode/**, because deny and ask both beat allow and
		// that scope sits above every file. The UI says so beside the option.
		//
		// The ONE rung that also writes file rules, and the only one vibekit can
		// author from its own definitions: `allow-all` resolves to a single
		// umbrella, and RelaxCapabilities() is already the derived, tested answer to
		// "the broadest grant a permissions file can express". The rungs below it
		// grant through edit-workspace and dev-shell, whose rule sets are a security
		// review upstream maintains (see the package comment) — spelling those to
		// disk would freeze upstream's judgement at today's bundle and make it
		// vibekit's to keep current.
		ID:        ProfileUnrestricted,
		Presets:   []string{PresetAllowAll},
		FileRules: relaxRules(),
	},
	{
		ID:      ProfileCustom,
		Presets: nil,
	},
}

// relaxRules maps RelaxCapabilities() to the bare allow rules the loosest profile
// writes: one rule per member, no match and no exclude.
//
// Bareness is load-bearing rather than incidental. Signature keys on capability +
// effect + sorted globs, so a bare rule is removable by exactly the value that
// wrote it — which is what lets [File.SetProfileRules] take a profile's rules back
// out of a file without disturbing a narrower hand-authored rule for the same
// capability. A match list on these would break that.
func relaxRules() []Rule {
	caps := RelaxCapabilities()
	out := make([]Rule, 0, len(caps))
	for _, c := range caps {
		out = append(out, Rule{Capability: c, Effect: EffectAllow})
	}
	return out
}

// ProfileOwnedRules returns every rule the profile mechanism itself could have
// written, deduplicated by Signature. It is the set a selection REMOVES from a
// writable file before writing the incoming rung's own rules.
//
// The UNION over the whole ladder rather than the outgoing rung's set, for two
// reasons. Switching from the loosest rung to a restrictive one has to genuinely
// narrow, and a narrowing that left the previous rung's `all: allow` standing
// would be the worst failure this mechanism could ship — so the removal set cannot
// depend on correctly identifying which rung is being left, which is a setting
// that can be absent or stale. And a rung added later must not orphan its rules on
// disk when a user moves off it; the union is a property of the code instead.
//
// It also reverses the retired workspace-relaxation checkbox for free: that
// checkbox wrote byte-identical bare rules from the same RelaxCapabilities() set,
// so the first profile selection is its own migration with no special case.
func ProfileOwnedRules() []Rule {
	var out []Rule
	seen := make(map[string]struct{})
	for i := range profiles {
		for j := range profiles[i].FileRules {
			r := profiles[i].FileRules[j]
			sig := Signature(&r)
			if _, dup := seen[sig]; dup {
				continue
			}
			seen[sig] = struct{}{}
			out = append(out, cloneRule(r))
		}
	}
	return out
}

// cloneRules deep-copies a rule slice, glob lists included. A bare slices.Clone
// would leave every rule's Match and Exclude sharing a backing array with the
// package copy, and the value being handed out is a security posture.
func cloneRules(in []Rule) []Rule {
	if in == nil {
		return nil
	}
	out := make([]Rule, len(in))
	for i := range in {
		out[i] = cloneRule(in[i])
	}
	return out
}

// cloneRule deep-copies one rule. Rule is a value, so only its two glob slices
// need it.
func cloneRule(r Rule) Rule {
	r.Match = slices.Clone(r.Match)
	r.Exclude = slices.Clone(r.Exclude)
	return r
}

// Profiles returns the ladder in picker order.
func Profiles() []Profile {
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	// Each entry's Presets and FileRules slices are shared with the package copy,
	// so hand out a clone: a caller that appended to one would rewrite the profile
	// for every later caller, and the value being mutated here would be a security
	// posture.
	for i := range out {
		out[i].Presets = slices.Clone(out[i].Presets)
		out[i].FileRules = cloneRules(out[i].FileRules)
	}
	return out
}

// ProfileFor returns the profile with the given id, and whether it exists.
//
// Absence is reported in band rather than through a default, because the two
// callers want opposite things: the settings reader wants to fall back to Normal
// with a logged reason, and the session door wants to send nothing rather than
// guess a posture. A silent default would hand one of them the wrong answer.
func ProfileFor(id string) (Profile, bool) {
	for i := range profiles {
		if profiles[i].ID == id {
			p := profiles[i]
			p.Presets = slices.Clone(p.Presets)
			p.FileRules = cloneRules(p.FileRules)
			return p, true
		}
	}
	return Profile{}, false
}

// DefaultProfile is what an unset or unrecognised setting resolves to. Guarded
// rather than Custom: it reproduces the floor vibekit ships today, where Custom
// would silently remove the fs_read floor from an instance that never chose to.
const DefaultProfile = ProfileGuarded
