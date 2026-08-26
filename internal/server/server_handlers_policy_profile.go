package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/policyfile"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// POST /api/permissions/profile — select the named security posture.
//
// A selection has TWO halves, and the second one is why this endpoint has the
// shape it does. The presets ride the session door (_meta.kiro.policyPreset on
// session/new), and KAS injects them at SESSION scope bound to the one session
// they arrived on. KAS creates a workflow STEP's session itself, with no _meta and
// no vibekit involvement, so a preset can never reach one — measured on this
// container: 280 permission prompts, every one of them on a step session and none
// on a seeded one. A USER-scope rule in permissions.yaml IS evaluated for every
// session in the process, step sessions included, so the loosest rung writes its
// posture there as well. Every other rung writes none, because a durable allow at
// user scope survives a restart and applies to any other ACP client sharing this
// HOME; policyfile.Profile.FileRules records that decision in full.
//
// It owns that rule set by MERGE, not by replacement: only the rules the profile
// mechanism itself could have written are removed (policyfile.ProfileOwnedRules),
// so a hand-authored rule survives a profile change and keeps applying beside the
// new profile's own. The blanket overwrite this replaced destroyed such a rule
// silently on every click. Both facts are in the panel copy, because a description
// promising a posture the code does not deliver is the defect being fixed and the
// inverse would be the same defect.
//
// Its own endpoint rather than a key on PATCH /api/settings: persisting the
// setting is the smallest part of what a selection does, and a settings PATCH that
// silently rewrote policy files would be a side effect nobody reading the route
// could predict.
//
// THREE files and no cross-file atomicity to be had, so a defined ORDER plus a
// compensating restore is what keeps the two halves from disagreeing: snapshot both
// writable files, write the user file, write the workspace file, then config.json;
// on a failure at either write, put both files back and answer 500. The FILES are
// restored rather than the setting because they are the half that has already taken
// effect — KAS watches them and hot-reloads, while the setting only reaches a
// session at its next start — so a part-way failure leaves both halves naming the
// OLD profile. Accepted cost: KAS sees two reloads on that path, the new rules and
// then the restore. Either ordering has such a window; this one keeps it on the
// half that can be undone.
//
// Two doors into Custom, and Seed is the difference. The Customize button sends
// seed=true, which MATERIALISES what is currently in force into the user file so
// the editable table opens on the outgoing profile's rules as a starting point.
// Selecting Custom from the list sends seed=false, which adds nothing — and, since
// the merge preserves what it does not own, leaves the user's own rules standing
// rather than wiping the file.

// errNoLivePolicy is returned when the profile's own rules cannot be read, which
// is the one condition that must not degrade into a silent blank slate: an empty
// Custom drops every grant, and it is indistinguishable on the wire from a session
// that simply has not started yet.
var errNoLivePolicy = errors.New("no live policy to read preset rules from")

// profileBody is the request. Seed is only meaningful when switching TO custom,
// and the handler refuses it otherwise rather than ignoring it: a caller asking to
// seed a named profile has misunderstood which way the materialisation runs, and
// answering 200 would hide that.
type profileBody struct {
	Profile string `json:"profile"`
	Seed    bool   `json:"seed"`
}

// presetRuleSource is the prefix KAS stamps on a rule it resolved from a policy
// preset (`preset:<id>`). Materialisation keys on it because there is no RPC that
// enumerates a preset's rules: the only place a profile's own rules are readable
// is the live policy view of a session that opened with them.
const presetRuleSource = "preset:"

