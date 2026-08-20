package server

import (
	"context"
	"net/http"

	"github.com/cplieger/vibekit/internal/vibekit"
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

// routeHandler is a component that wires its own routes under a sub-tree of
// /api/*. This package is the ROUTER, so it is the only consumer: the six
// packages that satisfy it (auth, filebrowse, forges, git, mcp's Store and its
// RegistryProxy) implement it, and an implementor is not a consumer. That is
// why one declaration here replaces the shared vibekit.RouteHandler rather than
// eight copies of it.
//
// 1 method, which is the whole of it — there is no narrower statement of "owns
// a mux subset" available.
type routeHandler interface {
	RegisterRoutes(mux *http.ServeMux)
}

// chatEngine is the bridge/SSE hub as this package uses it: it mounts /api/events
// and /api/command, it is the fan-out this package broadcasts a settings change
// through, and it is what the shutdown path drains. *agent.Runtime satisfies it.
//
// 3 methods against a *agent.Runtime that exports well over a hundred. Exported
// methods on the concrete type this package must NOT reach — bridge
// coordination, the utility runtime, the MCP registry, run hosting — are the
// reason the narrow spelling matters here more than anywhere else in the file.
type chatEngine interface {
	routeHandler

	// Broadcast fans one event out to every connected SSE client.
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
	// Shutdown drains in-flight prompts and closes all bridges, bounded by ctx,
	// and reports which wait ran out of budget.
	Shutdown(ctx context.Context) error
}

// pushService is the push surface this package serves: mount the subscription
// endpoints, and write the notification toggles a PATCH /api/settings changed.
// *push.Service satisfies it.
//
// 2 of the 8 methods the push service offers. Sending is not among them — that
// is the runtime's and the PR poller's — and neither is Subscribe/Unsubscribe, which
// the service's own handlers call on themselves.
type pushService interface {
	routeHandler

	// SetPreferences replaces the per-kind notification toggles.
	SetPreferences(prefs map[vibekit.PushKind]bool)
}

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
// so this package can serve GET /api/account/usage. *agent.Runtime satisfies it via
// the utility bridge.
//
// 1 method against a *agent.Runtime with well over a hundred: the narrowest possible
// statement of what this endpoint needs. Exported because the composition root
// names it in server.WithAccountUsage.
type AccountUsageProvider interface {
	AccountUsage(ctx context.Context) (*vibekit.AccountUsage, error)
}

// policyProvider READS kiro-cli's native Cedar policy over a live bridge,// backing GET /api/permissions and the pre-flight simulation at
// POST /api/permissions/explain. *agent.Runtime satisfies it.
//
// 2 methods, and deliberately not a third: policy/check is never called (it
// can raise a consent prompt), and the rule WRITER at
// POST /api/permissions/rules needs no provider at all — it is a file write
// KAS hot-reloads, which is why this contract is read-only.
type policyProvider interface {
	PolicyList(ctx context.Context, scope string) ([]vibekit.PolicyRule, error)
	PolicyExplain(ctx context.Context, req vibekit.PolicyExplainRequest) (*vibekit.PolicyExplainResult, error)
}

// utilityPrompter is AI-backed text generation for the two endpoints this
// package serves with it: explain-an-error and explain-a-diff. *agent.Runtime
// satisfies it over the long-lived utility bridge.
//
// 1 method. internal/git declares its own copy for its own three endpoints
// rather than importing this one — a 1-method contract is cheaper to restate
// than to share, and sharing it is what put it in a runtime package in the first
// place. It is NOT used for chat titles: those come from KAS.
type utilityPrompter interface {
	UtilityPrompt(ctx context.Context, prompt string, effort vibekit.EffortLevel) (string, error)
}
