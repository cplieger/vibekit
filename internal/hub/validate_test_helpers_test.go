package hub

import "vibekit/internal/api"

// validRequestID reports whether the given request_id is safe to use
// as an idempotency cache key. Delegates to api.ValidRequestID — the
// single source of truth.
//
// Production code uses internal/command.validRequestID; this copy is
// retained for the fuzz test in command_dispatch_test.go that verifies
// the validation logic independently.
func validRequestID(id string) bool {
	return api.ValidRequestID(id)
}
