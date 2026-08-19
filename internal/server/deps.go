package server

import (
	"context"

	"github.com/cplieger/vibekit/internal/api"
)

// The interfaces below are declared HERE, at the consumer that calls them,
// rather than in a shared contract package. Each names only the methods this
// package invokes, which is what keeps a test double small enough to be
// obviously correct and stops an unrelated method from widening the contract
// every consumer then has to satisfy.
//
// The width arithmetic is stated per interface. Where a consumer uses ALL of
// what the implementation offers, that is said too — the placement is still
// the point, because a contract nothing else reads has no business in a shared
// package.
//
// UNEXPORTED by default. An interface here is exported only when the
// composition root has to name it, and its doc says so.

// SteeringGenerator generates steering files for kiro-cli.
// *steering.Generator satisfies it.
//
// 2 of the 2 methods the generator offers: this package drives the whole of
// it. Exported because the composition root names it in server.WithSteering
// and main.go asserts *steering.Generator against it.
type SteeringGenerator interface {
	Generate(ctx context.Context)
	CustomPath() string
}

// AccountUsageProvider fetches account/subscription-level usage (plan,
// credits, quota) via the KAS _kiro/account/getUsage request on a live bridge,
// so this package can serve GET /api/account/usage. *hub.Hub satisfies it via
// the utility bridge.
//
// 1 method against a *hub.Hub with well over a hundred: the narrowest possible
// statement of what this endpoint needs. Exported because the composition root
// names it in server.WithAccountUsage.
type AccountUsageProvider interface {
	AccountUsage(ctx context.Context) (*api.AccountUsage, error)
}

// policyProvider READS kiro-cli's native Cedar policy over a live bridge,
// backing GET /api/permissions and the pre-flight simulation at
// POST /api/permissions/explain. *hub.Hub satisfies it.
//
// 2 methods, and deliberately not a third: policy/check is never called (it
// can raise a consent prompt), and the rule WRITER at
// POST /api/permissions/rules needs no provider at all — it is a file write
// KAS hot-reloads, which is why this contract is read-only.
type policyProvider interface {
	PolicyList(ctx context.Context, scope string) ([]api.PolicyRule, error)
	PolicyExplain(ctx context.Context, req api.PolicyExplainRequest) (*api.PolicyExplainResult, error)
}
