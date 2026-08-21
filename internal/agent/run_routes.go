package agent

import "net/http"

// runRoutes is the HTTP surface over the run lifecycle: the /api/runs and
// /api/recipes endpoints plus the schedule CRUD.
//
// It is a deliberate adapter, and the split is worth stating because two others
// were considered and rejected. Runs was 75 methods, well over the widest stdlib
// receiver (database/sql.DB at 53), so it wanted a cut. The obvious cut — the
// clock and the claims (deadlines, leases, termination arbitration) away from the
// executor — does NOT hold: the expiry path has to issue the cancel, so the
// extracted type would call back into Runs while Runs calls fifteen of its
// methods. That is a mutual pair, which Google's coupling test answers by
// combining, not separating: you cannot run a bounded run without both halves.
//
// The transport IS separable, because the dependency runs one way. Every handler
// here parses a request, calls one domain method and writes a response; nothing in
// the domain calls a handler. The eleven apparent back-edges were route
// registration, which moved here with them, and one genuine misfiling —
// rawInspect is a KAS call rather than a handler and went to its only caller.
//
// It holds nothing but its subject, which is what an adapter should hold. The
// rulebook blesses exactly this shape: a deliberate adapter that narrows a wide
// surface or bridges two contracts earns its keep even when thin.
type runRoutes struct{ runs *Runs }

// register mounts every run and schedule endpoint.
//
// The registrations used to sit in Runtime.RegisterRoutes, ten lines naming one
// handler each, which put the runtime in the middle of a decision only this type
// has an opinion about. A surface that owns its handlers should own its paths.
func (rr *runRoutes) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runs/{id}", rr.handleRun)
	mux.HandleFunc("POST /api/runs", rr.handleLaunch)
	mux.HandleFunc("POST /api/runs/{id}/cancel", rr.handleCancel)
	mux.HandleFunc("POST /api/runs/{id}/pause", rr.handlePause)
	mux.HandleFunc("POST /api/runs/{id}/resume", rr.handleResume)
	mux.HandleFunc("POST /api/runs/{id}/retry", rr.handleRetry)
	mux.HandleFunc("DELETE /api/runs/{id}", rr.handleDelete)
	mux.HandleFunc("POST /api/runs/{id}/step", rr.handleStepStatus)
	mux.HandleFunc("GET /api/recipes", rr.handleRecipes)
	rr.registerSchedule(mux)
}
