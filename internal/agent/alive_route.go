package agent

import (
	"net/http"

	"github.com/cplieger/sse"
	"github.com/cplieger/webhttp/v3"
)

// presenceTable is the push presence table as the runtime feeds it: the hub's
// connect/disconnect events and the client's keepalive acknowledgements.
// *push.Presence satisfies it.
type presenceTable interface {
	Observe(ev sse.PresenceEvent)
	Alive(tag string)
}

// aliveInvalidCode is the error code POST /api/events/alive answers when the
// SSE-Client header is absent or outside the tag grammar.
const aliveInvalidCode webhttp.ErrorCode = "alive_invalid"

// forwardPresence is the hub's presence hook: it hands every event to the table
// WithPresence wired, or drops it when none was.
func (b *bus) forwardPresence(ev sse.PresenceEvent) {
	if b.presence != nil {
		b.presence.Observe(ev)
	}
}

// handleAlive is POST /api/events/alive: the client's receipt for one keepalive it
// received, carrying its tag as SSE-Client. The tag is validated against the same
// grammar the hub applies to WithClientTag, so a hostile header puts no bytes in
// the table. An empty body and a 204: the response carries nothing the client
// reads beyond ok.
func (rt *Runtime) handleAlive(w http.ResponseWriter, r *http.Request) {
	tag := r.Header.Get(clientTagHeader)
	if !webhttp.ValidRequestID(tag) {
		webhttp.WriteError(w, r, http.StatusBadRequest, aliveInvalidCode,
			"SSE-Client header absent or outside [A-Za-z0-9_-]{1,64}")
		return
	}
	if rt.bus.presence != nil {
		rt.bus.presence.Alive(tag)
	}
	w.WriteHeader(http.StatusNoContent)
}
