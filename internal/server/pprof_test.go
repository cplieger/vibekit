package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	// The 1.27 profile is reached by name off this same index, so its absence
	// here is how a toolchain that dropped it would first read.
	if !strings.Contains(rec.Body.String(), "goroutineleak") {
		t.Errorf("index does not list the goroutineleak profile: %.400s", rec.Body.String())
	}
}

// goroutineleak, the POSITIVE claim, which nothing here asserted.
//
// Go 1.27 made the goroutine-leak profile generally available and pprof.Index
// derives the profile name from the request path, so the endpoint arrived with
// the toolchain rather than with a registration — which is exactly why it needs
// a test: nothing in this package mentions the name, so there is no compile
// error and no missing symbol if a future toolchain drops it or renames it. The
// only evidence the wire claim in pprof.go holds is a request.
//
// The three forms are asserted separately because an operator and a tool consume
// different ones: ?debug=1 is the text summary an agent inside the container
// reads with curl, ?debug=2 is the full stack form, and the bare path is the
// gzipped protobuf `go tool pprof <url>` fetches.
//
// The COUNT is deliberately not asserted as nonzero. Detection is
// reachability-based, so a leak the test planted would have to become
// unreachable and survive a GC cycle to be reported, and pprof.go's own note
// says `total 0` is not a statement that nothing leaked — the useful reading is
// a diff across an operation. What is deterministic, and what this pins, is that
// the endpoint answers through the mount and that its header line is the
// documented `goroutineleak profile: total <n>` shape.
func TestPprof_GoroutineLeakProfileAnswersOnLoopback(t *testing.T) {
	t.Run("debug=1 is the text summary", func(t *testing.T) {
		rec := pprofRequest(t, pprofPath+"goroutineleak?debug=1", "127.0.0.1:54321", "localhost:9847")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200: %.200s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
			t.Errorf("Content-Type = %q, want text/plain", got)
		}
		const prefix = "goroutineleak profile: total "
		body := rec.Body.String()
		if !strings.HasPrefix(body, prefix) {
			t.Fatalf("body = %q, want it to open %q", body, prefix)
		}
		total := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(body, "\n", 2)[0], prefix))
		if _, err := strconv.Atoi(total); err != nil {
			t.Errorf("total = %q, want an integer: %v", total, err)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
	})

	t.Run("debug=2 carries stack frames", func(t *testing.T) {
		rec := pprofRequest(t, pprofPath+"goroutineleak?debug=2", "127.0.0.1:54321", "localhost:9847")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200: %.200s", rec.Code, rec.Body.String())
		}
		if body := rec.Body.String(); !strings.Contains(body, ".go:") {
			t.Errorf("body carries no stack frames: %.400s", body)
		}
	})

	t.Run("bare path is the protobuf go tool pprof fetches", func(t *testing.T) {
		rec := pprofRequest(t, pprofPath+"goroutineleak", "127.0.0.1:54321", "localhost:9847")
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200: %.200s", rec.Code, rec.Body.String())
		}
		// pprof serialises to a gzipped protobuf. Decompressing is the cheapest
		// proof this is a real profile rather than an index page or an error
		// body that happened to answer 200.
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("body is not the gzipped protobuf go tool pprof expects: %v", err)
		}
		defer zr.Close()
		if _, err := io.Copy(io.Discard, zr); err != nil {
			t.Errorf("protobuf body did not decompress cleanly: %v", err)
		}
	})

	// Behind the SAME gate as every other profile, which is the security half:
	// a leak profile names every function on every parked stack.
	t.Run("refused off loopback", func(t *testing.T) {
		rec := pprofRequest(t, pprofPath+"goroutineleak?debug=2", "10.0.0.5:1", "localhost:9847")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("code = %d, want 403", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "goroutineleak profile") {
			t.Errorf("the refusal leaked the profile: %.400s", rec.Body.String())
		}
	})
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
