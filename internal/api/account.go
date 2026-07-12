package api

// Account/subscription-level usage, distinct from a chat's per-session
// context ring (which reads the v3 usage_update notification). This is
// the ACCOUNT quota/credits surface returned by the KAS
// _kiro/account/getUsage request (GetUsageLimits under the hood), served
// to the client at GET /api/account/usage and rendered only in the
// sidebar status footer.
//
// Fields mirror the KAS getUsage `data` object (snake_cased for the
// client). Parsing lives in hub/account_usage.go; the raw KAS shape is
// not exposed to clients.

// AccountUsage is the account/subscription usage snapshot for the
// signed-in identity.
type AccountUsage struct {
	// PlanName is the subscription title (e.g. "KIRO POWER").
	PlanName string `json:"plan_name,omitempty"`
	// BillingCycleReset is the YYYY-MM-DD reset date, or "" when unknown.
	BillingCycleReset string `json:"billing_cycle_reset,omitempty"`
	// FetchedAt is the RFC3339 timestamp of the fetch that produced this
	// snapshot, so the client can show freshness.
	FetchedAt string `json:"fetched_at,omitempty"`
	// Note carries a human-readable status for degraded states (e.g. an
	// admin-managed plan that returns no breakdowns).
	Note string `json:"note,omitempty"`
	// Breakdowns is the per-resource usage list (typically a single
	// CREDIT entry).
	Breakdowns []AccountUsageBreakdown `json:"breakdowns"`
	// IsEnterprise reports whether the plan is an enterprise/managed plan.
	IsEnterprise bool `json:"is_enterprise,omitempty"`
	// OveragesEnabled reports whether overage billing is enabled.
	OveragesEnabled bool `json:"overages_enabled,omitempty"`
	// Stale is true when this snapshot was served from the last-known
	// cache because a fresh fetch failed (no live bridge, rate limit).
	Stale bool `json:"stale,omitempty"`
}

// AccountUsageBreakdown is one resource line of an AccountUsage (e.g.
// credits). Used/Limit are precision floats as reported by KAS.
type AccountUsageBreakdown struct {
	// ResourceType is the KAS resource key (e.g. "CREDIT").
	ResourceType string `json:"resource_type,omitempty"`
	// DisplayName is the human label (e.g. "Credits").
	DisplayName string `json:"display_name,omitempty"`
	// Currency is the ISO currency for monetary fields (e.g. "USD").
	Currency string `json:"currency,omitempty"`
	// Used is the amount consumed this cycle.
	Used float64 `json:"used"`
	// Limit is the plan allowance; meaningful only when HasLimit is true.
	Limit float64 `json:"limit"`
	// CurrentOverages is the amount consumed beyond Limit.
	CurrentOverages float64 `json:"current_overages,omitempty"`
	// OverageCharges is the monetary overage charge in Currency.
	OverageCharges float64 `json:"overage_charges,omitempty"`
	// Percentage is floor(Used/Limit*100); can exceed 100 on overage.
	Percentage int `json:"percentage"`
	// HasLimit reports whether Limit is a real cap (false = unlimited).
	HasLimit bool `json:"has_limit,omitempty"`
}
