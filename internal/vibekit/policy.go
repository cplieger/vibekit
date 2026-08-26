package vibekit

// Native Cedar policy domain types (v3 / KAS).
//
// vibekit adopts kiro-cli's native permission policy as the source of
// truth for what is ENFORCED. The policy is read via the _kiro/permissions/
// list + explain extension requests on a live bridge, and edited by writing
// the user/workspace permissions.yaml files (which KAS hot-reloads). These
// types are the vibekit-facing shapes; the on-disk file shape lives in
// internal/policyfile.
//
// Wire facts verified live against the KAS 2.12 acp-server bundle:
//   - list  → {rules:[{capability, match?, exclude?, effect, scope, source}]}
//   - explain → {capability, resource, effect, isExplicitAsk, matchedRule?, scope, source}
//     (a PURE simulation — no consent prompt; safe for pre-flight)
//   - policy/check is NOT used: it calls acpToolApproval and raises a real
//     session/request_permission, so it is unsafe as a UI pre-flight query.
//
// Enum values on the wire (the Go string constants for both live in
// internal/policyfile, the package that writes the editable scopes):
//   - effect is 3-valued: allow | deny | ask (deny > ask > allow at
//     evaluation time)
//   - scope is kiro | administration | user | workspace | agent | session.
//     Only user + workspace are file-editable by vibekit;
//     kiro/administration are read-only baselines, agent comes from the
//     agent profile, and session is runtime state.

// PolicyRule is one native policy rule as reported by _kiro/permissions/list.
// Capability + effect are always present; match/exclude are optional glob
// lists; scope + source carry provenance (source is a file path for
// user/workspace rules, or a synthetic tag like "kiro-scope"/"agent-profile").
type PolicyRule struct {
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	Scope      string   `json:"scope"`
	Source     string   `json:"source"`
	Match      []string `json:"match,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
}

// PolicyRuleCore is the rule shape without provenance, used inside an
// explain result's matched_rule.
type PolicyRuleCore struct {
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	Match      []string `json:"match,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
}

// PolicyView is the GET /api/permissions response: the native policy rule
// set plus the metadata the editor needs. Available is false when no bridge
// could answer (the view falls back to reading the editable files directly).
// Capabilities and RelaxCapabilities answer different questions and neither is a
// filter on the other. Capabilities is what the rule-adder's dropdown OFFERS —
// the suggested set unioned with every capability the live rules already use, so
// it can learn a name vibekit shipped without. RelaxCapabilities is the fixed
// membership of the workspace relaxation switch, derived in policyfile and
// deliberately not discovered: it decides what one click grants, so it may not
// grow from whatever happens to be in the returned rules.
type PolicyView struct {
	Rules             []PolicyRule `json:"rules"`
	WritableScopes    []string     `json:"writable_scopes"`
	Capabilities      []string     `json:"capabilities"`
	RelaxCapabilities []string     `json:"relax_capabilities"`
	// Profiles is the security-posture ladder in picker order, loosest last, and
	// Profile is the one in force. Both travel here rather than being derived
	// client-side for the same reason RelaxCapabilities does: the ladder decides
	// what one click grants, so policyfile owns it and the client renders it.
	//
	// Order is part of the payload. A client that sorted these would put the
	// loosest option somewhere in the middle of a list a reader scans from
	// cautious to permissive.
	Profiles  []SecurityProfile `json:"profiles"`
	Profile   string            `json:"profile"`
	Available bool              `json:"available"`
}

// SecurityProfile is one entry in the picker: the persisted id and the KAS policy
// preset ids it sends. The presets travel so the UI can say what a profile grants
// without a second round trip, and so a reader can tell two profiles apart by
// something more than their names.
type SecurityProfile struct {
	ID      string   `json:"id"`
	Presets []string `json:"presets"`
}

// PolicyExplainRequest is the POST /api/permissions/explain body. Exactly
// one of Capability / ToolID is required; Resource is the path (fs caps) or
// command (shell) being simulated. KAS requires a resource for the shell
// capability.
type PolicyExplainRequest struct {
	Capability string `json:"capability,omitempty"`
	ToolID     string `json:"tool_id,omitempty"`
	Resource   string `json:"resource,omitempty"`
}

// PolicyExplainResult mirrors _kiro/permissions/explain (a pure simulation).
type PolicyExplainResult struct {
	Capability    string          `json:"capability"`
	Resource      string          `json:"resource,omitempty"`
	Effect        string          `json:"effect"`
	MatchedRule   *PolicyRuleCore `json:"matched_rule,omitempty"`
	Scope         string          `json:"scope,omitempty"`
	Source        string          `json:"source,omitempty"`
	IsExplicitAsk bool            `json:"is_explicit_ask"`
}

// PolicyErrorItem is one entry in a policy/changed or policy/error
// notification's errors array (from KAS).
type PolicyErrorItem struct {
	Scope   string `json:"scope,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal,omitempty"`
}

// PermissionsChangedPayload is the SSE payload for type="permissions_changed"
// (translated from _kiro/policy/changed). Clients refetch GET /api/permissions
// on receipt. Status is "success" or "failed"; Errors carries any non-fatal
// warnings or the fatal parse error.
type PermissionsChangedPayload struct {
	Status string            `json:"status,omitempty"`
	Errors []PolicyErrorItem `json:"errors,omitempty"`
}

// PolicyErrorPayload is the SSE payload for type="policy_error" (translated
// from _kiro/policy/error). Surfaced as a banner so a bad hand-edit or a
// rejected rule is visible without hunting the logs.
type PolicyErrorPayload struct {
	Errors []PolicyErrorItem `json:"errors,omitempty"`
}
