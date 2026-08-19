package vibekit

import "testing"

func TestWorkingLabelForKind(t *testing.T) {
	tests := []struct {
		kind  ToolKind
		title string
		want  string
	}{
		{ToolKind("execute"), "npm install", "Running npm install"},
		{ToolKind("execute"), "", "Running"},
		{ToolKind("shell"), "go test", "Running go test"},
		{ToolKind("shell"), "", "Running"},
		{ToolKind("read"), "", "Reading"},
		{ToolKind("search"), "", "Searching"},
		{ToolKind("fetch"), "", "Fetching"},
		{ToolKind("edit"), "", "Writing"},
		{ToolKind("write"), "", "Writing"},
		{ToolKind("think"), "", "Reasoning"},
		{ToolKind("delete"), "", "Deleting"},
		{ToolKind("move"), "", "Moving"},
		{ToolKind("command"), "", "Running"},
		{ToolKind("browser"), "", "Browsing"},
		{ToolKind("switch_mode"), "", "Switching"},
		{ToolKind("hook"), "", "Running hook"},
		{ToolKind("other"), "", "Thinking"},
		{ToolKind(""), "", "Thinking"},
		{ToolKind("mcp"), "", "Running"},
		// A title is ignored for non-execute/shell kinds.
		{ToolKind("read"), "ignored", "Reading"},
	}
	for _, tt := range tests {
		got := WorkingLabelForKind(tt.kind, tt.title)
		if got != tt.want {
			t.Errorf("WorkingLabelForKind(%q, %q) = %q, want %q", tt.kind, tt.title, got, tt.want)
		}
	}
}
