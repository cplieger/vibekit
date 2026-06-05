package hub

import (
	"testing"

	"vibekit/internal/api"
)

// FuzzResolveSwitchModel exercises model resolution logic with
// arbitrary model strings. Asserts no panic and consistent semantics.
func FuzzResolveSwitchModel(f *testing.F) {
	f.Add("claude-sonnet", "claude-opus")
	f.Add("claude-sonnet", "")
	f.Add("claude-sonnet", "auto")
	f.Add("", "gpt-4")
	f.Add("model-a", "model-a")

	f.Fuzz(func(t *testing.T, current, requested string) {
		chat := &api.Chat{Model: current}
		p := api.SwitchModelCommand{Model: requested}
		model, isSwitch := resolveSwitchModel(chat, p)

		if requested == "" || requested == "auto" || requested == current {
			if isSwitch {
				t.Errorf("resolveSwitchModel(%q, %q) isSwitch=true, want false", current, requested)
			}
			if model != current {
				t.Errorf("resolveSwitchModel(%q, %q) model=%q, want %q", current, requested, model, current)
			}
		} else {
			if !isSwitch {
				t.Errorf("resolveSwitchModel(%q, %q) isSwitch=false, want true", current, requested)
			}
			if model != requested {
				t.Errorf("resolveSwitchModel(%q, %q) model=%q, want %q", current, requested, model, requested)
			}
		}
	})
}
