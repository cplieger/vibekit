// Code-intelligence activation: one idempotent hook that initializes
// kiro-cli's (KAS's) native code intelligence for THE workspace.
//
// Every bridge already opts its sessions into the code tool via the
// initialize handshake, which gives the agent tree-sitter operations
// unconditionally. The LSP half needs a one-time per-workspace
// activation: .kiro/settings/lsp.json under the work dir, written by KAS's
// `init` subcommand; this is what chat sessions read to spawn language
// servers on demand — vibekit never manages server processes itself.
//
// EnsureCodeIntelligence runs init exactly when useful: the config file
// does not exist yet AND at least one lsp-marked tool is enabled and
// installed. Callers fire it at boot and on lsp-tool install success, so
// enabling a language server lights up code intelligence with no restart.
// KAS's init never rewrites an existing config, so a stale language set is
// refreshed by deleting the file — the next trigger re-initializes.

package agent

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// codeIntelInitBudget bounds one init call end to end, including a lazy
// utility-session start (subprocess spawn + auth callback + session/new).
const codeIntelInitBudget = 2 * time.Minute

// SetCodeIntelligence wires the activation inputs: lspConfigPath is the
// workspace's .kiro/settings/lsp.json, gate reports whether any
// lsp-marked tool is enabled and installed. Called once at composition;
// both empty/nil in tests that don't exercise activation.
func (rt *Runtime) SetCodeIntelligence(lspConfigPath string, gate func() bool) {
	rt.ciPath = lspConfigPath
	rt.ciGate = gate
}

// EnsureCodeIntelligence initializes workspace code intelligence when
// needed (see the file comment). Safe to call from any goroutine, any
// number of times; concurrent callers coalesce on an in-flight guard
// that re-arms on failure so a transient error retries on the next
// trigger. Never blocks the caller beyond the stat + gate check when
// nothing is to do.
func (rt *Runtime) EnsureCodeIntelligence(ctx context.Context) {
	if rt.ciPath == "" || rt.ciGate == nil {
		return // not wired (tests, or activation disabled)
	}
	if _, err := os.Stat(rt.ciPath); err == nil {
		return // workspace already initialized
	}
	if !rt.ciGate() {
		return // no enabled+installed language server: init would find nothing
	}
	if !rt.ciBusy.CompareAndSwap(false, true) {
		return // an init is already in flight
	}
	defer rt.ciBusy.Store(false)
	ctx, cancel := context.WithTimeout(ctx, codeIntelInitBudget)
	defer cancel()
	msg, err := rt.utility.get().session.codeIntelligenceInit(ctx)
	if err != nil {
		slog.Warn("code intelligence init failed; will retry on the next trigger",
			"error", err)
		return
	}
	slog.Info("code intelligence initialized", "detail", msg, "config", rt.ciPath)
}
