package translate

import "fmt"

// browserIncompatibleCommands lists slash commands that rely on
// terminal-local affordances or duplicate vibekit's own UX.
var browserIncompatibleCommands = map[string]struct{}{
	"/paste": {}, // clipboard image paste via OSC; we upload via DnD
	"/reply": {}, // opens $EDITOR
	"/quit":  {}, // nothing to quit in a browser
	"/chat":  {}, // vibekit owns chat lifecycle
}

// FilterCommands removes browser-incompatible commands and enriches
// /tools and /mcp descriptions with live counts.
func FilterCommands(commands []map[string]any, toolsCount, mcpRunning, mcpTotal int) []map[string]any {
	out := make([]map[string]any, 0, len(commands))
	for _, c := range commands {
		name, _ := c["name"].(string)
		if _, drop := browserIncompatibleCommands[name]; drop {
			continue
		}
		if name == "/tools" && toolsCount > 0 {
			if desc, ok := c["description"].(string); ok {
				c["description"] = fmt.Sprintf("%s (%d available)", desc, toolsCount)
			}
		}
		if name == "/mcp" && mcpTotal > 0 {
			if desc, ok := c["description"].(string); ok {
				c["description"] = fmt.Sprintf("%s (%d/%d running)", desc, mcpRunning, mcpTotal)
			}
		}
		out = append(out, c)
	}
	return out
}
