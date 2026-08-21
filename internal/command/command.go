// Package command implements the POST /api/command dispatch table.
// Runtime registers concrete handler functions; the Dispatcher routes
// incoming commands by type and handles envelope-level concerns
// (body parsing, validation).
//
// Idempotency is NOT here. It is the Idempotency-Key header, handled by one
// middleware for every mutating route (internal/server/idempotency.go). This
// package used to run a second dedup cache over a request_id BODY field, which
// is the reason it could not be middleware; that field is gone from the
// envelope, so the header middleware now covers this route like any other.
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

// maxCommandBody caps the whole POST /api/command envelope. It is the generic
// webhttp.MaxJSONBody: the largest payload this endpoint carries is a prompt's text
// at maxPromptBytes (512 KiB) plus path-only attachment metadata, so 1 MiB is
// ~2x headroom. It was 5 MiB to fit a 4 MiB user-merged partial-write payload,
// and that command is gone — KAS decides per action, so there is no merged text
// to post.
const maxCommandBody = webhttp.MaxJSONBody

// Handler is the signature for a command handler function.
//
// A handler RETURNS its outcome; it is handed no http.ResponseWriter and no
// Dispatcher. Two things follow that were not true when it wrote its own
// response. A handler cannot forget to answer — a bare return used to send an
// empty 200 — and it cannot answer twice. And it needs no reference to the
// router that called it: the whole reason every handler took a *Dispatcher was
// to reach three response helpers on it.
//
// The body is marshalled by the dispatcher. A nil error means 200 with that
// body; an error carrying a status (see StatusError) sets it, and a bare error
// is a 500.
type Handler func(ctx context.Context, cmd *vibekit.ClientCommand) (any, error)

// statusError carries the HTTP status a handler chose for a failure.
//
// The status rides the error per-SITE rather than being derived from the error
// value, because the same sentinel legitimately means different statuses in
// different places: ErrChatNotFound is a 409 when a shell command finds no chat
// to attach to and a 404 when set_mode is asked to configure one. A
// sentinel-to-status table would have to pick one and be wrong at the other.
type statusError struct {
	err  error
	code int
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

// StatusError wraps err with the HTTP status the dispatcher should answer with.
// Exported because Handler is: the runtime registers cmdSwitchModel directly and
// needs the same vocabulary as the handlers in this package.
func StatusError(code int, err error) error {
	return &statusError{code: code, err: err}
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

// New constructs a Dispatcher over the envelope seam. A handler's own
// collaborators arrive at registration (see RegisterDefaults), so nothing here
// can reach the host's wider surface.
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
// Using a struct instead of map[string]string makes the shape explicit,
// enables json.Marshal's cached struct encoder, and provides a single
// place to add future fields (e.g. code, retryable).
type errorResponse struct {
	Error string `json:"error"`
}

// writeErr writes a JSON error response at the status the handler chose.
//
// The body goes through rpcerr.Text rather than err.Error() because four of
// the handlers reaching here (compact, mode, rewind, steer) forward a bridge Call
// failure verbatim, and on a -32603 the error string is KAS's literal "Internal
// error" while the cause sits unread in `error.data`. RPCErrorText is a no-op for
// every ordinary Go error, so the one call covers both populations.
func writeErr(w http.ResponseWriter, err error) {
	webhttp.WriteJSONStatus(w, statusOf(err), errorResponse{Error: rpcerr.Text(err)})
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

	// Centralised chat_id validation.
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
	// A handler that succeeded with nothing to say still owes the client the
	// standard success body: {"ok":true} is what every such handler used to
	// write by hand through RespondOK.
	if body == nil {
		body = responseOK
	}
	webhttp.WriteJSON(w, body)
}

// SessionParams builds the base ACP parameter map with the "sessionId"
// key set from the bridge. Extra key-value pairs from extra maps are
// merged in (last-wins).
//
// Takes the 1-method sessionScoped rather than a whole Bridge: reading an id is
// not a licence to call, notify or take the turn slot.
func SessionParams(b sessionScoped, extra ...map[string]any) map[string]any {
	m := map[string]any{keySessionID: b.SessionID()}
	for _, e := range extra {
		maps.Copy(m, e)
	}
	return m
}
