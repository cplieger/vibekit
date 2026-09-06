package agent

import "net/http"

// runRoutes is the HTTP surface over the run lifecycle: the /api/runs and
// /api/recipes endpoints plus the schedule CRUD. A transport adapter only —
// every handler parses a request, calls one Runs method and writes a response,
// and nothing in Runs calls back into a handler.
type runRoutes struct{ runs *Runs }

// register mounts every run and schedule endpoint.
func (rr *runRoutes) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runs/{id}", rr.handleRun)
	mux.HandleFunc("GET /api/runs/{id}/controls", rr.handleControls)
	mux.HandleFunc("GET /api/runs/live", rr.handleLiveRuns)
	mux.HandleFunc("POST /api/runs", rr.handleLaunch)
	mux.HandleFunc("POST /api/runs/{id}/cancel", rr.handleCancel)
	mux.HandleFunc("POST /api/runs/{id}/pause", rr.handlePause)
	mux.HandleFunc("POST /api/runs/{id}/resume", rr.handleResume)
	mux.HandleFunc("POST /api/runs/{id}/retry", rr.handleRetry)
	mux.HandleFunc("DELETE /api/runs/{id}", rr.handleDelete)
	mux.HandleFunc("POST /api/runs/{id}/step", rr.handleStepStatus)
	mux.HandleFunc("POST /api/runs/{id}/answer", rr.handleAnswer)
	mux.HandleFunc("GET /api/recipes", rr.handleRecipes)
	rr.registerSchedule(mux)
}
