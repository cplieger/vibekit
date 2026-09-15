package push

import (
	"testing"

	"github.com/cplieger/vibekit/internal/settings"
)

// The registry's DefaultOn and the effective view must answer the same thing for
// every keyed kind, because the two are read by different halves of one feature:
// the send gate falls back to DefaultOn for a key config.json does not carry, and
// GET /api/settings answers EffectiveDefaults for the same absence. A third copy
// of a polarity, or a kind added to one side only, renders the toggle in one state
// while the server delivers in the other.
func TestKindRegistry_DefaultsMatchEffectiveDefaults(t *testing.T) {
	eff := settings.EffectiveDefaults()
	byKey := map[string]bool{
		settings.KeyNotifyAgentFinished: eff.NotifyAgentFinished,
		settings.KeyNotifyPRStatus:      eff.NotifyPRStatus,
		settings.KeyNotifyRunOutcome:    eff.NotifyRunOutcome,
	}

	var keyed int
	for _, k := range Kinds() {
		if k.SettingsKey == "" {
			continue // a floor has no settings key, so there is nothing to agree with
		}
		keyed++
		want, ok := byKey[k.SettingsKey]
		if !ok {
			t.Errorf("kind %s carries settings key %q, which no field of EffectiveSettings answers for: "+
				"the registry gained a keyed kind the effective view does not resolve",
				k.Kind, k.SettingsKey)
			continue
		}
		if k.DefaultOn != want {
			t.Errorf("kindRegistry[%s].DefaultOn = %v, EffectiveDefaults().%s = %v: "+
				"the send gate and the rendered toggle would disagree for every install whose "+
				"config.json has never carried %q",
				k.Kind, k.DefaultOn, k.SettingsKey, want, k.SettingsKey)
		}
	}
	if keyed != len(byKey) {
		t.Errorf("keyed kinds = %d, want %d: a kind was added to EffectiveSettings without a registry row, "+
			"so its switch would be settable and never consulted", keyed, len(byKey))
	}
}
