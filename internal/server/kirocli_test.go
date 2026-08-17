package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/pinstall/v2"
)

// TestKiroReasonTextIsTheClientContract pins the four reason literals
// /api/health puts on the wire. They are not internal wording: the browser
// banner (static-src/runtime-health.ts) prefix-matches "kiro-cli" to decide the
// verdict is a kiro-cli one AT ALL, then keys its per-state copy on each literal
// in full — so a rename here degrades every named state to the terminal
// "install failed and its retries are exhausted" copy, silently and only in the
// browser. Change a string here and change STATES there in the same commit.
//
// It also pins the mapping's totality: a reason this build cannot name (a value
// a future pinstall adds) must still read as blocking, not as ready.
func TestKiroReasonTextIsTheClientContract(t *testing.T) {
	tests := map[pinstall.Reason]string{
		pinstall.ReasonReady:       "",
		pinstall.ReasonInstalling:  "kiro-cli installing",
		pinstall.ReasonRetrying:    "kiro-cli install retrying",
		pinstall.ReasonUnavailable: "kiro-cli unavailable",
		pinstall.ReasonAssertion:   "kiro-cli required settings not enforced",
	}
	for why, want := range tests {
		if got := kiroReasonText(why); got != want {
			t.Errorf("kiroReasonText(%s) = %q, want %q (runtime-health.ts keys its banner copy on this literal)", why, got, want)
		}
	}
	unknown := pinstall.Reason(200)
	if got := kiroReasonText(unknown); got != reasonUnavailable {
		t.Errorf("kiroReasonText(unknown) = %q, want %q: an unnameable state still blocks chats", got, reasonUnavailable)
	}
	for why, want := range tests {
		if why == pinstall.ReasonReady {
			continue
		}
		if !strings.HasPrefix(want, "kiro-cli") {
			t.Errorf("reason %q does not carry the kiro-cli prefix the client matches on", want)
		}
	}
}

// TestHandleKiroRescanReportsTheResultingReadiness pins the repair hook's two
// answers. It exists so an operator who fixes an install inside the container
// learns whether it took WITHOUT polling /api/health, and so a failed rescan
// reports the manager's own verdict rather than the error text, which can name a
// path on the volume.
func TestHandleKiroRescanReportsTheResultingReadiness(t *testing.T) {
	tests := map[string]struct {
		ok         bool
		rescanErr  error
		ready      func() (bool, pinstall.Reason)
		wantStatus int
		wantBody   string
	}{
		"a successful rescan reports ok": {
			ok:         true,
			ready:      func() (bool, pinstall.Reason) { return true, pinstall.ReasonReady },
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok"}`,
		},
		"a failed rescan carries the manager's reason": {
			rescanErr:  pinstall.ErrNoVersion,
			ready:      func() (bool, pinstall.Reason) { return false, pinstall.ReasonAssertion },
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"unready","reason":"` + reasonSettings + `"}`,
		},
		"with no readiness verdict it falls back to unavailable": {
			rescanErr:  errors.New("/config/tools/kiro-cli-versions/2.14.2: permission denied"),
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"unready","reason":"` + reasonUnavailable + `"}`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := &Server{
				kiroReady:  tc.ready,
				kiroRescan: func(context.Context) (bool, error) { return tc.ok, tc.rescanErr },
			}
			rec := httptest.NewRecorder()
			s.handleKiroRescan(rec, httptest.NewRequest(http.MethodPost, kiroRescanPath, nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
				t.Errorf("body = %q, want %q", got, tc.wantBody)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store: a rescan verdict is never valid a moment later", got)
			}
		})
	}
}

// TestHandleKiroRescanNeverLeaksTheErrorText pins the disclosure rule: the hook
// answers on an unauthenticated port, and a rescan failure can carry a
// filesystem path from the volume. The manager has already logged the specific
// fault, so the response reports the verdict only.
func TestHandleKiroRescanNeverLeaksTheErrorText(t *testing.T) {
	const secretish = "/config/home/.aws/sso/cache/token.json"
	s := &Server{
		kiroReady:  func() (bool, pinstall.Reason) { return false, pinstall.ReasonUnavailable },
		kiroRescan: func(context.Context) (bool, error) { return false, errors.New("cannot read " + secretish) },
	}
	rec := httptest.NewRecorder()
	s.handleKiroRescan(rec, httptest.NewRequest(http.MethodPost, kiroRescanPath, nil))

	if strings.Contains(rec.Body.String(), secretish) {
		t.Errorf("the response echoed the error text: %s", rec.Body.String())
	}
}

// TestLoopbackOnlyAdmitsOnlyInContainerCallers pins the repair hook's only
// boundary. A rescan spawns bounded kiro-cli subprocesses, so reachability from
// the LAN would hand an unauthenticated caller a process-spawn lever; and BOTH
// ends have to be loopback, because a DNS-rebound page arrives with a loopback
// socket peer and the attacker's own Host.
func TestLoopbackOnlyAdmitsOnlyInContainerCallers(t *testing.T) {
	tests := map[string]struct {
		remote     string
		host       string
		wantStatus int
	}{
		"loopback peer and loopback host": {remote: "127.0.0.1:54321", host: "localhost:9847", wantStatus: http.StatusOK},
		"loopback IP host":                {remote: "127.0.0.1:54321", host: "127.0.0.1:9847", wantStatus: http.StatusOK},
		"IPv6 loopback":                   {remote: "[::1]:54321", host: "[::1]:9847", wantStatus: http.StatusOK},
		"LAN peer":                        {remote: "192.168.1.20:54321", host: "localhost:9847", wantStatus: http.StatusForbidden},
		"rebound host on a loopback peer": {remote: "127.0.0.1:54321", host: "evil.example.com", wantStatus: http.StatusForbidden},
		"malformed peer fails closed":     {remote: "not-an-address", host: "localhost:9847", wantStatus: http.StatusForbidden},
		"malformed host fails closed":     {remote: "127.0.0.1:54321", host: "http://localhost:9847/x", wantStatus: http.StatusForbidden},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			admitted := false
			h := loopbackOnly(kiroRescanSurface,
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) { admitted = true }))
			req := httptest.NewRequest(http.MethodPost, kiroRescanPath, nil)
			req.RemoteAddr = tc.remote
			req.Host = tc.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			switch tc.wantStatus {
			case http.StatusOK:
				if !admitted {
					t.Errorf("refused an in-container caller (%s / %s); the documented repair path would not work", tc.remote, tc.host)
				}
			default:
				if admitted {
					t.Fatalf("admitted %s / %s", tc.remote, tc.host)
				}
				if rec.Code != http.StatusForbidden {
					t.Errorf("status = %d, want 403", rec.Code)
				}
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("body not JSON: %v; body=%s", err, rec.Body.String())
				}
				if body["error"] == "" {
					t.Errorf("body = %v, want the canonical error envelope", body)
				}
				// The refusal names THIS surface. It is the whole of what a
				// rejected caller learns, and the same middleware now backs
				// /debug/pprof/ too, so a generic message would send an
				// operator to retry the wrong path.
				if !strings.Contains(body["error"], kiroRescanSurface) {
					t.Errorf("refusal = %q, want it to name %q", body["error"], kiroRescanSurface)
				}
				if _, isVerdict := body["status"]; isVerdict {
					t.Error("a rejected request answered with the readiness envelope; a caller could read the 403 as a verdict")
				}
			}
		})
	}
}
