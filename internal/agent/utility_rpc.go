// Stateless KAS RPC wrappers over the utility session (the second of the
// utility runtime's two roles; see utility_session.go for the split).
//
// Each wrapper is an instant read: acquire the session (lazily starting
// it), issue one Call OUTSIDE any lock, and reset the session on transport
// failure so the next caller restarts it. None of these wait behind the
// text-generation agent's turn mutex — a specs-board or permissions read
// returns in milliseconds even while a generation turn is in flight.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// rawCall is the shared RPC core: acquire → Call → resetIf-on-error.
// params receives the leased bridge so session-scoped wrappers can inject
// the live session id. label prefixes error messages ("account usage
// call: ...").
func (us *utilitySession) rawCall(ctx context.Context, label, method string, params func(bridge acpSession) map[string]any) (json.RawMessage, error) {
	raw, _, err := us.rawCallAt(ctx, label, method, params)
	return raw, err
}

// rawCallAt is rawCall plus the read-loop position the response arrived at and the
// session attachment it belongs to.
//
// Only a caller that has to ORDER a local decision against the frames still in
// flight needs it, which is bridge.CallAt's own rule one layer up. The step
// transcript read is the one caller: KAS replays the step's session as
// notifications that PRECEDE the load result, so the result alone says nothing
// about whether the forward goroutine has folded them.
func (us *utilitySession) rawCallAt(ctx context.Context, label, method string, params func(bridge acpSession) map[string]any) (json.RawMessage, drainPoint, error) {
	lease, err := us.acquire(ctx)
	if err != nil {
		return nil, drainPoint{}, err
	}
	at := drainPoint{gen: lease.gen}
	resp, seq, err := lease.bridge.CallAt(ctx, method, params(lease.bridge))
	at.seq = seq
	if err != nil {
		// A CALLER walking away is not evidence the bridge is unhealthy, so it
		// must not reset. startLocked deliberately spawns on us.shutdownCtx, so
		// an abort mid-handshake leaves a live session behind — and resetting on
		// the request ctx's own cancellation stopped exactly the session that
		// had just finished starting, making every later utility-backed
		// endpoint pay the respawn. A DEADLINE still resets: a session that
		// spent the whole budget without answering may genuinely be wedged.
		if !errors.Is(ctx.Err(), context.Canceled) {
			us.resetIf(lease.gen)
		}
		return nil, at, fmt.Errorf("%s: %w", label, err)
	}
	if resp == nil {
		return nil, at, errors.New(label + ": nil response")
	}
	return resp.Result, at, nil
}

// callerParams adapts a fixed, caller-supplied parameter map (no session
// id) to rawCall's params signature.
func callerParams(params map[string]any) func(acpSession) map[string]any {
	return func(acpSession) map[string]any { return params }
}

// scopedParams builds session-scoped params (session id + extras) from the
// leased bridge.
func scopedParams(extra map[string]any) func(acpSession) map[string]any {
	return func(bridge acpSession) map[string]any { return utilitySessionParams(bridge, extra) }
}

// codeIntelligenceInit issues _kiro/codeIntelligence subcommand=init on
// the utility session: KAS detects the workspace's languages, writes
// .kiro/settings/lsp.json (idempotent — an existing config is left
// untouched), and starts the detected languages' servers in ITS process.
// The file is the durable output; chat sessions read it and ensure their
// own server processes on demand. Returns KAS's human-readable message.
func (us *utilitySession) codeIntelligenceInit(ctx context.Context) (string, error) {
	raw, err := us.rawCall(ctx, "code intelligence init", methodKiroCodeIntel,
		scopedParams(map[string]any{"subcommand": "init"}))
	if err != nil {
		return "", err
	}
	var out struct {
		Message string `json:"message"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("code intelligence init: decode: %w", err)
	}
	if !out.Success {
		return "", fmt.Errorf("code intelligence init: %s", out.Message)
	}
	return out.Message, nil
}

// accountUsageRaw issues the account-level _kiro/account/getUsage request
// and returns the raw JSON-RPC result. getUsage is a bare C→A request that
// needs no model or tools — just a live acp session with valid auth (the
// getAccessToken callback supplies the profileArn getUsage requires).
func (us *utilitySession) accountUsageRaw(ctx context.Context) (json.RawMessage, error) {
	return us.rawCall(ctx, "account usage call", methodKiroGetUsage, scopedParams(nil))
}

// knowledgeRaw issues a _kiro/knowledge subcommand request and returns the
// raw JSON-RPC result. It does NOT inject the session id: omitting
// sessionId targets the workspace-global default store (a builtin-agent
// session would resolve to the same store, but omitting it is
// unconditional and matches the "knowledge is workspace-global" model).
// The knowledge subsystem embeds its own ONNX/MiniLM engine, so this needs
// only a live acp session with valid auth — no model or tools.
func (us *utilitySession) knowledgeRaw(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, "knowledge call", methodKiroKnowledge, callerParams(params))
}

// policyRaw issues a _kiro/permissions/* request and returns the raw
// JSON-RPC result. Injects the utility session id (these requests are
// session-scoped: list reads the session's resolved policy; explain
// simulates against it). The kiro/user/workspace scopes are
// workspace-global so the utility session's view is representative; the
// agent scope reflects the utility session's (default) agent. Read-only —
// pure policy inspection, no consent prompt (explain is a pure simulation;
// list is synchronous).
func (us *utilitySession) policyRaw(ctx context.Context, method string, extra map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, fmt.Sprintf("policy call %s", method), method, scopedParams(extra))
}

// hooksRaw issues a non-session _kiro/hooks request (list / setEnabled)
// and returns the raw JSON-RPC result. Mirrors knowledgeRaw.
func (us *utilitySession) hooksRaw(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, fmt.Sprintf("hooks call %s", method), method, callerParams(params))
}

// configTemplateRaw issues the session-less _kiro/config/template request
// (kiro-cli 2.14+) and returns the raw JSON-RPC result. No sessionId: the
// template is answerable by any agent from its own registries; the model
// list it reads is populated by the governance refresh our utility
// session's own session/new already triggered.
func (us *utilitySession) configTemplateRaw(ctx context.Context) (json.RawMessage, error) {
	return us.rawCall(ctx, "config template call", methodKiroConfigTemplate, callerParams(nil))
}

// No hook TRIGGER wrapper exists here: `_kiro/hooks/triggerHook` made vibekit
// run `sh -c` on a command a hook file specifies. Do not re-add it.
