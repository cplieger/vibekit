// Package command implements the POST /api/command dispatch table.
// Runtime registers concrete handler functions; the Dispatcher routes
// incoming commands by type and handles envelope-level concerns
// (body parsing, validation).
//
// Idempotency is the Idempotency-Key header middleware
// (internal/server/idempotency.go), not this package.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"sync"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/rpcerr"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// maxCommandBody caps the whole POST /api/command envelope: the largest
// payload is a prompt's text at maxPromptBytes (512 KiB) plus path-only
// attachment metadata, so 1 MiB is ~2x headroom.
const maxCommandBody = webhttp.MaxJSONBody

// Handler is the signature for a command handler function. It returns its
// outcome rather than writing to an http.ResponseWriter: the dispatcher
// marshals the body. A nil error means 200 with that body; an error carrying
// a status (see StatusError) sets it, and a bare error is a 500.
type Handler func(ctx context.Context, cmd *vibekit.ClientCommand) (any, error)

// statusError carries the HTTP status a handler chose for a failure, plus an
// optional machine-readable reason the error envelope emits as its additive
// `reason` field. The status rides the error per call site rather than a
// sentinel-to-status table, because the same sentinel can mean different
// statuses in different places (e.g. ErrChatNotFound is 409 from a shell
// command but 404 from set_mode).
type statusError struct {
	err    error
	reason string
	code   int
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

// StatusError wraps err with the HTTP status the dispatcher should answer with.
// Exported because Handler is: the runtime registers cmdSwitchModel directly.
func StatusError(code int, err error) error {
	return &statusError{code: code, err: err}
}

// StatusErrorReason is StatusError plus a machine-readable reason the error
// envelope carries beside the prose, so a client can branch on a value
// rather than on error text.
func StatusErrorReason(code int, reason string, err error) error {
	return &statusError{code: code, reason: reason, err: err}
}

// statusOf reports the status a handler outcome is answered with: 200 for
// success, the status the error named, and 500 for an error that named none.
func statusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if se, ok := errors.AsType[*statusError](err); ok {
		return se.code
	}
	return http.StatusInternalServerError
}

// Dispatcher holds the command dispatch table and serves the
// POST /api/command HTTP endpoint.
type Dispatcher struct {
	handlers map[vibekit.CommandType]Handler
	mu       sync.RWMutex
}

// New constructs a Dispatcher. A handler's own collaborators arrive at
// registration (see RegisterDefaults).
func New() *Dispatcher {
	return &Dispatcher{handlers: make(map[vibekit.CommandType]Handler)}
}

// Register adds a handler for the given command type.
func (d *Dispatcher) Register(t vibekit.CommandType, h Handler) {
	d.mu.Lock()
	d.handlers[t] = h
	d.mu.Unlock()
}

// errorResponse is the typed wire shape for JSON error responses.
type errorResponse struct {
	Error string `json:"error"`
	// Reason is the machine-readable refusal class, additive; existing
	// clients ignore it. See StatusErrorReason.
	Reason string `json:"reason,omitempty"`
}

// writeErr writes a JSON error response at the status the handler chose.
// rpcerr.Text (rather than err.Error()) unwraps a bridge Call's -32603
// "Internal error" to the real cause in error.data; it is a no-op for an
// ordinary Go error.
func writeErr(w http.ResponseWriter, err error) {
	resp := errorResponse{Error: rpcerr.Text(err)}
	if se, ok := errors.AsType[*statusError](err); ok {
		resp.Reason = se.reason
	}
	webhttp.WriteJSONStatus(w, statusOf(err), resp)
}

// requireChatID returns the 400 for a command that needs a chat and named none.
func requireChatID(cmd *vibekit.ClientCommand) error {
	if cmd.ChatID == "" {
		return StatusError(http.StatusBadRequest, ErrMissingChatID)
	}
	return nil
}

// ServeHTTP is the POST /api/command HTTP handler.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	webhttp.LimitBody(w, r, maxCommandBody)
	var cmd vibekit.ClientCommand
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("command body too large",
				"limit", maxCommandBody, keyError, maxErr)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				errorResponse{Error: "request body too large"})
			return
		}
		httpreply.BadRequest(w, "invalid json")
		return
	}

	if cmd.ChatID != "" && !validChatID(cmd.ChatID) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}

	d.mu.RLock()
	fn, ok := d.handlers[cmd.Type]
	d.mu.RUnlock()
	if !ok {
		httpreply.BadRequest(w, "unknown command: "+string(cmd.Type))
		return
	}

	body, err := fn(r.Context(), &cmd)
	if err != nil {
		writeErr(w, err)
		return
	}
	if body == nil {
		body = responseOK
	}
	webhttp.WriteJSON(w, body)
}

// SessionParams builds the base ACP parameter map with the "sessionId" key
// set from the bridge. Extra key-value pairs are merged in (last-wins).
// Takes the 1-method sessionScoped rather than a whole Bridge: reading an id
// is not a licence to call, notify or take the turn slot.
func SessionParams(b sessionScoped, extra ...map[string]any) map[string]any {
	m := map[string]any{keySessionID: b.SessionID()}
	for _, e := range extra {
		maps.Copy(m, e)
	}
	return m
}
