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
// The profile is the one control that owns a rule SET rather than editing one
// rule, and it owns it by REPLACEMENT: selecting a profile clears both writable
// permissions files and lets the profile's KAS presets be the policy. That is why
// it is its own endpoint rather than a key on PATCH /api/settings — persisting the
// setting is the smallest part of what a selection does, and a settings PATCH that
// silently deleted policy files would be a side effect nobody reading the route
// could predict.
//
// Two doors into Custom, and Seed is the difference. The Customize button sends
// seed=true, which MATERIALISES what is currently in force into the user file so
// the editable table opens on the outgoing profile's rules as a starting point.
// Selecting Custom from the list sends seed=false, which starts blank on purpose.

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
	if err := s.replacePolicyFiles(r.Context(), roots, seeded); err != nil {
		httpreply.InternalError(w, err)
		return
	}
	if err := s.persistProfile(r.Context(), profile.ID); err != nil {
		httpreply.InternalError(w, err)
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
		"presets", profile.Presets, "seeded_rules", len(seeded))
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

// replacePolicyFiles makes rules the ENTIRE content of the user file and empties
// the workspace one.
//
// Both files, because a profile selection means what it says: a workspace rule
// left standing would widen or narrow the profile with nothing on screen saying
// so. Only the user file receives content, because that is the scope whose reach
// matches the profile setting's own — one vibekit instance is one HOME, one user
// and one workspace root, so the workspace file buys no precision and having two
// writers of one posture is what makes a UI disagree with its own policy.
func (s *Server) replacePolicyFiles(ctx context.Context, roots policyfile.Roots, rules []policyfile.Rule) error {
	for _, scope := range []string{policyfile.ScopeUser, policyfile.ScopeWorkspace} {
		path, err := policyfile.PathFor(scope, roots)
		if err != nil {
			return err
		}
		f := &policyfile.File{Rules: []policyfile.Rule{}}
		if scope == policyfile.ScopeUser {
			f.Rules = rules
		}
		if err := policyfile.Save(ctx, path, f); err != nil {
			return err
		}
	}
	return nil
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
