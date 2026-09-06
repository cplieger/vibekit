package agent

import "net/http"

// runRoutes is the HTTP surface over the run lifecycle: the /api/runs and
// /api/recipes endpoints plus the schedule CRUD. An adapter holding nothing but
// its subject, because the dependency runs one way — every handler parses a
// request, calls one domain method and writes a response.
type runRoutes struct{ runs *Runs }

// register mounts every run and schedule endpoint.
func (rr *runRoutes) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runs/{id}", rr.handleRun)
	mux.HandleFunc("GET /api/runs/live", rr.handleLiveRuns)
	// The exact form is registered beside the subtree because ServeMux otherwise
	// answers it with a 307 to the trailing-slash form, the one redirect class
	// internal/server's canonicalAPIPath cannot see. Both land on one handler.
	mux.HandleFunc("GET /api/runs/{id}/steps", rr.handleStepTranscript)
	// Trailing wildcard: a node path contains "/", and percent-encoding it is
	// refused by canonicalAPIPath, which compares the DECODED path.
	mux.HandleFunc("GET /api/runs/{id}/steps/{path...}", rr.handleStepTranscript)
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
