package agent

import "net/http"

// runRoutes is the HTTP surface over the run lifecycle: the /api/runs and
// /api/recipes endpoints plus the schedule CRUD.
//
// A deliberate adapter rather than a further split of Runs. Runs was 75
// methods, well over the widest stdlib receiver, but the obvious cut — the
// clock and the claims away from the executor — does NOT hold: the expiry path
// has to issue the cancel, so the extracted type would call back into Runs
// while Runs calls fifteen of its methods, and Google's coupling test answers
// a mutual pair by combining, not separating.
//
// The transport IS separable, because the dependency runs one way: every
// handler here parses a request, calls one domain method and writes a
// response, and nothing in the domain calls a handler.
//
// It holds nothing but its subject, which is what an adapter should hold.
type runRoutes struct{ runs *Runs }

// register mounts every run and schedule endpoint.
func (rr *runRoutes) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runs/{id}", rr.handleRun)
	mux.HandleFunc("GET /api/runs/live", rr.handleLiveRuns)
	// ONE step's transcript, addressed by node PATH. The TRAILING wildcard is
	// required rather than a style choice: a node path contains "/", and a
	// single-segment {path} cannot match one. Percent-encoding the separators
	// instead would be REFUSED by internal/server's canonicalAPIPath, which
	// compares the DECODED path against what ServeMux would route — so raw slashes
	// are the only spelling that works, and they are canonical (no dot segments),
	// so that gate passes them.
	//
	// The EXACT form is registered beside the subtree deliberately: with only the
	// subtree, ServeMux answers /api/runs/{id}/steps with a 307 to the trailing-slash
	// form, which is the one redirect class canonicalAPIPath states it cannot see —
	// and its doc rests on every vibekit subtree route also registering its exact
	// form. Both land on the same handler, which 400s an empty path.
	mux.HandleFunc("GET /api/runs/{id}/steps", rr.handleStepTranscript)
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
