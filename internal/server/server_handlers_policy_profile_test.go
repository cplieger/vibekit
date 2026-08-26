package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/vibekit/internal/policyfile"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// fakeEngine is the SSE fan-out as this handler uses it: it records what was
// broadcast so a test can assert the client was told, which is the difference
// between a profile that changed and one that changed invisibly.
type fakeEngine struct{ events []vibekit.ServerEvent }

func (f *fakeEngine) RegisterRoutes(*http.ServeMux) {}
func (f *fakeEngine) Broadcast(_ context.Context, evt vibekit.ServerEvent) {
	f.events = append(f.events, evt)
}
func (f *fakeEngine) Shutdown(context.Context) error { return nil }

// fakeReload records whether the profile change asked for a session recycle.
type fakeReload struct{ restarts int }

func (f *fakeReload) RestartUtilitySession() { f.restarts++ }

// profileFixture stages a HOME and a workspace so the two writable policy paths
// resolve somewhere disposable, and returns the server plus both paths.
//
// t.Setenv rather than an injected home because policyfile.PathFor reads
// os.UserHomeDir, which is the same resolution KAS performs — faking it would test
// a path vibekit does not use. No t.Parallel in this file as a result.
func profileFixture(t *testing.T, live []vibekit.PolicyRule) (*Server, *fakeEngine, *fakeReload, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	eng := &fakeEngine{}
	reload := &fakeReload{}
	s := &Server{
		policy:       &fakePolicy{rules: live},
		policyReload: reload,
		agent:        eng,
		workDir:      work,
		configDir:    t.TempDir(),
	}
	userPath, err := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: work})
	if err != nil {
		t.Fatalf("user path: %v", err)
	}
	wsPath, err := policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
	if err != nil {
		t.Fatalf("workspace path: %v", err)
	}
	return s, eng, reload, userPath, wsPath
}

func postProfile(t *testing.T, s *Server, body profileBody) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/profile", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyProfile(rec, req)
	return rec
}

// seedRule is a bare session-scope rule as KAS reports one it resolved from a
// preset. The source prefix is what materialisation keys on.
func seedRule(capability, preset string) vibekit.PolicyRule {
	return vibekit.PolicyRule{
		Capability: capability, Effect: "allow",
		Scope: "session", Source: "preset:" + preset,
	}
}

func loadRules(t *testing.T, path string) []policyfile.Rule {
	t.Helper()
	f, err := policyfile.Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return f.Rules
}

// ruleCapabilities is the sorted capability projection of a rule set — the shape a
// profile expectation is written in, since the rules a profile writes are bare and
// so distinguished by capability alone. A pure projection, so it takes no *testing.T
// and cannot fail.
func ruleCapabilities(rules []policyfile.Rule) []string {
	out := make([]string, 0, len(rules))
	for i := range rules {
		out = append(out, rules[i].Capability)
	}
	slices.Sort(out)
	return out
}

// TestPolicyProfile_NamedSelectionRemovesProfileOwnedRules is the narrowing claim:
// selecting a restrictive rung takes the profile mechanism's OWN rules out of both
// writable files, so a grant the loosest rung wrote cannot outlive the switch away
// from it. The staged rule is a bare `all: allow`, which is byte-identical both to
// what that rung writes and to what the previous release's relaxation checkbox
// wrote at workspace scope — so the first selection is also its own migration, and
// the workspace file matters here as much as the user one.
//
// It claims nothing about HAND-AUTHORED rules, which survive a selection;
// TestPolicyProfile_KeepsHandAuthoredUserRules is that half. This case used to
// assert "a named profile must be the whole policy", the blanket overwrite the
// merge decision removed — a claim that would have passed equally under the
// behaviour this change replaced, and one a later reader could have cited to
// restore it.
func TestPolicyProfile_NamedSelectionRemovesProfileOwnedRules(t *testing.T) {
	s, eng, reload, userPath, wsPath := profileFixture(t, nil)
	for _, path := range []string{userPath, wsPath} {
		if err := policyfile.Save(t.Context(), path, &policyfile.File{Rules: []policyfile.Rule{
			{Capability: "all", Effect: "allow"},
		}}); err != nil {
			t.Fatalf("stage %s: %v", path, err)
		}
	}

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileTrusted}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	for _, path := range []string{userPath, wsPath} {
		if got := loadRules(t, path); len(got) != 0 {
			t.Errorf("%s still holds %v after selecting %q; a selection must remove the rules the "+
				"profile mechanism itself writes", path, got, policyfile.ProfileTrusted)
		}
	}
	var persisted string
	if !settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &persisted) {
		t.Fatal("profile was not persisted")
	}
	if persisted != policyfile.ProfileTrusted {
		t.Errorf("persisted %q, want %q", persisted, policyfile.ProfileTrusted)
	}
	// The presets ride the session door, so without the recycle the policy view
	// keeps describing the profile that was in force before the change.
	if reload.restarts != 1 {
		t.Errorf("utility restarts = %d, want 1", reload.restarts)
	}
	var sawPermissions bool
	for _, e := range eng.events {
		if e.Type == vibekit.EventPermissionsChanged {
			sawPermissions = true
		}
	}
	if !sawPermissions {
		t.Error("no permissions_changed broadcast; other devices would keep showing the old profile")
	}
}

