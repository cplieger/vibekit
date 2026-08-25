package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/uistate"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// handleUIState serves the synced UI arrangement.
//
//	GET  /api/ui-state -> {..., "revision": N}
//	PUT  /api/ui-state  {..., "revision": N} -> the accepted document, or 409
//
// The arrangement moved off localStorage because a per-device copy is what made
// it not travel between devices — the same defect, with the same cause, that the
// sibling terminal app already fixed by deleting its local copy. Reasoning and
// the two fields that stay per-device: internal/uistate.
//
// PUT takes the WHOLE document, not a patch. The fields are interdependent (a
// pin only means something against the tab list it names, which Sanitize
// enforces), and a whole-document write is what makes the revision check a real
// concurrency control rather than a per-field race.
func (s *Server) handleUIState(w http.ResponseWriter, r *http.Request) {
	if s.uiState == nil {
		// Unwired (a test server, or a build with no config dir). Answer the
		// empty document rather than 404: a client that cannot read the
		// arrangement must still boot, and an empty one is the honest answer.
		if r.Method == http.MethodGet {
			webhttp.WriteJSON(w, uistate.Document{})
			return
		}
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable,
			httpreply.ErrorJSON("ui state store unavailable"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		webhttp.WriteJSON(w, s.uiState.Get())
	case http.MethodPut:
		s.handleUIStateWrite(w, r)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (s *Server) handleUIStateWrite(w http.ResponseWriter, r *http.Request) {
	webhttp.LimitBody(w, r, uistate.MaxBytes)
	var doc uistate.Document
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&doc); err != nil {
		httpreply.BadRequest(w, "invalid json")
		return
	}
	// Refuse trailing data rather than half-accepting it, the same discipline the
	// terminal engine's order endpoint uses: a body this cannot fully read is a
	// client bug, and answering 200 to one hides it.
	if dec.More() {
		httpreply.BadRequest(w, "unexpected trailing data")
		return
	}
	next, err := s.uiState.Put(r.Context(), &doc.State, doc.Revision)
	if errors.Is(err, uistate.ErrStale) {
		// 409 rather than 400 or 404: the request is well formed and the resource
		// exists, the caller's view is simply behind. The CURRENT document rides
		// along so the client can re-apply and retry without a second round trip.
		webhttp.WriteJSONStatus(w, http.StatusConflict, next)
		return
	}
	if err != nil {
		slog.Error("ui state write failed", "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError,
			httpreply.ErrorJSON("could not save the UI arrangement"))
		return
	}
	// Tell every OTHER device. The payload carries the state AND the revision, so
	// a receiver applies it and knows what to write next without a GET — which is
	// what keeps one device's tab drag from costing every other device a round
	// trip. The writer receives its own echo too and treats it as a no-op,
	// because it already holds these values (see the client's applyRemote).
	if s.agent != nil {
		s.agent.Broadcast(r.Context(), vibekit.NewEvent(vibekit.EventUIStateChanged, "", uiStatePayload(&next)))
	}
	webhttp.WriteJSON(w, next)
}

// uiStatePayload projects the store's document onto the wire payload. A separate
// shape rather than embedding uistate.Document, because the wire type is what
// wiregen generates the client decoder from and it lives in internal/vibekit
// with the rest of the wire vocabulary.
func uiStatePayload(d *uistate.Document) vibekit.UIStateChangedPayload {
	return vibekit.UIStateChangedPayload{
		Revision:         d.Revision,
		TabOrder:         d.TabOrder,
		PinnedTabs:       d.PinnedTabs,
		EditorFiles:      d.EditorFiles,
		DismissedBanners: d.DismissedBanners,
		FBPath:           d.FBPath,
		Theme:            d.Theme,
		TurnFolds:        d.TurnFolds,
	}
}
