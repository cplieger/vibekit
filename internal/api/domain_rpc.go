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
	// Data is the optional member JSON-RPC allows for implementation-defined
	// detail, and on KAS it is where most errors actually SAY something.
	//
	// Counted over every engine-emitted frame in the wire logs: 127 `-32603`
	// errors set Message to the literal "Internal error" and put the real text
	// in Data — either `{"details": "…"}` or a Zod issue array — while the 6
	// `-32602` errors put it in Message and carry no Data at all.
	//
	// The 4 `-32000` errors are NOT in that second group, and the earlier claim
	// that they carry no Data was wrong: -32000 is KAS's own application code
	// and it does attach Data (the throttle case is the one vibekit reads, in
	// command/prompt.go's promptFailureReason). Treat -32000 as "check both".
	// So the two fields are not redundant and neither is primary: dropping Data
	// (which this struct did) loses the cause of every internal error, and
	// dropping Message would lose every parameter-validation message.
	//
	// Kept as raw JSON because the two shapes have nothing in common;
	// workflow.Details is the one place that unwraps both.
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
	Code    int             `json:"code"`
}

// Error makes RPCError implement the error interface by returning the
// server-provided message verbatim. Callers that need the code use
// errors.As to recover the concrete *RPCError.
func (e *RPCError) Error() string {
	return e.Message
}

// ErrorData exposes the raw `error.data` member. An accessor rather than direct
// field access so a decoder can unwrap it without importing this package's
// concrete type — see workflow.Details.
func (e *RPCError) ErrorData() json.RawMessage {
	return e.Data
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

// ErrBridgeExited is the sentinel a Call returns (wrapped in a TransportError)
// when the ACP subprocess died with the request still pending. Exported for the
// same reason as ErrNotIdle: a caller has to be able to tell this apart from a
// transient write failure without substring matching, because the two want
// OPPOSITE actions. A write that failed once may succeed on a retry; a dead
// bridge cannot, because the readLoop has closed its done channel permanently,
// so every retry fails instantly and the only thing the attempts buy is dead
// wall-clock time before the same error surfaces.
var ErrBridgeExited = errors.New("ACP bridge exited")

// There is no api.ErrChatNotFound sentinel. It existed for errors.Is
// classification against a store TRANSITION, and PromoteRewind was the only
// transition that returned it. (command.ErrChatNotFound is a different, live
// value: the 404 response body, not a sentinel to match on.)

// TransportError wraps bridge-level transport failures (pipe closed,
// write timeout, process exited) with explicit retryability semantics.
// Callers use errors.As to classify without substring matching.
type TransportError struct {
	Err       error
	Retryable bool
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }
