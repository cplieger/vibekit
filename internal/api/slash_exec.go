package api

import "context"

// slashCaller is the minimal bridge surface ExecuteSlashCommand needs:
// the ability to issue a JSON-RPC Call. Both ACPBridge (hub) and
// CommandBridge (command handlers) satisfy it structurally, so the
// helper works from either package without coupling to the wider
// interface.
type slashCaller interface {
	Call(ctx context.Context, method string, params any) (*RPCResponse, error)
}

// ExecuteSlashCommand dispatches a kiro-cli TUI slash command (e.g.
// "effort", "model") to a running ACP bridge via the
// _kiro.dev/commands/execute extension method.
//
// It centralizes the nested {command:{command,args}} payload shape and
// the MethodCommandsExecute method name so call sites don't re-hand-roll
// the wire format with drifting key literals. The returned *RPCResponse
// is the raw command result; most callers only care about the error.
//
// This is the mid-session lever (e.g. CmdSetEffort). For the *initial*
// effort of a new session, prefer the StartOpts.Effort launch flag
// (kiro-cli >=2.6 `acp --effort`) over a post-start dispatch.
func ExecuteSlashCommand(ctx context.Context, b slashCaller, name string, args ...string) (*RPCResponse, error) {
	// Normalize to a non-nil slice so the payload marshals as [] rather
	// than null when a command takes no arguments.
	if args == nil {
		args = []string{}
	}
	return b.Call(ctx, MethodCommandsExecute, map[string]any{
		"command": map[string]any{
			"command": name,
			"args":    args,
		},
	})
}
