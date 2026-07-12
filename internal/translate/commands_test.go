package translate

import "testing"

// TestToAvailableCommands_EmptyVsNonEmpty pins that an empty/nil input
// returns a nil slice (not a non-nil empty slice) and a non-empty input
// is converted element-for-element.
func TestToAvailableCommands_EmptyVsNonEmpty(t *testing.T) {
	if got := toAvailableCommands(nil); got != nil {
		t.Errorf("toAvailableCommands(nil) len=%d = %v, want nil", len(got), got)
	}
	if got := toAvailableCommands([]map[string]any{}); got != nil {
		t.Errorf("toAvailableCommands(empty) len=%d = %v, want nil", len(got), got)
	}
	got := toAvailableCommands([]map[string]any{{"name": "x"}})
	if len(got) != 1 {
		t.Fatalf("toAvailableCommands(1 elem) len = %d, want 1", len(got))
	}
	if got[0].Name != "x" {
		t.Errorf("toAvailableCommands(1 elem)[0].Name = %q, want %q", got[0].Name, "x")
	}
}

// TestToAvailableCommands_MetaExcludesNameAndDescription pins that the
// "name" and "description" keys are lifted into typed fields and never
// leak into the Meta passthrough map, which carries only unknown keys.
func TestToAvailableCommands_MetaExcludesNameAndDescription(t *testing.T) {
	got := toAvailableCommands([]map[string]any{
		{"name": "cmd-x", "description": "desc-d", "extra": "val-e"},
	})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	ac := got[0]
	if ac.Name != "cmd-x" {
		t.Errorf("Name = %q, want %q", ac.Name, "cmd-x")
	}
	if ac.Description != "desc-d" {
		t.Errorf("Description = %q, want %q", ac.Description, "desc-d")
	}
	if v, ok := ac.Meta["extra"]; !ok || v != "val-e" {
		t.Errorf("Meta[extra] = %v (ok=%v), want %q", v, ok, "val-e")
	}
	if _, ok := ac.Meta["name"]; ok {
		t.Errorf("Meta must not contain key %q (it is extracted to Name)", "name")
	}
	if _, ok := ac.Meta["description"]; ok {
		t.Errorf("Meta must not contain key %q (it is extracted to Description)", "description")
	}
	if len(ac.Meta) != 1 {
		t.Errorf("len(Meta) = %d, want 1 (only the extra key)", len(ac.Meta))
	}
}

// TestToAvailableCommands_MetaAssignedOnlyWhenNonEmpty pins that Meta is
// left nil when a command has no extra keys, and populated only when at
// least one unknown key is present.
func TestToAvailableCommands_MetaAssignedOnlyWhenNonEmpty(t *testing.T) {
	got := toAvailableCommands([]map[string]any{{"name": "n", "extra": "e"}})
	if got[0].Meta == nil {
		t.Fatal("ac.Meta = nil, want non-nil when an extra key is present")
	}
	if got[0].Meta["extra"] != "e" {
		t.Errorf("Meta[extra] = %v, want %q", got[0].Meta["extra"], "e")
	}
	got2 := toAvailableCommands([]map[string]any{{"name": "n", "description": "d"}})
	if got2[0].Meta != nil {
		t.Errorf("ac.Meta = %v, want nil when there are no extra keys", got2[0].Meta)
	}
}