// TestPolicyProfile_SeedMaterialisesTheProfileInForce is the Customize button. It
// has to copy the rules in BEFORE clearing, or the rules it copies are the ones it
// is about to delete, and it must take them from the live view because no RPC
// enumerates a preset.
func TestPolicyProfile_SeedMaterialisesTheProfileInForce(t *testing.T) {
	live := []vibekit.PolicyRule{
		seedRule("fs_read", "read-workspace"),
		seedRule("shell", "dev-shell"),
		// Neither of these is the profile's: one is a consent granted for this
		// session, the other a baseline scope. Materialising either would put a rule
		// in the user's file that they never chose and cannot trace.
		{Capability: "mcp", Effect: "allow", Scope: "session", Source: "consent"},
		{Capability: "fs_write", Effect: "ask", Scope: "kiro", Source: "kiro-scope"},
	}
	s, _, _, userPath, wsPath := profileFixture(t, live)

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom, Seed: true}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	got := loadRules(t, userPath)
	caps := make([]string, 0, len(got))
	for _, r := range got {
		caps = append(caps, r.Capability)
	}
	slices.Sort(caps)
	if !slices.Equal(caps, []string{"fs_read", "shell"}) {
		t.Errorf("materialised %v, want only the preset-sourced rules (fs_read, shell)", caps)
	}
	if r := loadRules(t, wsPath); len(r) != 0 {
		t.Errorf("workspace file holds %v; the user file is the profile's single home", r)
	}
}

// bareAllows is the rule shape a profile writes: one bare allow per capability, no
// match and no exclude. A pure projection, so it takes no *testing.T and cannot
// fail.
func bareAllows(caps []string) []policyfile.Rule {
	out := make([]policyfile.Rule, 0, len(caps))
	for _, c := range caps {
		out = append(out, policyfile.Rule{Capability: c, Effect: policyfile.EffectAllow})
	}
	return out
}

// TestPolicyProfile_SeedKeepsTheOutgoingRungsFileRules is the half of "what is in
// force" that a session-scope read cannot see.
//
// On the loosest rung the posture is its presets AND the bare allow rules it wrote
// to the user file. presetRulesInForce filters to SESSION scope, so the file half
// has to come from the profile definition — and the removal pass inside
// SetProfileRules deletes it unconditionally, keying on the union over the ladder.
// Without the union the only rule coming back is the one KAS's allow-all preset
// resolves to at session scope, `all: allow`, so `sandbox_network` — the one
// capability `all` does not cover, and the whole reason RelaxCapabilities has two
// members — was silently dropped and Customize handed back a posture narrower than
// the rung it claimed to have materialised.
//
// The fixture reports ONLY `all` live, which is the real distribution: that is what
// the preset resolves to, and sandbox_network exists nowhere but the user file.
func TestPolicyProfile_SeedKeepsTheOutgoingRungsFileRules(t *testing.T) {
	s, _, _, userPath, _ := profileFixture(t, []vibekit.PolicyRule{seedRule("all", "allow-all")})
	if err := s.persistProfile(t.Context(), policyfile.ProfileUnrestricted); err != nil {
		t.Fatalf("Setup: put the loosest rung in force: %v", err)
	}
	if err := policyfile.Save(t.Context(), userPath,
		&policyfile.File{Rules: bareAllows(policyfile.RelaxCapabilities())}); err != nil {
		t.Fatalf("Setup: stage the loosest rung's own file rules: %v", err)
	}

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom, Seed: true}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	got := loadRules(t, userPath)
	want := policyfile.RelaxCapabilities()
	if caps := ruleCapabilities(got); !slices.Equal(caps, want) {
		t.Errorf("Customize away from %q left %v in the user file, want the whole outgoing posture %v",
			policyfile.ProfileUnrestricted, caps, want)
	}
	for _, r := range got {
		// A recovered rule that is not a bare allow grants less than the rung did and
		// is not removable by the Signature the next selection looks for.
		if r.Effect != policyfile.EffectAllow || r.Match != nil || r.Exclude != nil {
			t.Errorf("materialised rule %+v is not a bare allow; Customize must copy the posture as it stood", r)
		}
	}
}

