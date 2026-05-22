package api

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
		{ToolKind("read"), "", "Reading"},
		{ToolKind("search"), "", "Searching"},
		{ToolKind("fetch"), "", "Fetching"},
		{ToolKind("edit"), "", "Writing"},
		{ToolKind("think"), "", "Reasoning"},
		{ToolKind("other"), "", "Thinking"},
		{ToolKind(""), "", "Thinking"},
		{ToolKind("mcp"), "", "Thinking"},
	}
	for _, tt := range tests {
		got := WorkingLabelForKind(tt.kind, tt.title)
		if got != tt.want {
			t.Errorf("WorkingLabelForKind(%q, %q) = %q, want %q", tt.kind, tt.title, got, tt.want)
		}
	}
}
