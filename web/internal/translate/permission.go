package translate

import "vibekit/internal/api"

// PermissionOutcome is the typed wire shape for ACP permission-outcome
// responses. Replaces anonymous map[string]any for compile-time safety.
type PermissionOutcome struct {
	Outcome PermissionOutcomeInner `json:"outcome"`
}

// PermissionOutcomeInner is the nested outcome payload.
type PermissionOutcomeInner struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// FindAllowOnce returns the option_id of the first "allow_once" option,
// or "" if none exists.
func FindAllowOnce(options []api.PermissionOption) string {
	for _, opt := range options {
		if opt.Kind == api.PermissionKindAllowOnce || opt.OptionID == api.PermissionKindAllowOnce {
			return opt.OptionID
		}
	}
	return ""
}

// PermissionOutcomeSelected builds the ACP permission-outcome response
// for a selected option. Single source of truth for the wire shape.
func PermissionOutcomeSelected(optionID string) *PermissionOutcome {
	return &PermissionOutcome{
		Outcome: PermissionOutcomeInner{Outcome: "selected", OptionID: optionID},
	}
}

// PermissionOutcomeCancelled builds the ACP permission-outcome response
// for a cancelled/denied permission. Single source of truth for the wire shape.
func PermissionOutcomeCancelled() *PermissionOutcome {
	return &PermissionOutcome{
		Outcome: PermissionOutcomeInner{Outcome: string(api.StopReasonCancelled)},
	}
}
