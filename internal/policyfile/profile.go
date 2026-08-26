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
		ID:      ProfileUnrestricted,
		Presets: []string{PresetAllowAll},
	},
	{
		ID:      ProfileCustom,
		Presets: nil,
	},
}

// Profiles returns the ladder in picker order.
func Profiles() []Profile {
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	// Each entry's Presets slice is shared with the package copy, so hand out a
	// clone: a caller that appended to it would rewrite the profile for every
	// later caller, and the value being mutated here would be a security posture.
	for i := range out {
		out[i].Presets = slices.Clone(out[i].Presets)
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
			return p, true
		}
	}
	return Profile{}, false
}

// DefaultProfile is what an unset or unrecognised setting resolves to. Guarded
// rather than Custom: it reproduces the floor vibekit ships today, where Custom
// would silently remove the fs_read floor from an instance that never chose to.
const DefaultProfile = ProfileGuarded
