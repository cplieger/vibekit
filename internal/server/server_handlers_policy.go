package server

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/policyfile"
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
	scope := r.URL.Query().Get("scope")
	view := api.PolicyView{
		WritableScopes: []string{policyfile.ScopeUser, policyfile.ScopeWorkspace},
		Capabilities:   policyfile.Capabilities(),
		Available:      true,
	}
	if s.policy != nil {
		rules, err := s.policy.PolicyList(r.Context(), scope)
		if err == nil {
			view.Rules = rules
			api.WriteJSON(w, view)
			return
		}
		slog.Warn("policy view: live list failed, falling back to file read", "error", err)
	}
	// Fallback: read the editable files directly so the editor works even
	// when no bridge can answer (e.g. not signed in).
	view.Available = false
	view.Rules = s.policyRulesFromFiles(scope)
	api.WriteJSON(w, view)
}

// policyRulesFromFiles reads the user + workspace permissions.yaml directly
// and returns them as api.PolicyRule with provenance. Used only as the
// no-bridge fallback for the view.
func (s *Server) policyRulesFromFiles(scope string) []api.PolicyRule {
	home, err := os.UserHomeDir()
	if err != nil {
		return []api.PolicyRule{}
	}
	out := []api.PolicyRule{}
	for _, sc := range []string{policyfile.ScopeUser, policyfile.ScopeWorkspace} {
		if scope != "" && scope != sc {
			continue
		}
		path, perr := policyfile.PathFor(sc, home, s.workDir)
		if perr != nil {
			continue
		}
		f, lerr := policyfile.Load(path)
		if lerr != nil || f == nil {
			continue
		}
		for _, ru := range f.Rules {
			out = append(out, api.PolicyRule{
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
		api.WriteJSONStatus(w, http.StatusServiceUnavailable, api.ErrorJSON("policy explain unavailable"))
		return
	}
	var req api.PolicyExplainRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Capability == "" && req.ToolID == "" {
		api.BadRequest(w, "capability or tool_id required")
		return
	}
	res, err := s.policy.PolicyExplain(r.Context(), req)
	if err != nil {
		slog.Warn("policy explain failed", "error", err)
		api.WriteJSONStatus(w, http.StatusBadGateway, api.ErrorJSON("policy explain failed"))
		return
	}
	api.WriteJSON(w, res)
}

// policyRuleBody is the POST /api/permissions/rules request. op is
// "add" | "remove". For add, an empty effect defaults to "ask"
// (conservative). For remove, effect is required and removing a "deny"
// requires confirm=true.
type policyRuleBody struct {
	Op         string   `json:"op"`
	Scope      string   `json:"scope"`
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	Match      []string `json:"match"`
	Exclude    []string `json:"exclude"`
	Confirm    bool     `json:"confirm"`
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
		api.BadRequest(w, "scope must be user or workspace")
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		api.InternalError(w, err)
		return
	}
	path, err := policyfile.PathFor(body.Scope, home, s.workDir)
	if err != nil {
		api.BadRequest(w, err.Error())
		return
	}
	switch body.Op {
	case "add":
		s.policyRuleAdd(w, r, &body, path)
	case "remove":
		s.policyRuleRemove(w, r, &body, path)
	default:
		api.BadRequest(w, "op must be add or remove")
	}
}

func (s *Server) policyRuleAdd(w http.ResponseWriter, r *http.Request, body *policyRuleBody, path string) {
	// Conservative default: a new rule with no explicit effect is `ask`,
	// never `allow`. Widening to allow requires the user to choose it.
	effect := body.Effect
	if effect == "" {
		effect = policyfile.EffectAsk
	}
	rule, err := policyfile.SanitizeRule(&policyfile.Rule{
		Capability: body.Capability, Effect: effect,
		Match: body.Match, Exclude: body.Exclude,
	})
	if err != nil {
		api.BadRequest(w, err.Error())
		return
	}
	f, err := policyfile.Load(path)
	if err != nil {
		api.WriteJSONStatus(w, http.StatusConflict,
			api.ErrorJSON("existing policy file could not be parsed; edit it manually"))
		return
	}
	changed, err := f.Upsert(&rule)
	if err != nil {
		api.BadRequest(w, err.Error())
		return
	}
	if !changed {
		api.Ok(w) // idempotent: identical rule already present
		return
	}
	if err := policyfile.Save(r.Context(), path, f); err != nil {
		api.InternalError(w, err)
		return
	}
	slog.Info("policy rule added", "scope", body.Scope, "capability", rule.Capability, "effect", rule.Effect)
	api.Ok(w)
}

func (s *Server) policyRuleRemove(w http.ResponseWriter, r *http.Request, body *policyRuleBody, path string) {
	if !policyfile.ValidEffect(body.Effect) {
		api.BadRequest(w, "effect required to remove a rule")
		return
	}
	// Removing a deny rule widens access — a destructive change. Require an
	// explicit confirm so it can't happen by accident.
	if body.Effect == policyfile.EffectDeny && !body.Confirm {
		api.WriteJSONStatus(w, http.StatusConflict,
			api.ErrorJSON("removing a deny rule widens access; resend with confirm=true"))
		return
	}
	rule, err := policyfile.SanitizeRule(&policyfile.Rule{
		Capability: body.Capability, Effect: body.Effect,
		Match: body.Match, Exclude: body.Exclude,
	})
	if err != nil {
		api.BadRequest(w, err.Error())
		return
	}
	f, err := policyfile.Load(path)
	if err != nil {
		api.WriteJSONStatus(w, http.StatusConflict,
			api.ErrorJSON("existing policy file could not be parsed; edit it manually"))
		return
	}
	if !f.Remove(&rule) {
		api.Ok(w) // idempotent: rule already absent
		return
	}
	if err := policyfile.Save(r.Context(), path, f); err != nil {
		api.InternalError(w, err)
		return
	}
	slog.Info("policy rule removed", "scope", body.Scope, "capability", rule.Capability, "effect", rule.Effect)
	api.Ok(w)
}
