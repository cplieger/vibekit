package translate

import (
	"encoding/json"
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// unmarshalParams decodes msg.Params into T. Returns the decoded value
// and true on success. On failure, logs at Debug level and returns the
// zero value with false. Centralises the repeated decode+log pattern
// across all translate handlers.
func unmarshalParams[T any](msg *vibekit.RPCResponse, method string) (T, bool) {
	var p T
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		slog.Debug("translate: unmarshal failed", "method", method, "error", err)
		return p, false
	}
	return p, true
}