// TestPolicyProfile_CustomWithoutSeedKeepsHandAuthoredRules is the second door,
// and the seed flag is still the whole difference between the two: Customize copies
// the outgoing profile's rules in, direct selection adds nothing.
//
// DELIBERATE CONTRACT CHANGE, not a weakened test. This case used to assert the
// file was EMPTIED, and wiping a file the user hand-edited is the silent
// destruction the merge decision forbids — a selection now removes only the rules
// the profile mechanism itself could have written. What the original reason was
// actually protecting is that the two doors differ, and the seed flag still carries
// that: the Customize case (_SeedMaterialisesTheProfileInForce) copies the live
// preset rules in and this one does not.
func TestPolicyProfile_CustomWithoutSeedKeepsHandAuthoredRules(t *testing.T) {
	s, _, _, userPath, _ := profileFixture(t, []vibekit.PolicyRule{seedRule("fs_read", "read-workspace")})
	if err := policyfile.Save(t.Context(), userPath, &policyfile.File{Rules: []policyfile.Rule{
		{Capability: "shell", Effect: "allow"},
	}}); err != nil {
		t.Fatalf("stage: %v", err)
	}

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got := loadRules(t, userPath)
	if len(got) != 1 || got[0].Capability != "shell" {
		t.Errorf("custom without seed left %v, want only the hand-authored shell rule", got)
	}
	// The live fs_read preset rule is what the seeding door would have copied in, so
	// its absence is what still separates the two doors.
	if slices.ContainsFunc(got, func(r policyfile.Rule) bool { return r.Capability == "fs_read" }) {
		t.Errorf("custom without seed materialised the live profile's rules (%v); that is the Customize door", got)
	}
}

// TestPolicyProfile_LoosestRungWritesUserScopeRules is the fix itself, asserted at
// the endpoint.
//
// A session-scope preset never reaches a session KAS creates itself, which every
// workflow step's session is, so the loosest rung has to put its posture where KAS
// reads it for every session in the process: the USER file. The workspace file
// stays empty — it would be a second writer of one posture and buys no precision
// when one instance is one HOME and one workspace root.
func TestPolicyProfile_LoosestRungWritesUserScopeRules(t *testing.T) {
	s, _, reload, userPath, wsPath := profileFixture(t, nil)

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileUnrestricted}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	got := loadRules(t, userPath)
	for _, r := range got {
		// Bare, or Signature removal cannot reverse the selection: the rule the next
		// profile change looks for would not be the rule this one wrote.
		if r.Effect != policyfile.EffectAllow || r.Match != nil || r.Exclude != nil {
			t.Errorf("user rule %+v is not a bare allow; the selection would not be reversible", r)
		}
	}
	want := policyfile.RelaxCapabilities()
	if caps := ruleCapabilities(got); !slices.Equal(caps, want) {
		t.Errorf("selecting %q wrote %v to the user file, want RelaxCapabilities() = %v",
			policyfile.ProfileUnrestricted, caps, want)
	}
	if r := loadRules(t, wsPath); len(r) != 0 {
		t.Errorf("workspace file holds %v; the user file is the profile's single home", r)
	}
	if reload.restarts != 1 {
		t.Errorf("utility restarts = %d, want 1", reload.restarts)
	}
}

