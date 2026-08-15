package hub

// Tests for permissions_policy.go: the _kiro/permissions/list + explain
// result parse (incl. the camelCase→snake mapping for explain), and the
// session-scoped bridge-targeting invariant (list/explain DO inject a
// sessionId, unlike the workspace-global knowledge/spec calls). The utility
// bridge that serves these calls is the shared fakeBridge from newTestHub,
// seeded with canned results.

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func seedPolicy(br *fakeBridge, list, explain string) {
	br.callResults = map[string]json.RawMessage{
		methodV3PermissionsList:    json.RawMessage(list),
		methodV3PermissionsExplain: json.RawMessage(explain),
	}
}

func TestPolicyList(t *testing.T) {
	h, _, br := newTestHub()
	// Shape captured from a live v3 permissions/list reply.
	seedPolicy(br, `{"rules":[`+
		`{"capability":"fs_write","effect":"allow","match":["/x/**"],"scope":"user","source":"/u/permissions.yaml"},`+
		`{"capability":"shell","effect":"deny","match":["sudo *"],"scope":"user","source":"/u/permissions.yaml"}]}`, `{}`)

	rules, err := h.PolicyList(t.Context(), "user")
	if err != nil {
		t.Fatalf("PolicyList: %v", err)
	}
	if len(rules) != 2 || rules[0].Capability != "fs_write" || rules[0].Effect != "allow" ||
		rules[1].Effect != "deny" || rules[1].Scope != "user" {
		t.Errorf("rules = %+v", rules)
	}
	// list is session-scoped: the sessionId MUST be injected, and the scope
	// filter forwarded.
	p := br.paramsFor(methodV3PermissionsList)
	if p[api.KeySessionID] == nil || p[api.KeySessionID] == "" {
		t.Errorf("permissions/list params missing sessionId: %+v", p)
	}
	if p["scope"] != "user" {
		t.Errorf("scope filter not forwarded: %+v", p)
	}
}

func TestPolicyListOmitsEmptyScope(t *testing.T) {
	h, _, br := newTestHub()
	seedPolicy(br, `{"rules":[]}`, `{}`)
	if _, err := h.PolicyList(t.Context(), ""); err != nil {
		t.Fatalf("PolicyList: %v", err)
	}
	if _, hasScope := br.paramsFor(methodV3PermissionsList)["scope"]; hasScope {
		t.Error("empty scope should be omitted from params, not sent as \"\"")
	}
}

func TestPolicyExplainMapsCamelCase(t *testing.T) {
	h, _, br := newTestHub()
	seedPolicy(br, `{"rules":[]}`, `{"capability":"fs_write","resource":"/x/y","effect":"ask",`+
		`"isExplicitAsk":true,"matchedRule":{"capability":"fs_write","match":["/x/**"],"effect":"ask"},`+
		`"scope":"workspace","source":"/w/permissions.yaml"}`)

	res, err := h.PolicyExplain(t.Context(), api.PolicyExplainRequest{Capability: "fs_write", Resource: "/x/y"})
	if err != nil {
		t.Fatalf("PolicyExplain: %v", err)
	}
	if res.Effect != "ask" || !res.IsExplicitAsk || res.Scope != "workspace" {
		t.Errorf("explain = %+v", res)
	}
	if res.MatchedRule == nil || res.MatchedRule.Capability != "fs_write" || len(res.MatchedRule.Match) != 1 {
		t.Errorf("matchedRule = %+v", res.MatchedRule)
	}
	// explain forwards capability + resource + sessionId.
	p := br.paramsFor(methodV3PermissionsExplain)
	if p["capability"] != "fs_write" || p["resource"] != "/x/y" || p[api.KeySessionID] == nil {
		t.Errorf("explain params = %+v", p)
	}
}

func TestPolicyExplainRequiresTarget(t *testing.T) {
	h, _, br := newTestHub()
	seedPolicy(br, `{}`, `{}`)
	if _, err := h.PolicyExplain(t.Context(), api.PolicyExplainRequest{}); err == nil {
		t.Error("PolicyExplain with no capability/toolId should error")
	}
}
