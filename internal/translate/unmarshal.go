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
	p, err := decodeParams[T](msg)
	if err != nil {
		slog.Debug("translate: unmarshal failed", "method", method, "error", err)
		return p, false
	}
	return p, true
}

// decodeParams decodes msg.Params into T and hands the decode error back.
//
// For the handlers that ANSWER a failed decode rather than dropping the frame:
// their refusal is logged at Warn and has to carry the reason, where a
// notification handler's drop is a Debug line the helper above emits for it.
func decodeParams[T any](msg *vibekit.RPCResponse) (T, error) {
	var p T
	err := json.Unmarshal(msg.Params, &p)
	return p, err
}
