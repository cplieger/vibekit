// Package command implements the POST /api/command dispatch table.
// Hub registers concrete handler functions; the Dispatcher routes
// incoming commands by type and handles envelope-level concerns
// (body parsing, idempotency, validation).
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
	"github.com/cplieger/webhttp"
)

// maxCommandBody caps the whole POST /api/command envelope. It is the generic
// webhttp.MaxJSONBody: the largest payload this endpoint carries is a prompt's text
// at maxPromptBytes (512 KiB) plus path-only attachment metadata, so 1 MiB is
// ~2x headroom. It was 5 MiB to fit a 4 MiB user-merged partial-write payload,
// and that command is gone — KAS decides per action, so there is no merged text
// to post.
const maxCommandBody = webhttp.MaxJSONBody

// Handler is the signature for a command handler function.
type Handler func(ctx context.Context, w http.ResponseWriter, cmd *vibekit.ClientCommand)

// DedupGate is the request_id idempotency cache: replay a repeated command
// instead of running it twice. Held by the Dispatcher rather than passed to
// handlers, because replay is an envelope concern the router owns and a handler
// decides nothing about it.
//
// The drain refusal used to live here as a third method and does not any more.
// It gates TWO routes, commands and the event stream, so it belongs at the
// routes rather than inside one of them; see hub.refuseWhenDraining. It cannot
// become a global middleware either, because only those two routes want it and
// the chain covers health, version and the static assets as well.
//
// The key is a BODY field, which is why this is not middleware in the first
// place: middleware cannot read request_id without buffering and re-parsing the
// body. vibekit's other idempotency layer keys on the Idempotency-Key header and
// therefore is middleware; the two are one concept implemented twice, and
// unifying them is a wire change recorded in specs/go-shape/stage1-residue.md.
type DedupGate interface {
	// CheckDedup returns the cached response for reqID, if any.
	CheckDedup(reqID string) ([]byte, bool)
	// RecordDedup caches a response for idempotent replay.
	RecordDedup(reqID string, result []byte)
}

// Dispatcher holds the command dispatch table and serves the
// POST /api/command HTTP endpoint.
type Dispatcher struct {
	gate     DedupGate
	handlers map[vibekit.CommandType]Handler
	mu       sync.RWMutex
}

// New constructs a Dispatcher over the envelope seam. A handler's own
// collaborators arrive at registration (see RegisterDefaults), so nothing here
// can reach the host's wider surface.
func New(gate DedupGate) *Dispatcher {
	return &Dispatcher{
		gate:     gate,
		handlers: make(map[vibekit.CommandType]Handler),
	}
}

// Register adds a handler for the given command type.
func (d *Dispatcher) Register(t vibekit.CommandType, h Handler) {
	d.mu.Lock()
	d.handlers[t] = h
	d.mu.Unlock()
}

// Respond writes a JSON body and caches it for request_id idempotency.
func (d *Dispatcher) Respond(w http.ResponseWriter, reqID string, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		slog.Error("respond marshal", keyError, err)
		httpreply.InternalError(w, err)
		return
	}
	d.gate.RecordDedup(reqID, data)
	httpreply.WriteRawJSON(w, data)
}

// RespondOK writes the standard {"ok":true} success response and
// caches it for idempotent replay. Shorthand for the most common
// command success case.
func (d *Dispatcher) RespondOK(w http.ResponseWriter, reqID string) {
	d.Respond(w, reqID, responseOK)
}

// errorResponse is the typed wire shape for JSON error responses.
// Using a struct instead of map[string]string makes the shape explicit,
// enables json.Marshal's cached struct encoder, and provides a single
// place to add future fields (e.g. code, retryable).
type errorResponse struct {
	Error string `json:"error"`
}

// RespondErr writes a JSON error response with the given status code.
//
// The body goes through rpcerr.Text rather than err.Error() because four of
// the handlers reaching here (compact, mode, rewind, steer) forward a bridge Call
// failure verbatim, and on a -32603 the error string is KAS's literal "Internal
// error" while the cause sits unread in `error.data`. RPCErrorText is a no-op for
// every ordinary Go error, so the one call covers both populations.
func (d *Dispatcher) RespondErr(w http.ResponseWriter, code int, err error) {
	webhttp.WriteJSONStatus(w, code, errorResponse{Error: rpcerr.Text(err)})
}

// RequireChatID validates that cmd.ChatID is non-empty and writes a
// 400 response if not. Returns true when the chat ID is present.
func (d *Dispatcher) RequireChatID(w http.ResponseWriter, cmd *vibekit.ClientCommand) bool {
	if cmd.ChatID == "" {
		d.RespondErr(w, http.StatusBadRequest, ErrMissingChatID)
		return false
	}
	return true
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

	if !validRequestID(cmd.RequestID) {
		httpreply.BadRequest(w, "invalid request_id")
		return
	}

	// Idempotent retries: same request_id → cached response.
	if cached, ok := d.gate.CheckDedup(cmd.RequestID); ok {
		slog.Debug("idempotent replay", "request_id", cmd.RequestID, keyType, cmd.Type)
		httpreply.WriteRawJSON(w, cached)
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
	if ok {
		fn(r.Context(), w, &cmd)
	} else {
		httpreply.BadRequest(w, "unknown command: "+string(cmd.Type))
	}
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
