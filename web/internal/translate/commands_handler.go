package translate

// _kiro.dev/commands/available handler.

import (
	"context"
	"encoding/json"

	"vibekit/internal/api"
)

// HandleCommandsAvailable processes the commands/available notification.
func (t *Translator) HandleCommandsAvailable(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
	var p struct {
		Commands   []map[string]any `json:"commands"`
		Prompts    []map[string]any `json:"prompts"`
		Tools      []any            `json:"tools"`
		MCPServers []struct {
			Name   string   `json:"name"`
			Status string   `json:"status"`
			Tools  []string `json:"tools"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return
	}

	t.deps.Broadcast(ctx, api.NewEvent(api.EventCommandsUpdated, chatID, api.CommandsUpdatedPayload{
		Commands: toAvailableCommands(p.Commands),
		Prompts:  toAvailableCommands(p.Prompts),
	}))
	t.deps.MCPSignalReady()

	// Persist per-server tool names so the UI can show suggestions in
	// the disabled-tools section even when the server is disconnected.
	for _, s := range p.MCPServers {
		if len(s.Tools) > 0 {
			t.deps.MCPSetKnownTools(s.Name, s.Tools)
		}
	}
}

// toAvailableCommands converts the opaque map-of-strings shape kiro-cli
// emits into typed AvailableCommand records. Unknown keys flow through
// the Meta map so the wire format keeps forward compatibility.
func toAvailableCommands(in []map[string]any) []api.AvailableCommand {
	if len(in) == 0 {
		return nil
	}
	out := make([]api.AvailableCommand, 0, len(in))
	for _, raw := range in {
		ac := api.AvailableCommand{}
		if name, ok := raw[jsonFieldName].(string); ok {
			ac.Name = name
		}
		if desc, ok := raw["description"].(string); ok {
			ac.Description = desc
		}
		// Stash any other fields under Meta so the wire shape is lossless.
		var meta map[string]any
		for k, v := range raw {
			if k == jsonFieldName || k == "description" {
				continue
			}
			if meta == nil {
				meta = make(map[string]any, len(raw))
			}
			meta[k] = v
		}
		if len(meta) > 0 {
			ac.Meta = meta
		}
		out = append(out, ac)
	}
	return out
}