// TestPolicyProfile_RestrictiveRungsWriteNoAllowRule is the guard on the other side
// of the design decision. A user-scope rule is durable — it survives a restart and
// applies to every ACP client sharing this HOME, with no session boundary to expire
// it — so writing one for a restrictive rung would WIDEN that posture rather than
// deliver it.
func TestPolicyProfile_RestrictiveRungsWriteNoAllowRule(t *testing.T) {
	for _, id := range []string{
		policyfile.ProfileGuarded, policyfile.ProfileReadOnly, policyfile.ProfileTrusted,
	} {
		t.Run(id, func(t *testing.T) {
			s, _, _, userPath, wsPath := profileFixture(t, nil)
			if rec := postProfile(t, s, profileBody{Profile: id}); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
			}
			for _, path := range []string{userPath, wsPath} {
				if got := loadRules(t, path); len(got) != 0 {
					t.Errorf("selecting %q left %v in %s; a restrictive rung writes no durable rule", id, got, path)
				}
			}
		})
	}
}

// TestPolicyProfile_KeepsHandAuthoredUserRules is the merge decision end to end,
// staged as the live container's own file: a hand-written `shell: allow`, added as
// the workaround for the defect this change fixes.
//
// A full guarded -> unrestricted -> guarded round trip, because the decision has
// two halves and each has its own way to fail. The blanket overwrite this replaced
// destroyed the rule on the first click; a removal set keyed on the OUTGOING rung
// would leave the loosest rung's `all: allow` standing on the way back down, and a
// narrowing that does not narrow is worse than the bug being fixed.
//
// Deliberately NOT subtests: each step reads the file the one before it wrote, so
// "runnable alone" is not the property here, and the step index is in every message
// instead.
func TestPolicyProfile_KeepsHandAuthoredUserRules(t *testing.T) {
	s, _, _, userPath, _ := profileFixture(t, nil)
	handAuthored := policyfile.Rule{Capability: "shell", Effect: policyfile.EffectAllow}
	if err := policyfile.Save(t.Context(), userPath,
		&policyfile.File{Rules: []policyfile.Rule{handAuthored}}); err != nil {
		t.Fatalf("Setup: stage the hand-authored rule: %v", err)
	}

	for i, step := range []struct {
		profile string
		want    []string
	}{
		{policyfile.ProfileGuarded, []string{"shell"}},
		{policyfile.ProfileUnrestricted, append([]string{"shell"}, policyfile.RelaxCapabilities()...)},
		{policyfile.ProfileGuarded, []string{"shell"}},
	} {
		if rec := postProfile(t, s, profileBody{Profile: step.profile}); rec.Code != http.StatusOK {
			t.Fatalf("step %d selecting %q: status = %d, body %s", i, step.profile, rec.Code, rec.Body)
		}
		want := slices.Clone(step.want)
		slices.Sort(want)
		if caps := ruleCapabilities(loadRules(t, userPath)); !slices.Equal(caps, want) {
			t.Errorf("step %d, after selecting %q the user file holds %v, want %v",
				i, step.profile, caps, want)
		}
	}
}

// TestPolicyProfile_PersistFailureRestoresTheFiles is the atomicity requirement:
// three files, no cross-file atomicity to be had, so a failure after the policy
// files were written must not leave them granting one posture while config.json
// names another.
//
// The injection is fixture-only and adds no production seam — configDir points
// inside a REGULAR FILE, so persistProfile's mkdir cannot succeed. It is also
// uid-independent, unlike a 0500 directory, which root can still write.
func TestPolicyProfile_PersistFailureRestoresTheFiles(t *testing.T) {
	s, _, reload, userPath, wsPath := profileFixture(t, nil)
	staged := []policyfile.Rule{{Capability: "shell", Effect: policyfile.EffectAllow}}
	for _, path := range []string{userPath, wsPath} {
		if err := policyfile.Save(t.Context(), path, &policyfile.File{Rules: staged}); err != nil {
			t.Fatalf("Setup: stage %s: %v", path, err)
		}
	}
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("Setup: stage the blocker: %v", err)
	}
	s.configDir = filepath.Join(blocker, "config")

	rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileUnrestricted})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body %s", rec.Code, rec.Body)
	}
	for _, path := range []string{userPath, wsPath} {
		got := loadRules(t, path)
		if len(got) != 1 || got[0].Capability != "shell" {
			t.Errorf("%s holds %v after a failed selection, want the staged shell rule back", path, got)
		}
	}
	if reload.restarts != 0 {
		t.Errorf("utility restarts = %d; a selection that failed must not recycle a session", reload.restarts)
	}
}

