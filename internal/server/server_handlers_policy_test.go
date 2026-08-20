package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/policyfile"
	"github.com/cplieger/vibekit/internal/vibekit"
)

type fakePolicy struct {
	rules       []vibekit.PolicyRule
	listErr     error
	explain     *vibekit.PolicyExplainResult
	explainErr  error
	explainReqs []vibekit.PolicyExplainRequest
}

func (f *fakePolicy) PolicyList(_ context.Context, _ string) ([]vibekit.PolicyRule, error) {
	return f.rules, f.listErr
}

func (f *fakePolicy) PolicyExplain(_ context.Context, req vibekit.PolicyExplainRequest) (*vibekit.PolicyExplainResult, error) {
	f.explainReqs = append(f.explainReqs, req)
	return f.explain, f.explainErr
}

func postRules(t *testing.T, s *Server, body policyRuleBody) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/rules", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyRules(rec, req)
	return rec
}

func TestPolicyViewLive(t *testing.T) {
	f := &fakePolicy{rules: []vibekit.PolicyRule{
		{Capability: "fs_write", Effect: "allow", Scope: "user", Source: "/x/permissions.yaml"},
	}}
	s := &Server{policy: f, workDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodGet, "/api/permissions", http.NoBody)
	rec := httptest.NewRecorder()
	s.handlePolicyView(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got vibekit.PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || len(got.Rules) != 1 || got.Rules[0].Capability != "fs_write" {
		t.Errorf("view = %+v", got)
	}
	if len(got.WritableScopes) != 2 || len(got.Capabilities) == 0 {
		t.Errorf("view metadata = %+v", got)
	}
}

func TestPolicyViewFileFallback(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	// Seed a user file so the fallback has something to read.
	up, _ := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: work})
	if err := policyfile.Save(t.Context(), up, &policyfile.File{
		Rules: []policyfile.Rule{{Capability: "shell", Effect: "deny", Match: []string{"sudo *"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// Erroring provider → fallback to file read.
	s := &Server{policy: &fakePolicy{listErr: context.DeadlineExceeded}, workDir: work}
	req := httptest.NewRequest(http.MethodGet, "/api/permissions", http.NoBody)
	rec := httptest.NewRecorder()
	s.handlePolicyView(rec, req)
	var got vibekit.PolicyView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Available {
		t.Error("Available should be false on fallback")
	}
	if len(got.Rules) != 1 || got.Rules[0].Capability != "shell" || got.Rules[0].Scope != "user" {
		t.Errorf("fallback rules = %+v", got.Rules)
	}
}

func TestPolicyRuleAddDefaultsToAsk(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{workDir: work}
	// No effect provided → must default to ask (conservative), never allow.
	rec := postRules(t, s, policyRuleBody{Op: "add", Scope: "workspace", Capability: "fs_write", Match: []string{"src/**"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	wp, _ := policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
	f, err := policyfile.Load(wp)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Effect != "ask" {
		t.Errorf("written rule = %+v, want effect=ask", f.Rules)
	}
}

func TestPolicyRuleAddExplicitAllow(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{workDir: work}
	rec := postRules(t, s, policyRuleBody{Op: "add", Scope: "user", Capability: "web_fetch", Effect: "allow"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	up, _ := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: work})
	f, _ := policyfile.Load(up)
	if len(f.Rules) != 1 || f.Rules[0].Effect != "allow" || f.Rules[0].Capability != "web_fetch" {
		t.Errorf("written rule = %+v", f.Rules)
	}
}

func TestPolicyRuleInvalidScopeRejected(t *testing.T) {
	s := &Server{workDir: t.TempDir()}
	rec := postRules(t, s, policyRuleBody{Op: "add", Scope: "agent", Capability: "shell", Effect: "ask"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (agent scope not writable)", rec.Code)
	}
}

// TestPolicyRuleUnrecognisedCapabilityRoundTrips is the T67 inversion at the
// HTTP edge: this used to be a 400. The rule is written verbatim and KAS's loader
// is the authority — it validates on load and SKIPS an unrecognised rule as
// non-fatal, reporting it on _kiro/policy/changed's errors array (translated to
// the permissions_changed SSE and rendered from payload.errors), NOT on
// _kiro/policy/error, which KAS emits only for fatal errors. The 400 meant
// vibekit refused to write the rule a newly-added capability exists for.
func TestPolicyRuleUnrecognisedCapabilityRoundTrips(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{workDir: work}

	// "hooks" is the real case: an upstream security report asked for it.
	rec := postRules(t, s, policyRuleBody{
		Op: "add", Scope: "user", Capability: "hooks", Effect: "deny",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (KAS is the authority on the vocabulary)", rec.Code)
	}
	up, _ := policyfile.PathFor(policyfile.ScopeUser, policyfile.Roots{Home: home, WorkDir: work})
	f, err := policyfile.Load(up)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Capability != "hooks" || f.Rules[0].Effect != "deny" {
		t.Errorf("written rule = %+v, want the unrecognised capability persisted verbatim", f.Rules)
	}
}

// TestPolicyRuleMalformedCapabilityRejected: dropping the vocabulary check did
// not drop the shape check. An empty or control-character capability is not
// something KAS could reject usefully — it is a malformed request, and writing it
// leaves a rule the user has to hand-edit out of a security policy file.
func TestPolicyRuleMalformedCapabilityRejected(t *testing.T) {
	for name, capability := range map[string]string{
		"empty":             "",
		"control character": "fs_\x00write",
		"newline":           "fs_write\nshell",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			s := &Server{workDir: t.TempDir()}
			rec := postRules(t, s, policyRuleBody{
				Op: "add", Scope: "user", Capability: capability, Effect: "ask",
			})
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (malformed, not merely unrecognised)", rec.Code)
			}
		})
	}
}

// TestPickerCapabilities_UnionsInWhatTheRulesUse: the suggested set is a
// hand-copied snapshot of a list KAS does not expose, so the rules KAS reports
// are the only channel through which the picker can learn a new capability. A
// capability in use anywhere — including the read-only kiro/administration/agent
// baselines — becomes selectable with no vibekit release.
func TestPickerCapabilities_UnionsInWhatTheRulesUse(t *testing.T) {
	base := policyfile.Capabilities()
	if slices.Contains(base, "hooks") {
		t.Fatal("test assumes 'hooks' is absent from the suggested set")
	}

	got := pickerCapabilities([]vibekit.PolicyRule{
		{Capability: "hooks", Effect: "deny", Scope: "kiro"},
		// Already suggested: must not be duplicated.
		{Capability: "shell", Effect: "ask", Scope: "user"},
		// Repeat of the new one: must not be duplicated either.
		{Capability: "hooks", Effect: "ask", Scope: "user"},
		// An empty capability is not an offer.
		{Capability: "", Effect: "ask", Scope: "user"},
	})

	if !slices.Contains(got, "hooks") {
		t.Errorf("picker = %v, want it to offer 'hooks'", got)
	}
	if len(got) != len(base)+1 {
		t.Errorf("picker has %d entries, want %d (one new capability, no duplicates)", len(got), len(base)+1)
	}
	if !slices.IsSorted(got) {
		t.Errorf("picker not sorted: %v", got)
	}
	for _, c := range base {
		if !slices.Contains(got, c) {
			t.Errorf("picker dropped the suggested capability %q", c)
		}
	}
}

// TestPickerCapabilities_NoRulesIsTheSuggestedSet: a fresh install with no rules
// must still populate the dropdown.
//
// The non-EMPTY leg is the wire assertion, and slices.Equal cannot make it:
// slices.Equal(nil, []string{}) is true, so comparing against the suggested set
// would pass if both were empty, and an empty picker marshals `"capabilities":
// null` rather than `[]` (the field carries no omitzero/omitempty). Since
// pickerCapabilities now returns slices.Sorted(maps.Keys(…)), whose answer for an
// empty set IS nil, the invariant "the snapshot always seeds it" is what keeps
// the wire shape stable — so it is asserted here rather than assumed.
func TestPickerCapabilities_NoRulesIsTheSuggestedSet(t *testing.T) {
	got := pickerCapabilities(nil)
	if len(got) == 0 {
		t.Fatal("picker is empty; the capabilities wire field would marshal as null, not []")
	}
	if !slices.Equal(got, policyfile.Capabilities()) {
		t.Errorf("picker = %v, want the suggested set", got)
	}
	if !slices.IsSorted(got) {
		t.Errorf("picker = %v, want one alphabetical list", got)
	}
}

func TestPolicyRuleRemoveDenyRequiresConfirm(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	// Seed a deny rule.
	wp, _ := policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
	if err := policyfile.Save(t.Context(), wp, &policyfile.File{
		Rules: []policyfile.Rule{{Capability: "shell", Effect: "deny", Match: []string{"rm -rf *"}}},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{workDir: work}

	// Without confirm → 409, rule stays.
	rec := postRules(t, s, policyRuleBody{Op: "remove", Scope: "workspace", Capability: "shell", Effect: "deny", Match: []string{"rm -rf *"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (deny removal needs confirm)", rec.Code)
	}
	if f, _ := policyfile.Load(wp); len(f.Rules) != 1 {
		t.Errorf("deny rule removed without confirm; rules=%d", len(f.Rules))
	}

	// With confirm → 200, rule gone.
	rec = postRules(t, s, policyRuleBody{Op: "remove", Scope: "workspace", Capability: "shell", Effect: "deny", Match: []string{"rm -rf *"}, Confirm: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed remove status = %d, want 200", rec.Code)
	}
	if f, _ := policyfile.Load(wp); len(f.Rules) != 0 {
		t.Errorf("deny rule not removed with confirm; rules=%d", len(f.Rules))
	}
}

func TestPolicyRuleUnknownOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{workDir: t.TempDir()}
	rec := postRules(t, s, policyRuleBody{Op: "frobnicate", Scope: "user", Capability: "shell", Effect: "ask"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown op)", rec.Code)
	}
}

func TestPolicyExplainProxies(t *testing.T) {
	f := &fakePolicy{explain: &vibekit.PolicyExplainResult{Capability: "fs_write", Effect: "ask"}}
	s := &Server{policy: f}
	b, _ := json.Marshal(vibekit.PolicyExplainRequest{Capability: "fs_write", Resource: "/etc/hosts"})
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/explain", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyExplain(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got vibekit.PolicyExplainResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Effect != "ask" {
		t.Errorf("explain effect = %q", got.Effect)
	}
	if len(f.explainReqs) != 1 || f.explainReqs[0].Resource != "/etc/hosts" {
		t.Errorf("explain reqs = %+v", f.explainReqs)
	}
}

func TestPolicyExplainRequiresTarget(t *testing.T) {
	s := &Server{policy: &fakePolicy{}}
	b, _ := json.Marshal(vibekit.PolicyExplainRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/explain", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyExplain(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (no capability/tool_id)", rec.Code)
	}
}

// --- op=update: in-place effect editing ---

func TestPolicyRuleUpdate(t *testing.T) {
	seed := func(t *testing.T) (s *Server, wp string) {
		t.Helper()
		home := t.TempDir()
		work := t.TempDir()
		t.Setenv("HOME", home)
		wp, _ = policyfile.PathFor(policyfile.ScopeWorkspace, policyfile.Roots{Home: home, WorkDir: work})
		if err := policyfile.Save(t.Context(), wp, &policyfile.File{
			Rules: []policyfile.Rule{
				{Capability: "shell", Effect: "ask", Match: []string{"rm *"}},
				{Capability: "fs_read", Effect: "allow", Match: []string{"src/**"}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		return &Server{workDir: work}, wp
	}

	t.Run("narrowing_update_needs_no_confirm", func(t *testing.T) {
		s, wp := seed(t)
		rec := postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "deny", Match: []string{"rm *"},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		f, _ := policyfile.Load(wp)
		if len(f.Rules) != 2 || f.Rules[0].Effect != "deny" {
			t.Errorf("rules = %+v, want shell rule updated in place at index 0", f.Rules)
		}
	})

	t.Run("widening_update_requires_confirm", func(t *testing.T) {
		s, wp := seed(t)
		rec := postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "allow", Match: []string{"rm *"},
		})
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (widening needs confirm)", rec.Code)
		}
		if f, _ := policyfile.Load(wp); f.Rules[0].Effect != "ask" {
			t.Error("rule widened without confirm")
		}

		rec = postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "allow", Match: []string{"rm *"}, Confirm: true,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("confirmed widening status = %d, want 200", rec.Code)
		}
		if f, _ := policyfile.Load(wp); f.Rules[0].Effect != "allow" {
			t.Error("confirmed widening did not apply")
		}
	})

	t.Run("replay_when_target_already_exists_is_ok", func(t *testing.T) {
		s, wp := seed(t)
		body := policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "deny", Match: []string{"rm *"},
		}
		if rec := postRules(t, s, body); rec.Code != http.StatusOK {
			t.Fatalf("first update = %d", rec.Code)
		}
		// Retry with the same body: old rule is gone, target exists → 200.
		if rec := postRules(t, s, body); rec.Code != http.StatusOK {
			t.Fatalf("replayed update = %d, want 200 (idempotent)", rec.Code)
		}
		f, _ := policyfile.Load(wp)
		if len(f.Rules) != 2 {
			t.Errorf("replay duplicated rules: %+v", f.Rules)
		}
	})

	t.Run("missing_rule_is_404", func(t *testing.T) {
		s, _ := seed(t)
		rec := postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "deny", Match: []string{"never-added *"},
		})
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("invalid_new_effect_is_400", func(t *testing.T) {
		s, _ := seed(t)
		rec := postRules(t, s, policyRuleBody{
			Op: "update", Scope: "workspace", Capability: "shell",
			Effect: "ask", NewEffect: "frobnicate", Match: []string{"rm *"},
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}

func TestPolicyExplainShellRequiresResource(t *testing.T) {
	// KAS has no command-independent shell decision; the handler refuses
	// the simulation up front with a clear reason instead of forwarding a
	// request that can only fail (and used to surface as a misleading
	// "unavailable" error).
	f := &fakePolicy{explain: &vibekit.PolicyExplainResult{Capability: "shell", Effect: "ask"}}
	s := &Server{policy: f}
	b, _ := json.Marshal(vibekit.PolicyExplainRequest{Capability: "shell", Resource: "   "})
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/explain", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handlePolicyExplain(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (shell needs a resource)", rec.Code)
	}
	if len(f.explainReqs) != 0 {
		t.Errorf("explain forwarded despite missing resource: %+v", f.explainReqs)
	}
}
