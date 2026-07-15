// Native Cedar policy VIEW (_kiro/permissions/list + explain).
//
// vibekit reads kiro-cli's native permission policy as the source of truth
// for what is ENFORCED, and exposes it read-only at GET /api/permissions.
// Editing is a FILE write (internal/policyfile) that KAS hot-reloads — not
// an RPC — so this file is purely the reader.
//
// The queries route through the long-lived utility bridge (same pattern as
// account_usage.go / spec.go / knowledge.go) so the Permissions panel works
// with no chat open. list is synchronous and explain is a pure simulation
// (verified to raise no consent prompt), so neither has a side effect on the
// agent — unlike policy/check, which is deliberately never called.
//
// The Hub satisfies api.PolicyProvider.

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

var _ api.PolicyProvider = (*Hub)(nil)

// policyCallTimeout bounds one _kiro/permissions/{list,explain} round-trip.
// Both hold the single utility mutex across bridge.Call, so without a
// deadline a wedged list/explain would starve every other utility read
// (chat auto-rename, explain-error, commit-msg, account-usage). Matches the
// 45s the knowledge/spec sibling reads use.
const policyCallTimeout = 45 * time.Second

// ensureUtility lazily constructs the shared utility runtime (session +
// text-gen agent; same guard used by UtilityPrompt / AccountUsage / spec /
// knowledge) with its hooks-management plumbing injected.
//
// The utility session opts into KAS's v2 hook engine (enableHooks →
// _meta.kiro.hooks) so the hooks dashboard can list/toggle/trigger hooks over
// it; chat bridges deliberately don't (see hooks.go). It issues no agent tool
// calls, so no hook ever auto-fires here — the only executeHook it answers is a
// user-initiated "Run now" trigger, gated on expectingHookExec.
func (h *Hub) ensureUtility() *utilityRuntime {
	h.bridge.utilityOnce.Do(func() {
		h.bridge.utility = newUtilityRuntime(
			h.lifecycle.shutdownCtx, h.bridge.factory, h.Models,
			utilitySessionHooks{
				runHookCommand:    h.runHookCommand,
				onHooksChanged:    h.broadcastHooksChanged,
				onGovernanceState: h.cacheGovernanceFromUtility,
			},
			true, // enableHooks
		)
	})
	return h.bridge.utility
}

// PolicyList returns the native policy rules, optionally filtered to one
// scope (empty = all scopes). Backed by _kiro/permissions/list on the
// utility bridge.
func (h *Hub) PolicyList(ctx context.Context, scope string) ([]api.PolicyRule, error) {
	extra := map[string]any{}
	if scope != "" {
		extra["scope"] = scope
	}
	cctx, cancel := context.WithTimeout(ctx, policyCallTimeout)
	defer cancel()
	raw, err := h.ensureUtility().session.policyRaw(cctx, methodV3PermissionsList, extra)
	if err != nil {
		return nil, err
	}
	var out struct {
		Rules []api.PolicyRule `json:"rules"`
	}
	if len(raw) == 0 {
		return []api.PolicyRule{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse permissions/list: %w", err)
	}
	if out.Rules == nil {
		out.Rules = []api.PolicyRule{}
	}
	return out.Rules, nil
}

// explainWire mirrors the KAS _kiro/permissions/explain reply (camelCase).
type explainWire struct {
	Capability  string `json:"capability"`
	Resource    string `json:"resource"`
	Effect      string `json:"effect"`
	MatchedRule *struct {
		Capability string   `json:"capability"`
		Effect     string   `json:"effect"`
		Match      []string `json:"match"`
		Exclude    []string `json:"exclude"`
	} `json:"matchedRule"`
	Scope         string `json:"scope"`
	Source        string `json:"source"`
	IsExplicitAsk bool   `json:"isExplicitAsk"`
}

// PolicyExplain simulates the policy decision for a capability/resource
// WITHOUT executing anything or raising a consent prompt (KAS
// evaluateSingleResource). Exactly one of Capability / ToolID is required;
// KAS additionally requires a resource for the shell capability.
func (h *Hub) PolicyExplain(ctx context.Context, req api.PolicyExplainRequest) (*api.PolicyExplainResult, error) {
	extra := map[string]any{}
	switch {
	case req.Capability != "":
		extra["capability"] = req.Capability
	case req.ToolID != "":
		extra["toolId"] = req.ToolID
	default:
		return nil, errors.New("capability or tool_id required")
	}
	if req.Resource != "" {
		extra["resource"] = req.Resource
	}
	cctx, cancel := context.WithTimeout(ctx, policyCallTimeout)
	defer cancel()
	raw, err := h.ensureUtility().session.policyRaw(cctx, methodV3PermissionsExplain, extra)
	if err != nil {
		return nil, err
	}
	var w explainWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("parse permissions/explain: %w", err)
	}
	res := &api.PolicyExplainResult{
		Capability:    w.Capability,
		Resource:      w.Resource,
		Effect:        w.Effect,
		IsExplicitAsk: w.IsExplicitAsk,
		Scope:         w.Scope,
		Source:        w.Source,
	}
	if w.MatchedRule != nil {
		res.MatchedRule = &api.PolicyRuleCore{
			Capability: w.MatchedRule.Capability,
			Match:      w.MatchedRule.Match,
			Exclude:    w.MatchedRule.Exclude,
			Effect:     w.MatchedRule.Effect,
		}
	}
	return res, nil
}
