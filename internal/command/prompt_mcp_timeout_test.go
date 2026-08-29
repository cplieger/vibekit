package command

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// promptJoin is a LifecycleAccess whose in-flight count a test can join on:
// CmdPrompt acks before its turn runs, so a test asserting on the turn's
// effects waits for the goroutine to deregister first. TurnContext matches the
// production derivation (detached from the request, so the goroutine survives
// the handler's return).
type promptJoin struct{ wg sync.WaitGroup }

func (l *promptJoin) TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(reqCtx))
}
func (l *promptJoin) InflightAdd(delta int) { l.wg.Add(delta) }
func (l *promptJoin) InflightDone()         { l.wg.Done() }

// join blocks until every in-flight turn has deregistered. Safe to call after
// CmdPrompt returned: the registration is synchronous, before the ack.
func (l *promptJoin) join() { l.wg.Wait() }

// stalledMCPDeps is a host whose MCP readiness wait never completes, and whose
// registry can name what it is waiting for.
type stalledMCPDeps struct {
	hostDouble
	bridge  Bridge
	summary MCPPendingSummary
	asked   int
}

func (d *stalledMCPDeps) WaitForReady(context.Context, time.Duration) bool { return false }

func (d *stalledMCPDeps) PendingSummary(context.Context) MCPPendingSummary {
	d.asked++
	return d.summary
}

func (d *stalledMCPDeps) OpenBridge(context.Context, vibekit.ChatID, string) (Bridge, error) {
	return d.bridge, nil
}

// An expired MCP readiness wait names the servers it was waiting for.
//
// The line carried a chat id and nothing else, which is the one fact the operator
// already had. All three causes are distinguishable per server in the registry and
// the enabled-name census sits beside them; what was missing was the join, at the
// one moment it matters. The bucket names travel as separate attributes because
// they want three different actions: nothing heard from, reported broken, waiting
// for a human to authorize.
func TestCmdPrompt_MCPReadinessTimeoutNamesTheServers(t *testing.T) {
	deps := &stalledMCPDeps{
		hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore()),
		bridge:     &recordingBridge{},
		summary: MCPPendingSummary{
			Silent:       []string{"quiet"},
			Failed:       []string{"broken: connection refused"},
			AwaitingAuth: []string{"needsauth"},
		},
	}
	roles := promptRolesOf(deps)
	roles.bridges = deps
	roles.mcp = deps
	join := &promptJoin{}
	roles.lifecycle = join
	logs := captureLogs(t)

	if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
		t.Fatalf("CmdPrompt: %v", err)
	}
	join.join()

	out := logs.String()
	if !strings.Contains(out, "MCP readiness timeout") {
		t.Fatalf("logs = %q, want the readiness timeout recorded", out)
	}
	if deps.asked != 1 {
		t.Errorf("PendingSummary called %d times, want 1: the summary is a diagnostic read once the wait has already expired", deps.asked)
	}
	for _, want := range []string{"quiet", "broken: connection refused", "needsauth"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs = %q, missing %q", out, want)
		}
	}
	for _, attr := range []string{"silent=", "failed=", "awaiting_auth="} {
		if !strings.Contains(out, attr) {
			t.Errorf("logs = %q, missing the %s attribute: the three buckets want three different actions, so one merged list is not the same line", out, attr)
		}
	}
}

// The other half of the same contract: a wait that SUCCEEDS asks nothing and says
// nothing. A summary read on the ordinary path would put a registry read plus a
// config-store read on every prompt, and a line that fires when nothing is wrong
// is how a reader learns to skip the one that matters.
func TestCmdPrompt_AReadyMCPFleetIsSilent(t *testing.T) {
	deps := &readyMCPDeps{
		hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore()),
		bridge:     &recordingBridge{},
	}
	roles := promptRolesOf(deps)
	roles.bridges = deps
	roles.mcp = deps
	join := &promptJoin{}
	roles.lifecycle = join
	logs := captureLogs(t)

	if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
		t.Fatalf("CmdPrompt: %v", err)
	}
	join.join()

	if deps.asked != 0 {
		t.Errorf("PendingSummary called %d times on a ready fleet, want 0", deps.asked)
	}
	if out := logs.String(); strings.Contains(out, "MCP readiness timeout") {
		t.Errorf("logs = %q, must not report a timeout that did not happen", out)
	}
}

// readyMCPDeps is stalledMCPDeps' control: the wait completes.
type readyMCPDeps struct {
	hostDouble
	bridge Bridge
	asked  int
}

func (d *readyMCPDeps) WaitForReady(context.Context, time.Duration) bool { return true }

func (d *readyMCPDeps) PendingSummary(context.Context) MCPPendingSummary {
	d.asked++
	return MCPPendingSummary{}
}

func (d *readyMCPDeps) OpenBridge(context.Context, vibekit.ChatID, string) (Bridge, error) {
	return d.bridge, nil
}
