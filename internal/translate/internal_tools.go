package translate

// Internal engine bookkeeping announced as tool calls. KAS emits these through
// its deterministicToolCalls channel during session machinery, not as agent
// work, and its own TUI never renders them. The create frame is dropped before
// it can open a turn; an update without an open turn is dropped by lookup.
func isInternalTool(toolID string) bool {
	return toolID == "fetch_cloud_config"
}
