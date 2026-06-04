package api

import "testing"

func FuzzWorkingLabelForKind(f *testing.F) {
	f.Add("execute", "bash")
	f.Add("shell", "")
	f.Add("read", "file.go")
	f.Add("search", "")
	f.Add("fetch", "")
	f.Add("edit", "")
	f.Add("write", "")
	f.Add("think", "")
	f.Add("delete", "")
	f.Add("move", "")
	f.Add("command", "")
	f.Add("browser", "")
	f.Add("switch_mode", "")
	f.Add("mcp", "")
	f.Add("hook", "")
	f.Add("other", "")
	f.Add("unknown_kind", "title")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, kind, title string) {
		result := WorkingLabelForKind(ToolKind(kind), title)
		// Must always return a non-empty label.
		if result == "" {
			t.Errorf("WorkingLabelForKind(%q, %q) returned empty", kind, title)
		}
	})
}

func FuzzEffortLevelValid(f *testing.F) {
	f.Add("low")
	f.Add("medium")
	f.Add("high")
	f.Add("xhigh")
	f.Add("max")
	f.Add("")
	f.Add("invalid")
	f.Add("LOW")
	f.Add("Maximum")

	f.Fuzz(func(t *testing.T, s string) {
		e := EffortLevel(s)
		valid := e.Valid()
		// Only the five canonical values should be valid.
		switch s {
		case "low", "medium", "high", "xhigh", "max":
			if !valid {
				t.Errorf("EffortLevel(%q).Valid() = false, want true", s)
			}
		default:
			if valid {
				t.Errorf("EffortLevel(%q).Valid() = true, want false", s)
			}
		}
	})
}