// TestPolicyProfile_WorkspaceWriteFailureRestoresTheUserFile is the failure the
// compensating restore exists for, and the only one where it prevents a durable
// OVER-GRANT: the user file has already taken the loosest rung's bare allow rules
// by the time the workspace write fails, so without the restore that grant stays on
// disk — surviving a restart and reaching every ACP client on this HOME — while
// config.json still names the old profile.
//
// The injection is a DANGLING SYMLINK where the workspace-roots hash directory
// would go. Fixture-only, no production seam, and asymmetric in exactly the way
// this path needs: Load resolves it as a path COMPONENT and gets ENOENT, which is
// the ordinary "no rules yet" answer, so the snapshot succeeds and the request
// reaches the write; Save's directory creation then finds the name taken by a
// non-directory and fails. It is also uid-independent, unlike a mode on that
// directory, which root ignores.
//
// The workspace RESTORE fails for the same reason the write did, so this covers the
// restore-failure answer as well: the specific 500 naming both files, and the
// broadcast that makes the Active-policy table refetch instead of waiting for KAS's
// own reload notification to expose the divergence.
func TestPolicyProfile_WorkspaceWriteFailureRestoresTheUserFile(t *testing.T) {
	s, eng, reload, userPath, wsPath := profileFixture(t, nil)
	staged := []policyfile.Rule{{Capability: "shell", Effect: policyfile.EffectAllow}}
	if err := policyfile.Save(t.Context(), userPath, &policyfile.File{Rules: staged}); err != nil {
		t.Fatalf("Setup: stage the user file: %v", err)
	}
	hashDir := filepath.Dir(wsPath)
	if err := os.MkdirAll(filepath.Dir(hashDir), 0o700); err != nil {
		t.Fatalf("Setup: stage workspace-roots: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "no-such-target"), hashDir); err != nil {
		t.Fatalf("Setup: stage the dangling symlink: %v", err)
	}

	rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileUnrestricted})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body %s", rec.Code, rec.Body)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("could not be put back")) {
		t.Errorf("body = %s, want the restore-failure message naming both files", rec.Body)
	}
	if got := ruleCapabilities(loadRules(t, userPath)); !slices.Equal(got, []string{"shell"}) {
		t.Errorf("the user file holds %v after a failed workspace write, want the staged shell rule back; "+
			"the loosest rung's grant would otherwise outlive the selection that failed", got)
	}
	var persisted string
	if settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &persisted) && persisted != "" {
		t.Errorf("persisted %q for a selection that failed", persisted)
	}
	if reload.restarts != 0 {
		t.Errorf("utility restarts = %d; a selection that failed must not recycle a session", reload.restarts)
	}
	var sawPermissions bool
	for _, e := range eng.events {
		if e.Type == vibekit.EventPermissionsChanged {
			sawPermissions = true
		}
	}
	if !sawPermissions {
		t.Error("no permissions_changed broadcast on the restore-failure path; the Active-policy table " +
			"would keep describing a posture the file no longer holds until KAS's own reload arrived")
	}
}

