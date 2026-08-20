// Account/subscription usage fetch (_kiro/account/getUsage).
//
// Account-level usage is distinct from a chat's per-session context ring
// (which reads the v3 usage_update notification). It's a bare C→A request
// that needs a live acp session with valid auth but no chat context, so we
// route it through the long-lived utility bridge. Served to the client at
// GET /api/account/usage and rendered only in the sidebar status footer.
//
// The account query is slow-changing and may be rate-limited, so the HTTP
// layer (server/server_handlers_account.go) fetches lazily on footer open
// and caches; this method is the uncached fetch + parse.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// accountUsageCallTimeout bounds one _kiro/account/getUsage round-trip.
// The leased utility session issues Calls OUTSIDE its lifecycle mutex
// (see utility_session.go's concurrent-call model), so this deadline is
// not about lock starvation — it keeps a wedged getUsage RPC from
// pinning its caller and the session lease indefinitely. Matches the
// 45s the knowledge/spec sibling reads use.
const accountUsageCallTimeout = 45 * time.Second

// AccountUsage fetches account/subscription usage via the utility bridge
// and parses the KAS getUsage result into the domain shape. Lazily
// constructs the utility bridge (same pattern as UtilityPrompt) so the
// footer works even when no chat is open. Satisfies server.AccountUsageProvider.
func (h *Runtime) AccountUsage(ctx context.Context) (*vibekit.AccountUsage, error) {
	cctx, cancel := context.WithTimeout(ctx, accountUsageCallTimeout)
	defer cancel()
	raw, err := h.ensureUtility().session.accountUsageRaw(cctx)
	if err != nil {
		return nil, err
	}
	return parseAccountUsage(raw)
}

// kasUsageResult mirrors the KAS _kiro/account/getUsage reply
// ({success, message, data}); data mirrors transformUsageLimits.
type kasUsageResult struct {
	Data    *kasUsageData `json:"data"`
	Message string        `json:"message"`
	Success bool          `json:"success"`
}

type kasUsageData struct {
	PlanName          string              `json:"planName"`
	BillingCycleReset string              `json:"billingCycleReset"`
	UsageBreakdowns   []kasUsageBreakdown `json:"usageBreakdowns"`
	IsEnterprise      bool                `json:"isEnterprise"`
	OveragesEnabled   bool                `json:"overagesEnabled"`
}

type kasUsageBreakdown struct {
	ResourceType    string  `json:"resourceType"`
	DisplayName     string  `json:"displayName"`
	Currency        string  `json:"currency"`
	Used            float64 `json:"used"`
	Limit           float64 `json:"limit"`
	CurrentOverages float64 `json:"currentOverages"`
	OverageCharges  float64 `json:"overageCharges"`
	Percentage      int     `json:"percentage"`
	HasLimit        bool    `json:"hasLimit"`
}

// parseAccountUsage converts the raw KAS getUsage result into the domain
// AccountUsage. A success=false reply (e.g. "Invalid profileArn.") is
// returned as an error; success=true with a nil data object (admin-managed
// plan) yields an AccountUsage carrying only the note.
func parseAccountUsage(raw json.RawMessage) (*vibekit.AccountUsage, error) {
	if len(raw) == 0 {
		return nil, errors.New("account usage: empty result")
	}
	var r kasUsageResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if !r.Success {
		msg := strings.TrimSpace(r.Message)
		if msg == "" {
			msg = "account usage unavailable"
		}
		return nil, errors.New(msg)
	}
	out := &vibekit.AccountUsage{FetchedAt: time.Now().UTC().Format(time.RFC3339)}
	if r.Data == nil {
		// Admin-managed plan: success with no breakdowns.
		out.Note = strings.TrimSpace(r.Message)
		out.Breakdowns = []vibekit.AccountUsageBreakdown{}
		return out, nil
	}
	out.PlanName = r.Data.PlanName
	out.BillingCycleReset = r.Data.BillingCycleReset
	out.IsEnterprise = r.Data.IsEnterprise
	out.OveragesEnabled = r.Data.OveragesEnabled
	out.Breakdowns = make([]vibekit.AccountUsageBreakdown, 0, len(r.Data.UsageBreakdowns))
	for _, b := range r.Data.UsageBreakdowns {
		out.Breakdowns = append(out.Breakdowns, vibekit.AccountUsageBreakdown{
			ResourceType:    b.ResourceType,
			DisplayName:     b.DisplayName,
			Currency:        b.Currency,
			Used:            b.Used,
			Limit:           b.Limit,
			CurrentOverages: b.CurrentOverages,
			OverageCharges:  b.OverageCharges,
			Percentage:      b.Percentage,
			HasLimit:        b.HasLimit,
		})
	}
	return out, nil
}