func (s *Server) handlePolicyProfile(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body profileBody
	if !decodeBody(w, r, &body) {
		return
	}
	profile, ok := policyfile.ProfileFor(body.Profile)
	if !ok {
		httpreply.BadRequest(w, "unknown security profile: "+body.Profile)
		return
	}
	if body.Seed && profile.ID != policyfile.ProfileCustom {
		httpreply.BadRequest(w, "seed applies only when switching to the custom profile")
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		httpreply.InternalError(w, err)
		return
	}
	roots := policyfile.Roots{Home: home, WorkDir: s.workDir}

	// Materialise BEFORE anything is cleared, and the reason is FAIL-CLOSED rather
	// than ordering: the rules come from the live session rather than from these
	// files, so clearing first would not destroy the source, but it would destroy
	// the user's policy before discovering the copy is impossible. Reading first
	// means a Customize that cannot see the profile leaves the profile in place
	// instead of landing on an empty Custom, which drops every grant they had.
	var seeded []policyfile.Rule
	if body.Seed {
		seeded, err = s.presetRulesInForce(r.Context())
		if err != nil {
			webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
				httpreply.ErrorJSON("cannot read the current profile's rules to copy them; nothing was changed"))
			return
		}
	}
	// The user-scope rules this selection writes: a seed copies the outgoing
	// profile's, a named rung writes its own (empty on every rung but the loosest),
	// and Custom picked from the list writes none and keeps whatever is there.
	userRules := profile.FileRules
	if body.Seed {
		userRules = seeded
	}
	// Read both files before touching either, which buys two things from one read.
	// The overwrite this replaced never read them, so an unparseable hand-edited
	// file was destroyed silently; this adopts policyRuleAdd's refusal instead. And
	// the same read is the snapshot a failed write is put back from.
	snap, badScope, err := snapshotPolicyFiles(roots)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusConflict, httpreply.ErrorJSON(
			"the existing "+badScope+"-scope permissions file could not be read; edit it manually"))
		return
	}
	if err := writeProfilePolicy(r.Context(), roots, userRules); err != nil {
		s.failProfileSelection(r.Context(), w, roots, snap, err)
		return
	}
	if err := s.persistProfile(r.Context(), profile.ID); err != nil {
		s.failProfileSelection(r.Context(), w, roots, snap, err)
		return
	}
	// The presets ride the session door, so the sessions already running still
	// carry the OLD profile. Recycling the utility session is what makes GET
	// /api/permissions describe the new one; chat bridges pick it up when their
	// session next starts or loads, which is the documented limit rather than an
	// oversight (KAS exposes no way to change a live session's policy).
	if s.policyReload != nil {
		s.policyReload.RestartUtilitySession()
	}
	slog.Info("security profile selected", "profile", profile.ID,
		"presets", profile.Presets, "seeded_rules", len(seeded), "file_rules", len(userRules))
	webhttp.Ok(w)
	s.agent.Broadcast(r.Context(), vibekit.NewEvent(vibekit.EventSettingsUpdated, "", vibekit.SettingsUpdatedPayload{}))
	s.agent.Broadcast(r.Context(), vibekit.NewEvent(vibekit.EventPermissionsChanged, "",
		vibekit.PermissionsChangedPayload{Status: "success"}))
}

// presetRulesInForce reads the rules the ACTIVE profile's presets contributed to
// the live policy, as writable file rules.
//
// Session scope plus a `preset:` source is the whole filter, and both halves are
// needed: session scope alone would also catch a consent the user granted for one
// session, and the source prefix alone would be a claim about a scope KAS could
// change. Rules are sanitized on the way through, so a pattern KAS accepts but the
// file format bounds is refused here rather than written and rejected on reload.
func (s *Server) presetRulesInForce(ctx context.Context) ([]policyfile.Rule, error) {
	if s.policy == nil {
		return nil, errNoLivePolicy
	}
	live, err := s.policy.PolicyList(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]policyfile.Rule, 0, len(live))
	for i := range live {
		r := &live[i]
		if r.Scope != "session" || !strings.HasPrefix(r.Source, presetRuleSource) {
			continue
		}
		clean, sErr := policyfile.SanitizeRule(&policyfile.Rule{
			Capability: r.Capability, Effect: r.Effect,
			Match: r.Match, Exclude: r.Exclude,
		})
		if sErr != nil {
			slog.Warn("skipping a preset rule that the file format cannot hold",
				"capability", r.Capability, "source", r.Source, "error", sErr)
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		// An empty result is indistinguishable from "the session has not started
		// yet", and the two want opposite outcomes, so refuse rather than write an
		// empty file that reads as a deliberate blank slate.
		return nil, errNoLivePolicy
	}
	return out, nil
}