// TestPolicyProfile_AFullUserFileIsTheCallersProblem pins the STATUS, because the
// status is the whole content of this fix: a user file at the rule cap is a
// condition the user created in their own file and can fix from the table, and the
// sibling rule endpoint has always answered 400 for it. Answered as a 500 it read
// as vibekit being broken, for every profile selection, until they found the log.
//
// It also pins that this refusal writes NOTHING, which needs two observables
// because the restore it must not run would have produced byte-identical rules. The
// staged file carries a YAML comment, which Load parses through and Save cannot
// reproduce, so a re-marshal is visible in the bytes; and the workspace file is
// absent, where a restore writes `rules: []` to both scopes and so brings it into
// existence. Together they separate "the cap was refused before any write" from
// "the cap was refused and then both files were rewritten with what they already
// held" — the second bumps mtime, wakes KAS's watcher and produces a policy-changed
// notification for a request that changed nothing.
func TestPolicyProfile_AFullUserFileIsTheCallersProblem(t *testing.T) {
	s, _, reload, userPath, wsPath := profileFixture(t, nil)
	// Hand-authored, so none of them is profile-owned and the removal pass keeps
	// every one: the file is still full when the incoming rules are upserted.
	full := make([]policyfile.Rule, 0, 512)
	for i := range 512 {
		full = append(full, policyfile.Rule{
			Capability: "cap-" + strconv.Itoa(i), Effect: policyfile.EffectAsk,
		})
	}
	if err := policyfile.Save(t.Context(), userPath, &policyfile.File{Rules: full}); err != nil {
		t.Fatalf("Setup: stage a full user file: %v", err)
	}
	marshalled, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("Setup: read the staged file: %v", err)
	}
	staged := append([]byte("# hand-edited; a comment Save cannot reproduce\n"), marshalled...)
	if err := os.WriteFile(userPath, staged, 0o600); err != nil {
		t.Fatalf("Setup: stage the commented file: %v", err)
	}

	rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileUnrestricted})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a file at the rule cap, body %s", rec.Code, rec.Body)
	}
	if got := loadRules(t, userPath); len(got) != len(full) {
		t.Errorf("the user file holds %d rules after the refusal, want the staged %d back",
			len(got), len(full))
	}
	back, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("read back %s: %v", userPath, err)
	}
	if !bytes.Equal(back, staged) {
		t.Errorf("the user file was rewritten after a refusal that wrote nothing; want it left "+
			"byte-for-byte alone, got %d bytes and lost the comment: %t",
			len(back), !bytes.Contains(back, []byte("# hand-edited")))
	}
	if _, err := os.Stat(wsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("os.Stat(%s) = %v, want the file still absent; a refusal that wrote nothing must "+
			"not create a workspace policy file on the way out", wsPath, err)
	}
	if reload.restarts != 0 {
		t.Errorf("utility restarts = %d; a refused selection must not recycle a session", reload.restarts)
	}
}

// TestPolicyProfile_RefusesAnUnparseableUserFile: the overwrite this replaced never
// read these files, so a hand-edit vibekit could not parse was destroyed silently.
// A selection now refuses with the same 409 policyRuleAdd answers, and the bytes
// stay on disk for the user to fix.
func TestPolicyProfile_RefusesAnUnparseableUserFile(t *testing.T) {
	s, _, reload, userPath, _ := profileFixture(t, nil)
	malformed := []byte("rules: [ this is not a rule list\n")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatalf("Setup: stage the directory: %v", err)
	}
	if err := os.WriteFile(userPath, malformed, 0o600); err != nil {
		t.Fatalf("Setup: stage the malformed file: %v", err)
	}

	rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileUnrestricted})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body %s", rec.Code, rec.Body)
	}
	back, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("read back %s: %v", userPath, err)
	}
	if !bytes.Equal(back, malformed) {
		t.Errorf("the unparseable file was rewritten as %q, want it left byte-for-byte alone", back)
	}
	var persisted string
	if settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &persisted) && persisted != "" {
		t.Errorf("persisted %q on a refused selection", persisted)
	}
	if reload.restarts != 0 {
		t.Errorf("utility restarts = %d; a refused selection recycled a session", reload.restarts)
	}
}

