package hub

// Schedule REST: the workflow rows on /docs/workflows carry a Schedule button
// beside Run, and this is what it talks to.
//
// Three verbs and no more. A schedule is a small record the client rewrites
// wholesale, so an upsert plus a delete covers editing without a PATCH shape,
// and the list is small enough to send in full.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/runesafe"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/schedule"
	"github.com/cplieger/webhttp"
)

// maxLogFieldBytes bounds one caller-supplied string on its way into a log
// attribute. A schedule id arrives in a JSON body or a path segment and is
// checked only for emptiness, so it has no length of its own to respect.
const maxLogFieldBytes = 256

// logField prepares one untrusted string for a slog attribute: runesafe's
// single-line preset (C0/C1 controls, DEL, Bidi overrides and the paragraph
// separators become spaces) capped on a rune boundary.
//
// The single-line part is the point. A schedule id is client-chosen and reaches
// these two log lines unconstrained, so a newline in it forges a whole log
// record downstream in Loki — the reader cannot tell an injected line from one
// the server wrote. The recurrence frequency is enum-checked by Spec.Validate
// before it gets here and the recipe name comes from the workspace rather than
// the request, but both are routed through the same helper: a reader of this
// handler should not have to prove which of four attributes is safe, and the
// cost is one call.
func logField(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxLogFieldBytes)
}

// scheduleView is one row as the client sees it: the stored entry plus the
// resolved next run, which the server computes so the UI never reimplements the
// recurrence math and the summary line cannot disagree with what will happen.
type scheduleView struct {
	NextRunAt  *time.Time    `json:"next_run_at,omitempty"`
	LastRunAt  *time.Time    `json:"last_run_at,omitempty"`
	ID         string        `json:"id"`
	Source     string        `json:"source"`
	Name       string        `json:"name,omitempty"`
	LastResult string        `json:"last_result,omitempty"`
	Spec       schedule.Spec `json:"spec"`
	Enabled    bool          `json:"enabled"`
}

// registerScheduleRoutes wires the schedule surface. No-op when scheduling is
// unavailable (no store), so the routes never 500 on a nil dependency.
func (h *Hub) registerScheduleRoutes(mux *http.ServeMux) {
	if h.schedules == nil {
		return
	}
	mux.HandleFunc("GET /api/schedules", h.handleScheduleList)
	mux.HandleFunc("POST /api/schedules", h.handleSchedulePut)
	mux.HandleFunc("DELETE /api/schedules/{id}", h.handleScheduleDelete)
}

// handleScheduleList: GET /api/schedules → every schedule with its next run.
func (h *Hub) handleScheduleList(w http.ResponseWriter, _ *http.Request) {
	entries := h.schedules.List()
	out := make([]scheduleView, 0, len(entries))
	for i := range entries {
		out = append(out, h.scheduleViewOf(&entries[i]))
	}
	webhttp.WriteJSON(w, map[string]any{"schedules": out})
}

// scheduleViewOf resolves an entry's next run for display. A spec that cannot
// be computed yields no next run rather than an error: the row still renders so
// the user can fix or delete it.
func (h *Hub) scheduleViewOf(e *schedule.Entry) scheduleView {
	v := scheduleView{
		Spec: e.Spec, ID: e.ID, Source: e.Source, Name: e.Name,
		LastResult: e.LastResult, Enabled: e.Enabled,
	}
	if !e.LastRunAt.IsZero() {
		t := e.LastRunAt
		v.LastRunAt = &t
	}
	if e.Enabled {
		// Same derivation the runner uses (schedule.NextRunFrom), measured from
		// the entry's anchor and floored at now so a stale anchor cannot render a
		// next run that has already passed.
		if next, err := schedule.NextRunFrom(e.Spec, e.Anchor, time.Now()); err == nil {
			v.NextRunAt = &next
		}
	}
	return v
}

// handleSchedulePut: POST /api/schedules → insert or replace one schedule.
//
// The recipe source is validated against the live recipe list rather than
// trusted, for the same reason handleRunLaunch does it: the value looks like a
// path, and storing an arbitrary one would let a client aim the scheduler at a
// file that is not a recipe.
func (h *Hub) handleSchedulePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string        `json:"id"`
		Source  string        `json:"source"`
		Spec    schedule.Spec `json:"spec"`
		Enabled bool          `json:"enabled"`
	}
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpreply.BadRequest(w, "invalid schedule payload")
		return
	}
	recipe, err := h.recipeBySource(r.Context(), req.Source)
	if err != nil {
		httpreply.BadRequest(w, "unknown recipe: "+req.Source)
		return
	}
	id := req.ID
	if id == "" {
		id = recipe.Source // one schedule per recipe, addressable by its source
	}
	entry := schedule.Entry{
		ID: id, Source: recipe.Source, Name: recipe.Name,
		Spec: req.Spec, Enabled: req.Enabled,
	}
	if err := h.schedules.Put(r.Context(), &entry); err != nil {
		httpreply.BadRequest(w, err.Error())
		return
	}
	slog.Info("schedule saved", "id", logField(entry.ID), "recipe", logField(recipe.Name),
		"freq", logField(string(entry.Spec.Freq)), "enabled", entry.Enabled)
	webhttp.WriteJSON(w, h.scheduleViewOf(&entry))
}

// handleScheduleDelete: DELETE /api/schedules/{id}.
func (h *Hub) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpreply.BadRequest(w, "schedule id is required")
		return
	}
	if err := h.schedules.Delete(r.Context(), id); err != nil {
		httpreply.ServerError(w, "could not delete schedule", err)
		return
	}
	slog.Info("schedule deleted", "id", logField(id))
	webhttp.WriteJSON(w, map[string]any{"ok": true})
}
