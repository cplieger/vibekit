package translate

// Slash-command catalog helper. On v3 (KAS) the catalog arrives via the
// available_commands_update session/update sub-kind (HandleAvailableCommandsUpdate
// in v3_updates.go); this file retains only the shared wire→domain mapper.

import (
	"github.com/cplieger/vibekit/internal/api"
)

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
		if name, ok := raw[api.JSONKeyName].(string); ok {
			ac.Name = name
		}
		if desc, ok := raw["description"].(string); ok {
			ac.Description = desc
		}
		ac.Meta = commandMeta(raw)
		out = append(out, ac)
	}
	return out
}

// commandMeta collects every key except the typed "name"/"description"
// fields into a passthrough map (nil when there are none), keeping the
// wire shape lossless for forward compatibility.
func commandMeta(raw map[string]any) map[string]any {
	var meta map[string]any
	for k, v := range raw {
		if k == api.JSONKeyName || k == "description" {
			continue
		}
		if meta == nil {
			meta = make(map[string]any, len(raw))
		}
		meta[k] = v
	}
	return meta
}
