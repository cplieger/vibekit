package agent

// Tests for schedule_http.go: whether the schedule surface exists at all, which
// depends on a dependency the rest of the run surface does not need.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/schedule"
)

// TestRegisterSchedule_MountsTheSurfaceOnlyWithAStoreBehindIt pins the guard that
// decides whether these three routes exist.
//
// Every schedule handler reads the store directly, so registering them without one
// does not degrade — it panics inside an HTTP handler on the first request, which
// takes down the connection and logs a stack trace where a 404 belongs. Skipping
// the registration when a store IS present is the same bug reflected: the schedule
// UI gets a 404 for a feature that is configured and working.
func TestRegisterSchedule_MountsTheSurfaceOnlyWithAStoreBehindIt(t *testing.T) {
	t.Run("a configured store gets the routes", func(t *testing.T) {
		st, err := schedule.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("schedule.NewStore: %v", err)
		}
		mux := http.NewServeMux()
		(&runRoutes{runs: &Runs{schedules: st}}).registerSchedule(mux)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET /api/schedules = %d, want 200; the schedule UI reads this: %s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("no store means no routes, not broken ones", func(t *testing.T) {
		mux := http.NewServeMux()
		(&runRoutes{runs: &Runs{}}).registerSchedule(mux)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/schedules", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/schedules = %d with no store, want 404; a registered route with no "+
				"store behind it fails inside the handler instead: %s", rec.Code, rec.Body.String())
		}
	})
}
