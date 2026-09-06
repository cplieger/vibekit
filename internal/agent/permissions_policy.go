// Native Cedar policy VIEW (_kiro/permissions/list + explain), read-only at
// GET /api/permissions. Editing is a FILE write (internal/policyfile) KAS
// hot-reloads, not an RPC. Both queries route through the utility bridge.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// policyCallTimeout bounds one _kiro/permissions/{list,explain} round-trip.
// Both hold the single utility mutex across bridge.Call, so without a
// deadline a wedged list/explain would starve every other utility read.
const policyCallTimeout = 45 * time.Second

// buildUtility is the lease's constructor, handed to it at wiring time. It
// lives here rather than inside the lease because of what it CLOSES OVER: two
// Settings hooks and the token source, all of which point back into runtime
// services while those same services lease the utility runtime.
func (rt *Runtime) buildUtility() *utilityRuntime {
	return newUtilityRuntime(
		rt.lifecycle.shutdownCtx, rt.bridge.factory, rt.Models,
		utilitySessionHooks{
			onHooksChanged:       rt.config.broadcastHooksChanged,
			onGovernanceState:    rt.config.cacheGovernanceFromUtility,
			onPolicyNotification: rt.forwardPolicyNotification,
			// The step-transcript seam: a `session/load` of a step's session replays
			// frames the utility session reads as foreign. No-ops until a read is open.
			onForeignUpdate: func(sessionID string, kind vibekit.ACPUpdateKind, update json.RawMessage) bool {
				return rt.runs.stepReplays.ingest(sessionID, kind, update)
			},
			onFrameDrained: func(at drainPoint, force bool) {
				rt.runs.stepReplays.settleConsumed(at, force)
			},
			presets: func(ctx context.Context) []string {
				return securityPresets(ctx, rt.lifecycle.configDir)
			},
			tokenSource: rt.inbound.kiroAccessTokenResult,
		},
		rt.secrets,
		true, // enableHooks
	)
}

// forwardPolicyNotification hands a _kiro/policy/{changed,error} notification the
// UTILITY session received to the same translator the chat dispatch table uses, so
// one decode serves both doors. Broadcast workspace-global (empty chatID), on
// context.Background(): forward is a goroutine with no request behind it.
func (rt *Runtime) forwardPolicyNotification(msg *vibekit.RPCResponse) {
	switch msg.Method {
	case methodV3PolicyChanged:
		rt.translator.HandlePolicyChanged(context.Background(), "", msg)
	case methodV3PolicyError:
		rt.translator.HandlePolicyError(context.Background(), "", msg)
	}
}

// PolicyList returns the native policy rules, optionally filtered to one
// scope (empty = all scopes). Backed by _kiro/permissions/list on the
// utility bridge.
func (st *Settings) PolicyList(ctx context.Context, scope string) ([]vibekit.PolicyRule, error) {
	extra := map[string]any{}
	if scope != "" {
		extra["scope"] = scope
	}
	cctx, cancel := context.WithTimeout(ctx, policyCallTimeout)
	defer cancel()
	raw, err := st.utility().session.policyRaw(cctx, methodV3PermissionsList, extra)
	if err != nil {
		return nil, err
	}
	var out struct {
		Rules []vibekit.PolicyRule `json:"rules"`
	}
	if len(raw) == 0 {
		return []vibekit.PolicyRule{}, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse permissions/list: %w", err)
	}
	if out.Rules == nil {
		out.Rules = []vibekit.PolicyRule{}
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
func (st *Settings) PolicyExplain(ctx context.Context, req vibekit.PolicyExplainRequest) (*vibekit.PolicyExplainResult, error) {
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
	raw, err := st.utility().session.policyRaw(cctx, methodV3PermissionsExplain, extra)
	if err != nil {
		return nil, err
	}
	var w explainWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("parse permissions/explain: %w", err)
	}
	res := &vibekit.PolicyExplainResult{
		Capability:    w.Capability,
		Resource:      w.Resource,
		Effect:        w.Effect,
		IsExplicitAsk: w.IsExplicitAsk,
		Scope:         w.Scope,
		Source:        w.Source,
	}
	if w.MatchedRule != nil {
		res.MatchedRule = &vibekit.PolicyRuleCore{
			Capability: w.MatchedRule.Capability,
			Match:      w.MatchedRule.Match,
			Exclude:    w.MatchedRule.Exclude,
			Effect:     w.MatchedRule.Effect,
		}
	}
	return res, nil
}
