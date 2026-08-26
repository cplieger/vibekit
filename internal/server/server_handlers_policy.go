package server

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/policyfile"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// Native Cedar policy endpoints (v3 / KAS).
//
//	GET  /api/permissions          → the native policy VIEW (source of truth
//	                                  for what is ENFORCED), via a bridge.
//	POST /api/permissions/explain  → pure "why" simulation (no consent prompt).
//	POST /api/permissions/rules    → add / remove a rule in the user or
//	                                  workspace permissions.yaml (KAS hot-reloads).
//
// The rule WRITER is a FILE write (internal/policyfile), not an RPC: KAS
// watches the file and reloads live. It is deliberately CONSERVATIVE — a new
// rule with no explicit effect defaults to `ask` (never `allow`), only the
// user + workspace scopes are writable, and removing a `deny` rule (which
// widens access) requires an explicit confirm. The existing legacy
// permission-mode radios, shell-tier command rules, supervised staging, and
// the native permission dialog are all untouched; this ADDS the native view +
// file editor.

// handlePolicyView serves GET /api/permissions: the native policy rule set
// grouped client-side by scope. Backed by _kiro/permissions/list on the
// utility bridge; on bridge failure it degrades to reading the editable
// user/workspace files directly so the panel + editor still work offline
// (Available=false signals the baseline scopes are missing).
func (s *Server) handlePolicyView(w http.ResponseWriter, r *http.Request) {
	// Gated here, not on the ServeMux pattern: a method-pattern mismatch falls
	// through to the SPA mount and answers 200 with index.html. See
	// server.go's ListenAndServe.
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	scope := r.URL.Query().Get("scope")
	view := vibekit.PolicyView{
		WritableScopes: []string{policyfile.ScopeUser, policyfile.ScopeWorkspace},
		// The relaxation set travels on BOTH branches: it is a fixed property of
		// this build, not a projection of the live policy, so the switch stays
		// usable when no bridge can answer (which is exactly when a user is
		// most likely to be editing rules by hand).
		RelaxCapabilities: policyfile.RelaxCapabilities(),
		Profiles:          securityProfileCatalog(),
		Profile:           s.activeProfile(r.Context()),
		Available:         true,
	}
	if s.policy != nil {
		rules, err := s.policy.PolicyList(r.Context(), scope)
		if err == nil {
			view.Rules = dedupePolicyRules(rules)
			view.Capabilities = pickerCapabilities(view.Rules)
			webhttp.WriteJSON(w, view)
			return
		}
		slog.Warn("policy view: live list failed, falling back to file read", "error", err)
	}
	// Fallback: read the editable files directly so the editor works even
	// when no bridge can answer (e.g. not signed in).
	view.Available = false
	view.Rules = dedupePolicyRules(s.policyRulesFromFiles(scope))
	view.Capabilities = pickerCapabilities(view.Rules)
	webhttp.WriteJSON(w, view)
}

// dedupePolicyRules drops rules that are identical in EVERY field, preserving
// the order the first copy arrived in.
//
// KAS's `_kiro/permissions/list` reports one rule several times. Measured live
// on kiro-cli 2.19.0: a workspace file holding exactly one rule
// (`capability: all, effect: allow, match: ['*']`) came back as TEN byte-
// identical entries carrying the same scope and the same source path, while
// every other rule in the same reply appeared once. vibekit forwarded them
// verbatim, so Settings -> Permissions rendered the user's single rule ten
// times, each with its own remove button.
//
// The key is the whole VALUE rather than a signature over capability + effect +
// globs, because scope and source are what legitimately separate two rows: the
// same rule written into both the user and the workspace file is two rules, and
// a reader needs to see both to know which file to edit. Two rows agreeing on
// all six fields cannot be told apart by anyone — including the remove button,
// which would delete "one of them" with nothing to say which.
//
// Done here rather than in the agent's PolicyList because this handler is the
// one place BOTH the live and the file-fallback projections pass through, and
// the fallback can produce a genuine cross-scope pair that must survive.
func dedupePolicyRules(rules []vibekit.PolicyRule) []vibekit.PolicyRule {
	if len(rules) == 0 {
		// Preserve the empty-not-null contract the wire field carries.
		return []vibekit.PolicyRule{}
	}
	out := make([]vibekit.PolicyRule, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, r := range rules {
		key := policyRuleKey(&r)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, r)
	}
	if dropped := len(rules) - len(out); dropped > 0 {
		slog.Debug("policy view: dropped identical duplicate rules", "dropped", dropped, "kept", len(out))
	}
	return out
}

