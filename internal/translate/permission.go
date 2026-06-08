package translate

import "github.com/cplieger/vibekit/internal/api"

// Re-export permission wire types from api for backward compatibility
// within this package. External consumers should use api directly.

// FindAllowOnce delegates to api.FindAllowOnce.
func FindAllowOnce(options []api.PermissionOption) string {
	return api.FindAllowOnce(options)
}

// PermissionOutcomeSelected delegates to api.PermissionOutcomeSelected.
func PermissionOutcomeSelected(optionID string) *api.PermissionOutcome {
	return api.PermissionOutcomeSelected(optionID)
}

// PermissionOutcomeCancelled delegates to api.PermissionOutcomeCancelled.
func PermissionOutcomeCancelled() *api.PermissionOutcome {
	return api.PermissionOutcomeCancelled()
}
