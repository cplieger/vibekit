package api

// PermissionOutcome is the typed wire shape for ACP permission-outcome
// responses. Replaces anonymous map[string]any for compile-time safety.
type PermissionOutcome struct {
	Meta    *PermissionOutcomeMeta `json:"_meta,omitempty"`
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

// PermissionOutcomeWithFileDecisions builds a permission response that also
// carries per-file decisions for a TURN APPROVAL.
//
// The decisions ride `_meta.kiro.fileDecisions` on the ordinary permission
// reply — a map from KAS's pending-action id to accept/reject. KAS applies the
// accepted ids and calls restorePendingChanges on the rest, so **an id omitted
// from the map counts as a REJECT**, not as unspecified: a partial map silently
// discards the files it forgot. Callers must send a decision for every file they
// were offered.
func PermissionOutcomeWithFileDecisions(optionID string, decisions map[string]bool) *PermissionOutcome {
	out := PermissionOutcomeSelected(optionID)
	if len(decisions) > 0 {
		out.Meta = &PermissionOutcomeMeta{Kiro: PermissionOutcomeKiro{FileDecisions: decisions}}
	}
	return out
}

// PermissionOutcomeMeta is the `_meta` envelope on a permission reply.
type PermissionOutcomeMeta struct {
	Kiro PermissionOutcomeKiro `json:"kiro"`
}

// PermissionOutcomeKiro is the vendor block inside that envelope.
type PermissionOutcomeKiro struct {
	FileDecisions map[string]bool `json:"fileDecisions,omitempty"`
}

// PermissionOutcomeCancelled builds the ACP permission-outcome response
// for a cancelled/denied permission. Single source of truth for the wire shape.
func PermissionOutcomeCancelled() *PermissionOutcome {
	return &PermissionOutcome{
		Outcome: PermissionOutcomeInner{Outcome: string(StopReasonCancelled)},
	}
}
