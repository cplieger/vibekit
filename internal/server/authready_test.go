package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/pinstall/v3"
)

// TestAuthReasonIsTheClientContract pins the sign-in reason literal and the one
// property that makes it a SECOND family rather than a variant of the install
// family: it must not carry the "kiro-cli" prefix.
//
// static-src/runtime-health.ts decides a verdict is a kiro-cli INSTALL verdict by
// that prefix, then keys its copy on each install literal in full. A sign-in
// reason spelled "kiro-cli ..." would match the family, miss every key, and
// render the terminal "the install failed and its retries are exhausted; restart
// the container" copy — telling the reader to do the one thing that cannot fix an
// expired sign-in. Change this string and change AUTH_REASON_PREFIX there in the
// same commit.
func TestAuthReasonIsTheClientContract(t *testing.T) {
	if reasonSignIn != "sign-in required" {
		t.Errorf("reasonSignIn = %q, want %q (runtime-health.ts prefix-matches this)", reasonSignIn, "sign-in required")
	}
	if strings.HasPrefix(reasonSignIn, "kiro-cli") {
		t.Errorf("reasonSignIn %q carries the install family's prefix, so the client would render install copy for a signed-out runtime", reasonSignIn)
	}
	// And the reverse: no install reason may be read as a sign-in one, or the
	// banner would offer a login modal for a missing binary.
	for _, install := range []string{reasonInstalling, reasonRetrying, reasonUnavailable, reasonSettings} {
		if strings.HasPrefix(install, reasonSignIn) {
			t.Errorf("install reason %q starts with the sign-in prefix", install)
		}
	}
}

// TestHealthReportsTheSignInLeg covers the whole point of the latch: readiness
// reports a dead sign-in, it reports it BEHIND the kiro-cli leg (the envelope
// carries one reason, and an uninstalled runtime is the superset failure), and it
// reads a value rather than probing — a health handler that spawned kiro-cli
// would hand a monitor's poll a process-launch lever, and kiroauth.Token can
// block up to 15s on an SSO-OIDC refresh.
func TestHealthReportsTheSignInLeg(t *testing.T) {
	ready := func() (bool, pinstall.Reason) { return true, pinstall.ReasonReady }
	installing := func() (bool, pinstall.Reason) { return false, pinstall.ReasonInstalling }

	tests := map[string]struct {
		kiroReady  func() (bool, pinstall.Reason)
		wireAuth   bool
		authFailed bool
		wantStatus int
		wantBody   string
		wantCalls  int
	}{
		"a live sign-in is ready": {
			kiroReady:  ready,
			wireAuth:   true,
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok"}`,
			wantCalls:  1,
		},
		"a dead sign-in withholds readiness with the sign-in reason": {
			kiroReady:  ready,
			wireAuth:   true,
			authFailed: true,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"unready","reason":"` + reasonSignIn + `"}`,
			wantCalls:  1,
		},
		"the kiro-cli leg wins when both are withheld": {
			kiroReady:  installing,
			wireAuth:   true,
			authFailed: true,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"unready","reason":"` + reasonInstalling + `"}`,
			// Not consulted at all behind a withheld install verdict, which is
			// what keeps the single reason field honest.
			wantCalls: 0,
		},
		"an unwired auth leg leaves readiness to the install verdict": {
			kiroReady:  ready,
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok"}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			calls := 0
			s := &Server{kiroReady: tc.kiroReady}
			if tc.wireAuth {
				s.authUnavailable = func() bool {
					calls++
					return tc.authFailed
				}
			}
			s.ready.Store(true)

			rec := httptest.NewRecorder()
			s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
				t.Errorf("body = %q, want %q", got, tc.wantBody)
			}
			if calls != tc.wantCalls {
				t.Errorf("auth leg consulted %d times, want %d", calls, tc.wantCalls)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

// TestHealthNeverLeaksTheAuthFailureReason pins the disclosure rule for this leg.
// /api/health is unauthenticated, and kiro-cli's own token error can name a path
// on the volume (an SSO cache file, a version directory). The specific failure
// goes to the log line and the SSE error frame; readiness serves a fixed literal,
// which is why the latch holds a bool and not a string.
func TestHealthNeverLeaksTheAuthFailureReason(t *testing.T) {
	s := &Server{
		kiroReady:       func() (bool, pinstall.Reason) { return true, pinstall.ReasonReady },
		authUnavailable: func() bool { return true },
	}
	s.ready.Store(true)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	body := strings.TrimSpace(rec.Body.String())
	if body != `{"status":"unready","reason":"`+reasonSignIn+`"}` {
		t.Errorf("body = %q: the sign-in leg must serve the fixed literal and nothing else", body)
	}
}