// policyRuleKey is a total, collision-free key over a rule's six fields.
//
// Length-prefixing every element is what makes it collision-free: joining the
// glob lists with a separator would let `match: ["a", "b"]` and
// `match: ["a<sep>b"]` produce one key, and a glob is arbitrary user text that
// can contain any separator a reader might pick. The order of a rule's globs is
// significant here — two rules differing only in glob order are left as two
// rows, because reordering is not vibekit's call to make on the user's file.
func policyRuleKey(r *vibekit.PolicyRule) string {
	var b strings.Builder
	for _, s := range []string{r.Capability, r.Effect, r.Scope, r.Source} {
		fmt.Fprintf(&b, "%d:%s", len(s), s)
	}
	for _, list := range [][]string{r.Match, r.Exclude} {
		fmt.Fprintf(&b, "|%d|", len(list))
		for _, s := range list {
			fmt.Fprintf(&b, "%d:%s", len(s), s)
		}
	}
	return b.String()
}

// pickerCapabilities is what the capability dropdowns offer: vibekit's suggested
// set UNION every capability the returned rules already use.
//
// The union is what keeps the picker from going stale. The suggested set is a
// hand-copied snapshot of a list KAS does not expose, so it cannot learn about a
// capability the agent server gains — but the rules KAS reports here CAN, and
// they include every scope's baseline (kiro, administration, agent, session), not
// just the two vibekit writes. So the day one rule anywhere uses a new
// capability, it becomes selectable, with no vibekit release.
//
// Deliberately not a filter on what may be WRITTEN: a capability absent from
// both the snapshot and the current rules is still writable (see SanitizeRule),
// it just is not suggested.
//
// Sorted as ONE list, not suggestions-then-extras: the dropdown is alphabetical,
// and a reader looking for "hooks" should not have to know whether vibekit
// shipped knowing about it. slices.Sorted(maps.Keys(…)) (Go 1.23) is that in one
// expression; the set is the only intermediate, which is what removes the
// separate duplicate test and the `added` flag that existed solely to skip
// re-sorting fifteen strings.
//
// The result is non-empty for any input because the snapshot seeds it, so the
// `capabilities` wire field never degrades from [] to null.
// TestPickerCapabilities_NoRulesIsTheSuggestedSet asserts that rather than
// leaving it to the reader.
func pickerCapabilities(rules []vibekit.PolicyRule) []string {
	suggested := policyfile.Capabilities()
	seen := make(map[string]struct{}, len(suggested)+len(rules))
	for _, c := range suggested {
		seen[c] = struct{}{}
	}
	for i := range rules {
		if c := rules[i].Capability; c != "" {
			seen[c] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// policyRulesFromFiles reads the user + workspace permissions.yaml directly
// and returns them as vibekit.PolicyRule with provenance. Used only as the
// no-bridge fallback for the view.
func (s *Server) policyRulesFromFiles(scope string) []vibekit.PolicyRule {
	home, err := os.UserHomeDir()
	if err != nil {
		return []vibekit.PolicyRule{}
	}
	out := []vibekit.PolicyRule{}
	for _, sc := range []string{policyfile.ScopeUser, policyfile.ScopeWorkspace} {
		if scope != "" && scope != sc {
			continue
		}
		path, perr := policyfile.PathFor(sc, policyfile.Roots{Home: home, WorkDir: s.workDir})
		if perr != nil {
			continue
		}
		f, lerr := policyfile.Load(path)
		if lerr != nil || f == nil {
			continue
		}
		for _, ru := range f.Rules {
			out = append(out, vibekit.PolicyRule{
				Capability: ru.Capability, Effect: ru.Effect,
				Match: ru.Match, Exclude: ru.Exclude,
				Scope: sc, Source: path,
			})
		}
	}
	return out
}

// handlePolicyExplain serves POST /api/permissions/explain: a pure
// simulation of the policy decision for a capability/resource. Safe — KAS
// evaluateSingleResource raises no consent prompt (verified live).
func (s *Server) handlePolicyExplain(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	if s.policy == nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("policy explain unavailable"))
		return
	}
	var req vibekit.PolicyExplainRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Capability == "" && req.ToolID == "" {
		httpreply.BadRequest(w, "capability or tool_id required")
		return
	}
	// KAS requires a resource for the shell capability (there is no
	// command-independent shell decision). Refuse it here with a clear
	// reason instead of forwarding a request that can only fail.
	if req.Capability == capShell && strings.TrimSpace(req.Resource) == "" {
		httpreply.BadRequest(w, "the shell capability needs a resource (the command) to evaluate")
		return
	}
	res, err := s.policy.PolicyExplain(r.Context(), req)
	if err != nil {
		slog.Warn("policy explain failed", "error", err)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("policy explain failed"))
		return
	}
	webhttp.WriteJSON(w, res)
}

