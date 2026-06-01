package api

// ACP JSON-RPC wire types: request/response/notification envelopes for
// communication with kiro-cli over the ACP protocol.

import (
	"encoding/json"
	"errors"
)

// RPCRequest is an outbound JSON-RPC 2.0 request sent to kiro-cli.
type RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Params  any    `json:"params,omitempty"`
	Method  string `json:"method"`
	ID      int64  `json:"id"`
}

// RPCResponse is a JSON-RPC 2.0 message from kiro-cli. A populated ID with
// Result or Error is a response; an empty ID with Method and Params is a
// server-sent notification.
type RPCResponse struct {
	Error   *RPCError       `json:"error,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// Error makes RPCError implement the error interface by returning the
// server-provided message verbatim. Callers that need the code use
// errors.As to recover the concrete *RPCError.
func (e *RPCError) Error() string {
	return e.Message
}

// RPCNotification is an outbound JSON-RPC 2.0 notification (no id, no
// response expected). Used by Bridge.Notify instead of ad-hoc
// map[string]any construction.
type RPCNotification struct {
	Params  any    `json:"params,omitempty"`
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
}

// RPCResponseOut is an outbound JSON-RPC 2.0 response to a request
// received from kiro-cli. Used by Bridge.Respond instead of ad-hoc
// map[string]any construction.
type RPCResponseOut struct {
	Result  any          `json:"result,omitempty"`
	Error   *RPCErrorOut `json:"error,omitempty"`
	JSONRPC string       `json:"jsonrpc"`
	ID      int64        `json:"id"`
}

// RPCErrorOut is the error object in an outbound JSON-RPC response.
type RPCErrorOut struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// ErrNotIdle is the sentinel for "session not idle" errors from
// kiro-cli. Wraps the raw RPC error when the message contains "not
// idle" but the code isn't RPCCodeNotIdle, unifying the detection
// path for callers that need errors.Is classification.
var ErrNotIdle = errors.New("session not idle")

// TransportError wraps bridge-level transport failures (pipe closed,
// write timeout, process exited) with explicit retryability semantics.
// Callers use errors.As to classify without substring matching.
type TransportError struct {
	Err       error
	Retryable bool
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }
