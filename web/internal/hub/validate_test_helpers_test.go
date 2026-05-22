package hub

// validRequestID reports whether the given request_id is safe to use
// as an idempotency cache key. Empty is allowed (skips caching). Any
// non-empty value must match the same safe character class as message
// ids so it also log-prints cleanly and can't smuggle newlines into
// slog structured output.
//
// Production code uses internal/command.validRequestID; this copy is
// retained for the fuzz test in command_dispatch_test.go that verifies
// the validation logic independently.
func validRequestID(id string) bool {
	if id == "" {
		return true
	}
	if len(id) > maxRequestIDBytes {
		return false
	}
	return validMessageIDRe.MatchString(id)
}
