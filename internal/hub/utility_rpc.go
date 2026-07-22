// Stateless KAS RPC wrappers over the utility session (the second of the
// utility runtime's two roles; see utility_session.go for the split).
//
// Each wrapper is an instant read: acquire the session (lazily starting
// it), issue one Call OUTSIDE any lock, and reset the session on transport
// failure so the next caller restarts it. None of these wait behind the
// text-generation agent's turn mutex — a specs-board or permissions read
// returns in milliseconds even while a generation turn is in flight.

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cplieger/vibekit/internal/api"
)

// rawCall is the shared RPC core: acquire → Call → resetIf-on-error.
// params receives the leased bridge so session-scoped wrappers can inject
// the live session id. label prefixes error messages ("account usage
// call: ...").
func (us *utilitySession) rawCall(ctx context.Context, label, method string, params func(bridge api.ACPBridge) map[string]any) (json.RawMessage, error) {
	lease, err := us.acquire(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := lease.bridge.Call(ctx, method, params(lease.bridge))
	if err != nil {
		// Bridge may be dead; reset (if still this generation) so the
		// next call restarts it.
		us.resetIf(lease.gen)
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	if resp == nil {
		return nil, errors.New(label + ": nil response")
	}
	return resp.Result, nil
}

// callerParams adapts a fixed, caller-supplied parameter map (no session
// id) to rawCall's params signature.
func callerParams(params map[string]any) func(api.ACPBridge) map[string]any {
	return func(api.ACPBridge) map[string]any { return params }
}

// scopedParams builds session-scoped params (session id + extras) from the
// leased bridge.
func scopedParams(extra map[string]any) func(api.ACPBridge) map[string]any {
	return func(bridge api.ACPBridge) map[string]any { return utilitySessionParams(bridge, extra) }
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

// specTaskStatusesRaw issues a _kiro/spec/getTaskStatuses request and
// returns the raw JSON-RPC result. No sessionId: getTaskStatuses is a
// stateless read that takes workspacePaths + tasksFilePath in params and
// needs no session context (verified live). The Specs board works with no
// chat open because this runs on the always-available utility session.
func (us *utilitySession) specTaskStatusesRaw(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, "spec getTaskStatuses call", methodV3SpecGetTaskStatuses, callerParams(params))
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

// triggerRunCommandHook triggers a runCommand hook and returns the captured
// command output. It sets expectingHookExec around the triggerHook Call so
// the executeHook callback (which fires DURING the Call, handled by the
// forward goroutine) is allowed to run the command; the result lands in
// lastHookRun. approved:true — the user's "Run now" click is the consent,
// so KAS skips its own per-command approval round-trip. hookTriggerMu
// serializes concurrent triggers, whose shared expectingHookExec +
// lastHookRun would otherwise cross outputs.
func (us *utilitySession) triggerRunCommandHook(ctx context.Context, hookID, hookName, command string) (hookRunResult, error) {
	us.hookTriggerMu.Lock()
	defer us.hookTriggerMu.Unlock()

	us.lastHookRun.Store(nil)
	us.expectingHookExec.Store(true)
	defer us.expectingHookExec.Store(false)

	raw, err := us.rawCall(ctx, "hooks trigger", methodKiroHooksTriggerHook, scopedParams(map[string]any{
		"hookId":         hookID,
		"hookName":       hookName,
		"hookActionType": actionRunCommand,
		"command":        command,
		"approved":       true,
	}))
	if err != nil {
		return hookRunResult{}, err
	}
	// A success:false reply (e.g. session gone) is a real failure; a
	// success:true with a non-zero command exit is a valid "ran, failed"
	// outcome carried in lastHookRun. A nil lastHookRun (KAS never issued
	// the executeHook callback, e.g. a hook disabled server-side) yields
	// the zero result.
	if res := parseHookResult(raw); !res.Success {
		return hookRunResult{}, hookTriggerError(res)
	}
	if run := us.lastHookRun.Load(); run != nil {
		return *run, nil
	}
	return hookRunResult{}, nil
}
