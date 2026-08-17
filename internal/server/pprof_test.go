package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pprofRequest drives the gated handler the way a caller reaches it, with the
// full /debug/pprof/ path intact — pprof.Index derives the profile name from that
// prefix, so a test that stripped it would exercise a different code path than
// the mount does.
func pprofRequest(t *testing.T, path, remote, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	req.Host = host
	rec := httptest.NewRecorder()
	pprofHandler().ServeHTTP(rec, req)
	return rec
}

// The goroutine dump is the reason D116 mounts anything at all, so it is the one
// asserted end to end rather than through the index page.
func TestPprof_GoroutineDumpAnswersOnLoopback(t *testing.T) {
	rec := pprofRequest(t, pprofPath+"goroutine?debug=2", "127.0.0.1:54321", "localhost:9847")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "goroutine") {
		t.Errorf("body does not look like a goroutine dump: %.200s", body)
	}
	// ?debug=2 is the plain-text form, which is what makes this fetchable by an
	// agent inside the container with no pprof tooling.
	if !strings.Contains(body, "runtime.") && !strings.Contains(body, ".go:") {
		t.Errorf("body carries no stack frames: %.400s", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestPprof_IndexAnswersOnLoopback(t *testing.T) {
	rec := pprofRequest(t, pprofPath, "127.0.0.1:54321", "127.0.0.1:9847")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("index does not list the goroutine profile: %.400s", rec.Body.String())
	}
}

// The gate, case by case. Both ends have to be loopback, and browser or proxy
// provenance is refused on top — a dump names every function on every stack, so
// this is a map of the process rather than a status page.
func TestPprof_RefusesEverythingButAnInContainerCaller(t *testing.T) {
	cases := map[string]struct {
		remote, host string
		header       [2]string
		wantStatus   int
	}{
		"loopback peer and host":      {remote: "127.0.0.1:1", host: "localhost:9847", wantStatus: http.StatusOK},
		"IPv6 loopback":               {remote: "[::1]:1", host: "[::1]:9847", wantStatus: http.StatusOK},
		"LAN peer":                    {remote: "192.168.1.20:1", host: "localhost:9847", wantStatus: http.StatusForbidden},
		"rebound host, loopback peer": {remote: "127.0.0.1:1", host: "evil.example.com", wantStatus: http.StatusForbidden},
		"loopback peer behind a proxy": {
			remote: "127.0.0.1:1", host: "localhost:9847",
			header: [2]string{"X-Forwarded-For", "203.0.113.7"}, wantStatus: http.StatusForbidden,
		},
		"from a browser tab": {
			remote: "127.0.0.1:1", host: "localhost:9847",
			header: [2]string{"Sec-Fetch-Site", "same-origin"}, wantStatus: http.StatusForbidden,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, pprofPath+"goroutine", nil)
			req.RemoteAddr = c.remote
			req.Host = c.host
			if c.header[0] != "" {
				req.Header.Set(c.header[0], c.header[1])
			}
			rec := httptest.NewRecorder()
			pprofHandler().ServeHTTP(rec, req)
			if rec.Code != c.wantStatus {
				t.Errorf("code = %d, want %d: %.200s", rec.Code, c.wantStatus, rec.Body.String())
			}
		})
	}
}

// The two expensive profiles are deliberately absent, and a 404 from Index is how
// that reads on the wire. Pinned so re-adding one is a decision rather than a
// drive-by: each holds the server for its sample window, caller-controlled.
func TestPprof_HoldTheServerProfilesAreNotMounted(t *testing.T) {
	for _, name := range []string{"profile", "trace"} {
		t.Run(name, func(t *testing.T) {
			rec := pprofRequest(t, pprofPath+name, "127.0.0.1:1", "localhost:9847")
			if rec.Code == http.StatusOK {
				t.Errorf("%s answered 200; it holds the server for its sample window", name)
			}
		})
	}
}

// A refusal must not leak the profile, and it must say that THIS endpoint
// declined it.
//
// The middleware is shared with the kiro-cli repair hook, and the refusal is the
// whole of what a rejected caller is told: a profile request answered with "the
// kiro-cli repair hook is loopback-only" is a correct status with a wrong answer,
// and it sends the operator to retry a path they never called.
func TestPprof_RefusalNamesProfilesAndCarriesNoProfileData(t *testing.T) {
	rec := pprofRequest(t, pprofPath+"goroutine?debug=2", "10.0.0.5:1", "localhost:9847")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "goroutine profile") || strings.Contains(body, "runtime.gopark") {
		t.Errorf("the refusal leaked profile data: %.400s", body)
	}
	if !strings.Contains(body, "loopback-only") {
		t.Errorf("refusal body = %q, want the loopback-only error envelope", body)
	}
	if !strings.Contains(body, pprofSurface) {
		t.Errorf("refusal body = %q, want it to name %q", body, pprofSurface)
	}
	if strings.Contains(body, kiroRescanSurface) {
		t.Errorf("refusal body names the repair hook instead of this endpoint: %q", body)
	}
}
