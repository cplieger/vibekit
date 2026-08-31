package vibekit

import (
	"regexp"
	"testing"
)

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
	f.Add("none")
	f.Add("")
	f.Add("LOW")
	f.Add("Maximum")
	f.Add("9high")
	f.Add("-high")
	f.Add("x high")
	f.Add("extra-high")
	f.Add("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") // 33 bytes: one past the cap

	// Independent statement of the shape rule (Valid is a hand loop; this is
	// the differential oracle). The vocabulary itself is per model and
	// upstream-owned, so validity is a SHAPE question, not a member list —
	// gpt-luna's "none" tier is the case the old closed set rejected.
	shape := regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

	f.Fuzz(func(t *testing.T, s string) {
		if got, want := EffortLevel(s).Valid(), shape.MatchString(s); got != want {
			t.Errorf("EffortLevel(%q).Valid() = %v, want %v", s, got, want)
		}
	})
}