// policyRuleBody is the POST /api/permissions/rules request. op is
// "add" | "remove" | "update". For add, an empty effect defaults to "ask"
// (conservative). For remove and update, the rule fields identify the
// EXISTING rule (effect required); removing a "deny", like any update
// that widens access, requires confirm=true. update changes the rule's
// effect to new_effect in place. guard_resource (optional, add+allow
// only) is a concrete resource the caller wants the rule to take effect
// for — e.g. the command behind a permission dialog's "Always allow":
// when an explicit ask rule already covers it, the allow would be
// silently shadowed (ask > allow), so the write is refused instead.
type policyRuleBody struct {
	Op            string   `json:"op"`
	Scope         string   `json:"scope"`
	Capability    string   `json:"capability"`
	Effect        string   `json:"effect"`
	NewEffect     string   `json:"new_effect"`
	GuardResource string   `json:"guard_resource"`
	Match         []string `json:"match"`
	Exclude       []string `json:"exclude"`
	Confirm       bool     `json:"confirm"`
}

// capShell is the capability whose decisions are always resource-scoped.
const capShell = "shell"

// effectRank orders effects by strictness for the widening gate: moving a
// rule to a lower rank grants the agent more than it had.
var effectRank = map[string]int{
	policyfile.EffectDeny:  2,
	policyfile.EffectAsk:   1,
	policyfile.EffectAllow: 0,
}

// handlePolicyRules serves POST /api/permissions/rules (op=add|remove).
// Writes the scope's permissions.yaml; KAS hot-reloads and emits
// _kiro/policy/changed → the permissions_changed SSE.
func (s *Server) handlePolicyRules(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var body policyRuleBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !policyfile.ValidScope(body.Scope) {
		httpreply.BadRequest(w, "scope must be user or workspace")
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		httpreply.InternalError(w, err)
		return
	}
	path, err := policyfile.PathFor(body.Scope, policyfile.Roots{Home: home, WorkDir: s.workDir})
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	switch body.Op {
	case "add":
		s.policyRuleAdd(w, r, &body, path)
	case "remove":
		policyRuleRemove(w, r, &body, path)
	case "update":
		policyRuleUpdate(w, r, &body, path)
	default:
		httpreply.BadRequest(w, "op must be add, remove, or update")
	}
}

func (s *Server) policyRuleAdd(w http.ResponseWriter, r *http.Request, body *policyRuleBody, path string) {
	// Conservative default: a new rule with no explicit effect is `ask`,
	// never `allow`. Widening to allow requires the user to choose it.
	effect := cmp.Or(body.Effect, policyfile.EffectAsk)
	rule, err := policyfile.SanitizeRule(&policyfile.Rule{
		Capability: body.Capability, Effect: effect,
		Match: body.Match, Exclude: body.Exclude,
	})
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	// vibekit does not own the capability vocabulary (see policyfile.SanitizeRule),
	// so an unrecognised name is written through for KAS to judge. KAS then SKIPS
	// that one rule as non-fatal and says so on _kiro/policy/changed, which reaches
	// the user in the permissions panel — but nothing on this side records having
	// written it, so a rule that silently does nothing has no server-side trace to
	// correlate against. One line closes that; the write still goes through.
	if !slices.Contains(policyfile.Capabilities(), rule.Capability) {
		slog.Warn("writing a policy rule naming a capability vibekit does not recognise; "+
			"kiro-cli decides whether it loads",
			"capability", rule.Capability, "effect", rule.Effect, "scope", body.Scope)
	}
	if rule.Effect == policyfile.EffectAllow && body.GuardResource != "" &&
		!s.guardAllowRule(w, r, &rule, body.GuardResource) {
		return
	}
	f, err := policyfile.Load(path)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("existing policy file could not be parsed; edit it manually"))
		return
	}
	changed, err := f.Upsert(&rule)
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	if !changed {
		webhttp.Ok(w) // idempotent: identical rule already present
		return
	}
	if err := policyfile.Save(r.Context(), path, f); err != nil {
		httpreply.InternalError(w, err)
		return
	}
	slog.Info("policy rule added", "scope", body.Scope, "capability", rule.Capability, "effect", rule.Effect)
	webhttp.Ok(w)
}

