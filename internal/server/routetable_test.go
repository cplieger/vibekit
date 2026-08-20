package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServeMuxMethodPatternCannotAnswer405UnderACatchAll is the measurement the
// route table's shape rests on, and it is a fact about net/http rather than about
// vibekit: ServeMux synthesises its 405 + Allow only when NO pattern matched at
// all, so a "/" mount — which vibekit needs for History-API client routing —
// absorbs every method mismatch before that path is reached.
//
// Written as an A/B over the same patterns because the claim is a DIFFERENCE, not
// a status: with the catch-all a method mismatch is answered by the catch-all,
// and without it the same request is a 405 naming Allow. Asserting only the first
// half would pass equally if ServeMux had simply stopped emitting Allow.
func TestServeMuxMethodPatternCannotAnswer405UnderACatchAll(t *testing.T) {
	const catchAllStatus = 299 // a status no vibekit handler produces

	build := func(withCatchAll bool) *http.ServeMux {
		mux := http.NewServeMux()
		if withCatchAll {
			mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(catchAllStatus)
			}))
		}
		mux.HandleFunc("GET /api/permissions", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		return mux
	}

	serve := func(mux *http.ServeMux, method string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "http://example.com/api/permissions", http.NoBody))
		return rec
	}

	t.Run("without_catch_all_405_names_allow", func(t *testing.T) {
		rec := serve(build(false), http.MethodPost)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405 (ServeMux's own method refusal)", rec.Code)
		}
		// GET patterns match HEAD, so ServeMux advertises both.
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
		}
	})

	t.Run("with_catch_all_the_mismatch_is_absorbed", func(t *testing.T) {
		rec := serve(build(true), http.MethodPost)
		if rec.Code != catchAllStatus {
			t.Fatalf("status = %d, want %d (the catch-all answered, not a 405)",
				rec.Code, catchAllStatus)
		}
		if got := rec.Header().Get("Allow"); got != "" {
			t.Errorf("Allow = %q, want unset — ServeMux never computed a method list", got)
		}
	})

	// The one method a GET pattern does serve, stated so the expectations above
	// are not read as "no other method reaches the handler".
	t.Run("HEAD_is_served_by_a_GET_pattern", func(t *testing.T) {
		if rec := serve(build(true), http.MethodHead); rec.Code != http.StatusOK {
			t.Errorf("HEAD status = %d, want 200", rec.Code)
		}
	})
}

// TestPlainPathRoutesRefuseTheWrongMethod pins the consequence for the five
// routes that carried a ServeMux method pattern until the gate moved into the
// handler. Each must answer 405 with the Allow header
// httpreply.MethodNotAllowed renders; under the "/" SPA mount the pattern's own
// refusal is unreachable (see the test above), so the request was previously
// answered 200 with index.html.
//
// The two loopback-gated routes are exercised THROUGH loopbackOnly with an
// in-container caller, because the gate order is part of the claim: a remote
// caller must still get the 403 and learn nothing about the method set.
func TestPlainPathRoutesRefuseTheWrongMethod(t *testing.T) {
	cases := []struct {
		name      string
		method    string
		path      string
		wantAllow string
		serve     func(*Server, http.ResponseWriter, *http.Request)
	}{
		{
			name: "policy_view", method: http.MethodPost, path: "/api/permissions", wantAllow: "GET",
			serve: func(s *Server, w http.ResponseWriter, r *http.Request) { s.handlePolicyView(w, r) },
		},
		{
			name: "account_usage", method: http.MethodDelete, path: "/api/account/usage", wantAllow: "GET",
			serve: func(s *Server, w http.ResponseWriter, r *http.Request) { s.handleAccountUsage(w, r) },
		},
		{
			name: "tool_status", method: http.MethodPatch, path: "/api/tools/status", wantAllow: "GET",
			serve: func(_ *Server, w http.ResponseWriter, r *http.Request) { handleToolStatus(w, r) },
		},
		{
			name: "kiro_rescan", method: http.MethodGet, path: kiroRescanPath, wantAllow: "POST",
			serve: func(s *Server, w http.ResponseWriter, r *http.Request) {
				loopbackOnly(kiroRescanSurface, http.HandlerFunc(s.handleKiroRescan)).ServeHTTP(w, r)
			},
		},
		{
			name: "pprof_index", method: http.MethodPost, path: pprofPath + "goroutine", wantAllow: "GET",
			serve: func(_ *Server, w http.ResponseWriter, r *http.Request) { pprofHandler().ServeHTTP(w, r) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A rescan reaching its body would call kiroRescan. The gate must
			// refuse first, so recording the call is the red check for its
			// placement — a gate written after the header write would still 405
			// while having already spawned the subprocesses.
			rescanned := false
			s := &Server{
				kiroDocs:   &docsCache{},
				kiroRescan: func(context.Context) (bool, error) { rescanned = true; return true, nil },
			}

			req := httptest.NewRequest(tc.method, "http://127.0.0.1:9847"+tc.path, http.NoBody)
			req.RemoteAddr = "127.0.0.1:54321"
			req.Host = "127.0.0.1:9847"
			rec := httptest.NewRecorder()
			tc.serve(s, rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s: status = %d, want 405 (a mismatch used to reach the SPA shell)",
					tc.method, tc.path, rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"method not allowed"}` {
				t.Errorf("body = %q, want vibekit's bare error envelope", got)
			}
			if rescanned {
				t.Error("the refused request still ran the rescan; the method gate is placed after the work")
			}
		})
	}
}

// TestLoopbackGateStillPrecedesTheMethodGate pins the ORDER the two gates run
// in on the loopback-only routes: a LAN caller with the wrong method gets 403
// and no Allow header, so the refusal discloses nothing about the method set.
func TestLoopbackGateStillPrecedesTheMethodGate(t *testing.T) {
	rescanServer := &Server{kiroRescan: func(context.Context) (bool, error) { return true, nil }}
	for name, h := range map[string]http.Handler{
		"kiro_rescan": loopbackOnly(kiroRescanSurface, http.HandlerFunc(rescanServer.handleKiroRescan)),
		"pprof_index": pprofHandler(),
	} {
		t.Run(name, func(t *testing.T) {
			// GET is wrong for the rescan and right for pprof; either way the
			// LAN peer must lose at the outer gate.
			req := httptest.NewRequest(http.MethodGet, "http://localhost:9847/x", http.NoBody)
			req.RemoteAddr = "192.168.1.20:54321"
			req.Host = "localhost:9847"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 from the loopback gate", rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != "" {
				t.Errorf("Allow = %q on a 403, want unset: a refused remote caller must not learn the method set", got)
			}
		})
	}
}
