package translate

import "vibekit/internal/api"

// keyOutcome is the ACP permission-outcome envelope key. Used by both
// PermissionOutcomeSelected and PermissionOutcomeCancelled to build the
// response shape; centralising keeps the wire contract in one place.
const keyOutcome = "outcome"

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
func PermissionOutcomeSelected(optionID string) map[string]any {
	return map[string]any{
		keyOutcome: map[string]any{keyOutcome: "selected", "optionId": optionID},
	}
}

// PermissionOutcomeCancelled builds the ACP permission-outcome response
// for a cancelled/denied permission. Single source of truth for the wire shape.
func PermissionOutcomeCancelled() map[string]any {
	return map[string]any{
		keyOutcome: map[string]any{keyOutcome: api.StopReasonCancelled},
	}
}