// policySnapshot is both writable files' rules as they stood before a selection,
// keyed by scope.
//
// atomicfile gives per-FILE atomicity and a selection writes three files, so
// cross-file atomicity is not something the library can provide and this endpoint
// does not invent one. A snapshot plus a compensating restore is the achievable
// form of "the panel never disagrees with its own policy".
type policySnapshot map[string][]policyfile.Rule

// writableScopes are the two scopes a selection touches, in the order it touches
// them: user first, because that is the scope carrying the posture. One list for
// the snapshot, the write and the restore, so the three cannot disagree about
// which files are in play.
func writableScopes() []string {
	return []string{policyfile.ScopeUser, policyfile.ScopeWorkspace}
}

// snapshotPolicyFiles reads both writable files so a failed selection can be put
// back. A parse error propagates: the caller refuses the request rather than
// overwriting a file vibekit could not understand.
//
// The middle result is the scope whose file could not be read, and it is
// meaningful ONLY when the error is non-nil. It exists because this reads TWO
// files where policyRuleAdd's identically-worded refusal reads one, so the message
// left the user to guess which of them to go and fix. The cause and the resolved
// PATH go to the log instead of the response: Load fails for a FIFO at the name,
// an oversize file, a symlink at the final component, a multi-document YAML and
// ordinary I/O, none of which the user can tell apart from the message, and a
// filesystem path in an HTTP error body is a detail no client has a use for.
func snapshotPolicyFiles(roots policyfile.Roots) (policySnapshot, string, error) {
	snap := make(policySnapshot, len(writableScopes()))
	for _, scope := range writableScopes() {
		path, err := policyfile.PathFor(scope, roots)
		if err != nil {
			slog.Warn("a writable permissions file path could not be resolved, so the profile selection was refused",
				"scope", scope, "error", err)
			return nil, scope, err
		}
		f, err := policyfile.Load(path)
		if err != nil {
			slog.Warn("a writable permissions file could not be read, so the profile selection was refused",
				"scope", scope, "path", path, "error", err)
			return nil, scope, err
		}
		snap[scope] = f.Rules
	}
	return snap, "", nil
}

// writeProfilePolicy makes userRules the profile's contribution to the user file
// and takes the profile mechanism's rules out of the workspace one, preserving
// every hand-authored rule in both.
//
// Only the user file receives content, and that is the fix rather than a
// simplification. What was MEASURED (2026-08-26, kiro-cli 2.19.2, two states of
// this container) is that a USER-scope file rule is evaluated for a session KAS
// created itself — which every workflow step's session is — while a session preset
// is not: 145 step-session asks with `rules: []`, zero with one user-scope shell
// allow. Do NOT restate that as "user scope is the only scope KAS evaluates for
// such a session": workspace scope was never measured, and KAS loads both files
// process-wide and resolves by restrictiveness regardless of scope, so a
// hand-authored workspace allow may well reach a step session too. The reason the
// workspace file receives no CONTENT is one-writer-of-one-posture, not
// unreachability — a second file granting the same posture is a second thing to
// keep in step with the picker, and it buys no precision when one instance is one
// HOME and one workspace root. It still gets the removal half, so a rule the
// previous release's relaxation wrote there cannot outlive the profile that
// replaced it, and a leftover workspace rule cannot widen or narrow the posture
// with nothing on screen saying so.
func writeProfilePolicy(ctx context.Context, roots policyfile.Roots, userRules []policyfile.Rule) error {
	for _, scope := range writableScopes() {
		path, err := policyfile.PathFor(scope, roots)
		if err != nil {
			return err
		}
		f, err := policyfile.Load(path)
		if err != nil {
			return err
		}
		var incoming []policyfile.Rule
		if scope == policyfile.ScopeUser {
			incoming = userRules
		}
		if err := f.SetProfileRules(incoming); err != nil {
			return err
		}
		if err := policyfile.Save(ctx, path, f); err != nil {
			return err
		}
	}
	return nil
}

