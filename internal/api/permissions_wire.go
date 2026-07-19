package api

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
		Outcome: PermissionOutcomeInner{Outcome: string(StopReasonCancelled)},
	}
}