// guardAllowRule pre-flights an allow-rule write against the LIVE policy
// via explain (a pure simulation): when an explicit ask rule already
// covers the guard resource, KAS would shadow the new allow (ask > allow),
// so the write is refused with a clear reason instead of persisting a
// rule that changes nothing. Fails CLOSED — if the decision cannot be
// verified, the rule is not written (the caller's fallback is allow-once).
// Returns true when the write may proceed; otherwise the response has
// been written.
func (s *Server) guardAllowRule(w http.ResponseWriter, r *http.Request, rule *policyfile.Rule, resource string) bool {
	if s.policy == nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("cannot verify the rule against the live policy; rule not written"))
		return false
	}
	res, err := s.policy.PolicyExplain(r.Context(), vibekit.PolicyExplainRequest{
		Capability: rule.Capability, Resource: resource,
	})
	if err != nil {
		slog.Warn("policy rule add: guard explain failed", "error", err)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway,
			httpreply.ErrorJSON("cannot verify the rule against the live policy; rule not written"))
		return false
	}
	if res.IsExplicitAsk {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("an explicit ask rule covers this command; the new allow rule would be shadowed and was not written"))
		return false
	}
	return true
}

func policyRuleRemove(w http.ResponseWriter, r *http.Request, body *policyRuleBody, path string) {
	if !policyfile.ValidEffect(body.Effect) {
		httpreply.BadRequest(w, "effect required to remove a rule")
		return
	}
	// Removing a deny rule widens access — a destructive change. Require an
	// explicit confirm so it can't happen by accident.
	if body.Effect == policyfile.EffectDeny && !body.Confirm {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("removing a deny rule widens access; resend with confirm=true"))
		return
	}
	rule, err := policyfile.SanitizeRule(&policyfile.Rule{
		Capability: body.Capability, Effect: body.Effect,
		Match: body.Match, Exclude: body.Exclude,
	})
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	f, err := policyfile.Load(path)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("existing policy file could not be parsed; edit it manually"))
		return
	}
	if !f.Remove(&rule) {
		webhttp.Ok(w) // idempotent: rule already absent
		return
	}
	if err := policyfile.Save(r.Context(), path, f); err != nil {
		httpreply.InternalError(w, err)
		return
	}
	slog.Info("policy rule removed", "scope", body.Scope, "capability", rule.Capability, "effect", rule.Effect)
	webhttp.Ok(w)
}

// policyRuleUpdate changes an existing rule's effect in place (op=update):
// the body's rule fields identify the current rule, new_effect is the
// target. One atomic file write — never a client-side remove+add that
// could half-apply. A widening change (deny→ask, deny→allow, ask→allow)
// grants the agent more than it had, so it requires confirm=true, same as
// removing a deny.
func policyRuleUpdate(w http.ResponseWriter, r *http.Request, body *policyRuleBody, path string) {
	if !policyfile.ValidEffect(body.Effect) {
		httpreply.BadRequest(w, "effect required to identify the rule")
		return
	}
	if !policyfile.ValidEffect(body.NewEffect) {
		httpreply.BadRequest(w, "new_effect must be allow, deny, or ask")
		return
	}
	if effectRank[body.NewEffect] < effectRank[body.Effect] && !body.Confirm {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("changing "+body.Effect+" to "+body.NewEffect+" widens access; resend with confirm=true"))
		return
	}
	rule, err := policyfile.SanitizeRule(&policyfile.Rule{
		Capability: body.Capability, Effect: body.Effect,
		Match: body.Match, Exclude: body.Exclude,
	})
	if err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	f, err := policyfile.Load(path)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusConflict,
			httpreply.ErrorJSON("existing policy file could not be parsed; edit it manually"))
		return
	}
	if !f.ReplaceEffect(&rule, body.NewEffect) {
		// Idempotent replay: the target state may already be on disk.
		target := rule
		target.Effect = body.NewEffect
		if f.Has(&target) {
			webhttp.Ok(w)
			return
		}
		httpreply.NotFound(w, "rule not found; refresh the policy view")
		return
	}
	if err := policyfile.Save(r.Context(), path, f); err != nil {
		httpreply.InternalError(w, err)
		return
	}
	slog.Info("policy rule updated", "scope", body.Scope,
		"capability", rule.Capability, "effect", body.Effect, "new_effect", body.NewEffect)
	webhttp.Ok(w)
}