// TestPolicyProfile_SeedFailsClosed: a Customize that cannot read the profile must
// leave everything alone. Switching anyway would land on an empty Custom, which
// drops every grant the user had — the loudest possible failure dressed as a
// successful click.
func TestPolicyProfile_SeedFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		live []vibekit.PolicyRule
	}{
		// Nothing preset-sourced is indistinguishable from a session that has not
		// started yet, and the two want opposite outcomes.
		{"no preset rules in force", []vibekit.PolicyRule{
			{Capability: "fs_write", Effect: "ask", Scope: "kiro", Source: "kiro-scope"},
		}},
		{"no live policy at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, reload, userPath, _ := profileFixture(t, tc.live)
			staged := []policyfile.Rule{{Capability: "shell", Effect: "allow"}}
			if err := policyfile.Save(t.Context(), userPath, &policyfile.File{Rules: staged}); err != nil {
				t.Fatalf("stage: %v", err)
			}

			rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileCustom, Seed: true})
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rec.Code)
			}
			if got := loadRules(t, userPath); len(got) != 1 || got[0].Capability != "shell" {
				t.Errorf("the staged policy was disturbed: %v", got)
			}
			var persisted string
			if settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &persisted) && persisted != "" {
				t.Errorf("persisted %q on a refused switch", persisted)
			}
			if reload.restarts != 0 {
				t.Errorf("recycled a session for a switch that did not happen")
			}
		})
	}
}

// TestPolicyProfile_Refusals covers the two shapes a caller can get wrong, and both
// are 400 rather than a tolerated no-op: an unknown id would otherwise persist a
// profile no session can resolve, and seed on a named profile means the caller has
// the materialisation running the wrong way.
func TestPolicyProfile_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		body profileBody
	}{
		{"unknown profile", profileBody{Profile: "yolo"}},
		{"empty profile", profileBody{}},
		{"seed on a named profile", profileBody{Profile: policyfile.ProfileTrusted, Seed: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, reload, userPath, _ := profileFixture(t, []vibekit.PolicyRule{seedRule("fs_read", "read-workspace")})
			if rec := postProfile(t, s, tc.body); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if _, err := os.Stat(userPath); err == nil {
				t.Error("a refused request wrote a policy file")
			}
			if reload.restarts != 0 {
				t.Error("a refused request recycled a session")
			}
		})
	}
}

// TestPolicyProfile_PersistKeepsSiblingSettings: the profile write merges. Replacing
// config.json would drop every other preference, and a settings file that silently
// reverts is the failure the atomic write exists to prevent.
func TestPolicyProfile_PersistKeepsSiblingSettings(t *testing.T) {
	s, _, _, _, _ := profileFixture(t, nil)
	path := filepath.Join(s.configDir, settings.Filename)
	if err := os.WriteFile(path, []byte(`{"last_model":"m-keep","supervised_default":true}`), 0o600); err != nil {
		t.Fatalf("stage settings: %v", err)
	}

	if rec := postProfile(t, s, profileBody{Profile: policyfile.ProfileReadOnly}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var model string
	if !settings.FieldInto(t.Context(), s.configDir, settings.KeyLastModel, &model) || model != "m-keep" {
		t.Errorf("last_model = %q, want it carried over", model)
	}
	var profile string
	settings.FieldInto(t.Context(), s.configDir, settings.KeySecurityProfile, &profile)
	if profile != policyfile.ProfileReadOnly {
		t.Errorf("profile = %q, want %q", profile, policyfile.ProfileReadOnly)
	}
}

// TestPolicyView_CarriesTheLadderAndTheActiveProfile: the picker renders from this,
// so a stale or reordered ladder here is a picker that offers the wrong postures.
func TestPolicyView_CarriesTheLadderAndTheActiveProfile(t *testing.T) {
	s, _, _, _, _ := profileFixture(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/permissions", nil)
	rec := httptest.NewRecorder()
	s.handlePolicyView(rec, req)

	var view vibekit.PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := policyfile.Profiles()
	if len(view.Profiles) != len(want) {
		t.Fatalf("profiles = %d, want %d", len(view.Profiles), len(want))
	}
	for i := range want {
		if view.Profiles[i].ID != want[i].ID {
			t.Errorf("profile %d = %q, want %q; the ladder's order is part of the payload", i, view.Profiles[i].ID, want[i].ID)
		}
		if !slices.Equal(view.Profiles[i].Presets, want[i].Presets) {
			t.Errorf("profile %q presets = %v, want %v", want[i].ID, view.Profiles[i].Presets, want[i].Presets)
		}
	}
	// Unset resolves to the default rather than to empty, or the picker would open
	// on no selection while the sessions run at the default.
	if view.Profile != policyfile.DefaultProfile {
		t.Errorf("active profile = %q, want the default %q", view.Profile, policyfile.DefaultProfile)
	}
}
