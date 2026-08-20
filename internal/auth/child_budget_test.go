package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The two handlers that shell out synchronously document a wall-clock cap. These
// pin that the cap bounds the HANDLER, not the child.
//
// exec.CommandContext's default cancellation SIGKILLs the parent PID only, and a
// forked helper inherits the stdout/stderr pipe write ends, so cmd.Run waits for
// the LAST descendant before its output goroutines see EOF. Measured on go1.27.0
// against this same `sleep 10` fixture under a 50ms budget: Run returned after
// 10.001s with the default cancellation and, identically, with Setpgid alone —
// because the group kill both handlers already carried only ran after Run had
// returned. Reverting boundChild makes both of these fail on elapsed time.
//
// The assertion is deliberately on time and not only on the status code: the old
// code produced the RIGHT status (504 / a fail-soft 200) after the wrong
// duration, so a status-only test passed throughout and would keep passing.
const (
	// budget is the per-handler timeout the fixture configures.
	budget = 50 * time.Millisecond
	// childLife is how long the fake CLI lives. Two orders of magnitude above the
	// budget so the two are impossible to confuse.
	childLife = 10 * time.Second
	// tolerance is the slack allowed over budget+childWaitDelay for scheduling on
	// a loaded runner. Still far below childLife, which is what keeps this
	// falsifiable rather than merely generous.
	tolerance = 2 * time.Second
)

func maxHandlerTime() time.Duration { return budget + childWaitDelay + tolerance }

func TestHandleWhoami_ReturnsOnItsOwnBudgetNotTheChildsLifetime(t *testing.T) {
	skipIfNotUnix(t)

	path := writeFakeCLIScript(t, "sleep 10\n")
	h := NewHandler(fixedPath(path), WithConfig(Config{
		LoginURLTimeout: DefaultConfig.LoginURLTimeout,
		LoginTimeout:    DefaultConfig.LoginTimeout,
		LogoutTimeout:   DefaultConfig.LogoutTimeout,
		WhoamiTimeout:   budget,
	}))

	rr := httptest.NewRecorder()
	start := time.Now()
	h.handleWhoami(rr, httptest.NewRequest(http.MethodGet, "/api/whoami", nil))
	elapsed := time.Since(start)

	if elapsed > maxHandlerTime() {
		t.Errorf("handleWhoami returned after %v, want <= %v: the handler waited out the child "+
			"(%v) instead of honouring WhoamiTimeout (%v)", elapsed, maxHandlerTime(), childLife, budget)
	}
	// Fail-soft is unchanged: still 200 with the sentinel, just on time.
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (fail-soft banner)", rr.Code)
	}
}

func TestHandleLogout_ReturnsOnItsOwnBudgetNotTheChildsLifetime(t *testing.T) {
	skipIfNotUnix(t)

	path := writeFakeCLIScript(t, "sleep 10\n")
	h := NewHandler(fixedPath(path), WithConfig(Config{
		LoginURLTimeout: DefaultConfig.LoginURLTimeout,
		LoginTimeout:    DefaultConfig.LoginTimeout,
		LogoutTimeout:   budget,
		WhoamiTimeout:   DefaultConfig.WhoamiTimeout,
	}))

	rr := httptest.NewRecorder()
	start := time.Now()
	h.handleLogout(rr, httptest.NewRequest(http.MethodPost, "/api/logout", nil))
	elapsed := time.Since(start)

	if elapsed > maxHandlerTime() {
		t.Errorf("handleLogout returned after %v, want <= %v: the handler waited out the child "+
			"(%v) instead of honouring LogoutTimeout (%v)", elapsed, maxHandlerTime(), childLife, budget)
	}
	if rr.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", rr.Code)
	}
}