// restorePolicyFiles puts both writable files back to snap, compensating a
// selection that failed after writing them.
//
// Its own failure is logged with the path, because at that point the file grants a
// posture config.json does not name and only the operator can reconcile the two;
// the caller then answers a 500 naming both files rather than the generic one.
func restorePolicyFiles(ctx context.Context, roots policyfile.Roots, snap policySnapshot) error {
	for _, scope := range writableScopes() {
		path, err := policyfile.PathFor(scope, roots)
		if err != nil {
			return err
		}
		if err := policyfile.Save(ctx, path, &policyfile.File{Rules: snap[scope]}); err != nil {
			slog.Error("could not restore a permissions file after a failed profile selection",
				"scope", scope, "path", path, "error", err)
			return err
		}
	}
	return nil
}

// failProfileSelection answers a selection that failed after the files were
// touched: put them back, then report. Never a 200 — the files and config.json
// would then name different postures, which is exactly what the write order and
// this restore exist to prevent.
//
// A restore that itself FAILS is the one path that leaves a file granting a
// posture config.json does not name, so it broadcasts permissions_changed on the
// way out. That is not decoration: the Active-policy table reads the live policy,
// so the refetch is what makes it show the rules that are actually in force and
// visibly disagree with the picker beside it, instead of that disagreement waiting
// on KAS's own reload notification to happen to arrive.
//
// ErrTooManyRules is separated from the generic 500 because it is not vibekit
// failing: it is the user's own file at the 512-rule cap, a condition they caused
// and can fix, and the sibling rule endpoint already answers 400 for it. Folding
// it into an internal error made every profile selection report a bug in vibekit
// for a full file.
func (s *Server) failProfileSelection(ctx context.Context, w http.ResponseWriter, roots policyfile.Roots,
	snap policySnapshot, cause error,
) {
	if err := restorePolicyFiles(ctx, roots, snap); err != nil {
		s.agent.Broadcast(ctx, vibekit.NewEvent(vibekit.EventPermissionsChanged, "",
			vibekit.PermissionsChangedPayload{Status: "failed"}))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON(
			"the profile could not be applied and the previous rules could not be put back; "+
				"inspect permissions.yaml under ~/.kiro/settings and ~/.kiro/workspace-roots"))
		return
	}
	if errors.Is(cause, policyfile.ErrTooManyRules) {
		httpreply.BadRequest(w,
			"the permissions file is at its rule limit; remove a rule from the table and try again")
		return
	}
	httpreply.InternalError(w, cause)
}

// persistProfile writes the profile id into config.json, merging rather than
// replacing so it cannot drop a sibling preference.
//
// It takes the same settingsMu the settings writer takes: two concurrent writers
// of one file would otherwise read-modify-write over each other, and one of them
// deleting a preference is exactly the silent loss the atomic write exists to
// prevent.
func (s *Server) persistProfile(ctx context.Context, id string) error {
	path := filepath.Join(s.configDir, settings.Filename)
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	merged := readExistingSettings(path)
	raw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	merged[settings.KeySecurityProfile] = raw
	pretty, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	_, err = atomicfile.WriteFile(ctx, path, append(pretty, '\n'),
		atomicfile.WithMode(0o644), atomicfile.WithMkdirMode(0o755))
	return err
}

// securityProfileCatalog projects policyfile's ladder onto the wire, order intact.
func securityProfileCatalog() []vibekit.SecurityProfile {
	src := policyfile.Profiles()
	out := make([]vibekit.SecurityProfile, 0, len(src))
	for i := range src {
		out = append(out, vibekit.SecurityProfile{ID: src[i].ID, Presets: src[i].Presets})
	}
	return out
}

// activeProfile reads the profile in force, falling back the same way the session
// door does. One rule, two readers: a picker showing a different profile from the
// one the sessions actually opened with would be the read-back lie this panel has
// already been through once.
func (s *Server) activeProfile(ctx context.Context) string {
	var id string
	if !settings.FieldInto(ctx, s.configDir, settings.KeySecurityProfile, &id) || id == "" {
		return policyfile.DefaultProfile
	}
	if _, ok := policyfile.ProfileFor(id); !ok {
		return policyfile.DefaultProfile
	}
	return id
}
