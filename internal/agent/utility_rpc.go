// Stateless KAS RPC wrappers over the utility session. Each Call runs outside any
// lock, so none waits behind the text-generation agent's turn mutex.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// rawCall is the shared RPC core: acquire → Call → resetIf-on-error. params
// receives the leased bridge; label prefixes error messages.
func (us *utilitySession) rawCall(ctx context.Context, label, method string, params func(bridge acpSession) map[string]any) (json.RawMessage, error) {
	raw, _, err := us.rawCallAt(ctx, label, method, params)
	return raw, err
}

// rawCallAt is rawCall plus the read-loop position the response arrived at and
// the session it belongs to. Only a caller ordering a local decision against
// frames still in flight needs it: KAS replays a step's session as
// notifications PRECEDING the load result, so the result alone says nothing.
func (us *utilitySession) rawCallAt(ctx context.Context, label, method string, params func(bridge acpSession) map[string]any) (json.RawMessage, drainPoint, error) {
	lease, err := us.acquire(ctx)
	if err != nil {
		return nil, drainPoint{}, err
	}
	at := drainPoint{gen: lease.gen}
	resp, seq, err := lease.bridge.CallAt(ctx, method, params(lease.bridge))
	at.seq = seq
	if err != nil {
		// A CALLER walking away is not evidence the bridge is unhealthy: startLocked
		// spawns on us.shutdownCtx, so resetting here would kill the session that just
		// started. A DEADLINE still resets — that one may genuinely be wedged.
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

// callerParams adapts a fixed caller-supplied parameter map (no session id) to
// rawCall's params signature.
func callerParams(params map[string]any) func(acpSession) map[string]any {
	return func(acpSession) map[string]any { return params }
}

// scopedParams builds session-scoped params (session id + extras) from the lease.
func scopedParams(extra map[string]any) func(acpSession) map[string]any {
	return func(bridge acpSession) map[string]any { return utilitySessionParams(bridge, extra) }
}

// codeIntelligenceInit issues _kiro/codeIntelligence subcommand=init: KAS detects
// the workspace's languages and writes .kiro/settings/lsp.json, leaving an
// existing config untouched. Returns KAS's human-readable message.
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

// accountUsageRaw issues _kiro/account/getUsage and returns the raw JSON-RPC
// result. Needs no model or tools, only a live acp session with valid auth.
func (us *utilitySession) accountUsageRaw(ctx context.Context) (json.RawMessage, error) {
	return us.rawCall(ctx, "account usage call", methodKiroGetUsage, scopedParams(nil))
}

// knowledgeRaw issues a _kiro/knowledge subcommand request and returns the raw
// JSON-RPC result. It does NOT inject the session id: omitting sessionId targets
// the workspace-global default store.
func (us *utilitySession) knowledgeRaw(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, "knowledge call", methodKiroKnowledge, callerParams(params))
}

// policyRaw issues a _kiro/permissions/* request and returns the raw JSON-RPC
// result. Injects the utility session id, since these requests are session-scoped.
// Read-only: pure policy inspection, no consent prompt.
func (us *utilitySession) policyRaw(ctx context.Context, method string, extra map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, fmt.Sprintf("policy call %s", method), method, scopedParams(extra))
}

// hooksRaw issues a non-session _kiro/hooks request (list / setEnabled).
func (us *utilitySession) hooksRaw(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return us.rawCall(ctx, fmt.Sprintf("hooks call %s", method), method, callerParams(params))
}

// configTemplateRaw issues the session-less _kiro/config/template request
// (kiro-cli 2.14+) and returns the raw JSON-RPC result. No sessionId: any agent
// can answer the template from its own registries.
func (us *utilitySession) configTemplateRaw(ctx context.Context) (json.RawMessage, error) {
	return us.rawCall(ctx, "config template call", methodKiroConfigTemplate, callerParams(nil))
}

// No hook TRIGGER wrapper: `_kiro/hooks/triggerHook` runs `sh -c` on a command a
// hook file names.
