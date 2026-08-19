package server

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/webhttp"
)

func helloMux() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// fallbackCSPPolicy assembles the CSP with the script-src hash slot relaxed to
// 'unsafe-inline' instead of a pinned hash. It lives in the TEST file by design:
// production always goes through buildCSPPolicy against the real embedded
// index.html and never relaxes script-src, so a relaxed builder must not be
// reachable from — or even compiled into — the production binary. Tests that
// assert structural properties of the middleware (origin checks, headers set)
// rather than the inline-script hashing use it as their policy stand-in.
func fallbackCSPPolicy() string {
	return fmt.Sprintf(cspTemplate, "'unsafe-inline'")
}

func TestSecurityMiddleware_SetsCSP(t *testing.T) {
	h := securityMiddleware(fallbackCSPPolicy(), nil, helloMux())
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP missing default-src: %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing frame-ancestors: %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("X-Content-Type-Options not set")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestSecurityMiddleware_OriginCheck(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		origin   string
		wantCode int
	}{
		{"GET cross-origin allowed", http.MethodGet, "http://attacker.example", http.StatusOK},
		{"POST same-origin allowed", http.MethodPost, "http://example.com", http.StatusOK},
		{"POST missing origin allowed", http.MethodPost, "", http.StatusOK},
		{"POST cross-origin blocked", http.MethodPost, "http://attacker.example", http.StatusForbidden},
		{"PUT cross-origin blocked", http.MethodPut, "http://attacker.example", http.StatusForbidden},
		{"PATCH cross-origin blocked", http.MethodPatch, "http://attacker.example", http.StatusForbidden},
		{"DELETE cross-origin blocked", http.MethodDelete, "http://attacker.example", http.StatusForbidden},
	}

	h := securityMiddleware(fallbackCSPPolicy(), nil, helloMux())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *strings.Reader
			if tc.method != http.MethodGet {
				body = strings.NewReader("")
			}
			var req *http.Request
			if body != nil {
				req = httptest.NewRequest(tc.method, "http://example.com/x", body)
			} else {
				req = httptest.NewRequest(tc.method, "http://example.com/", http.NoBody)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("got status %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

// TestSecurityMiddleware_HostAllowlist pins the ALLOWED_HOSTS
// anti-DNS-rebinding gate inside the real security middleware: a rebinding
// attack makes an attacker-controlled hostname resolve to this server, so
// Origin and Host AGREE and the CSRF layer alone admits the request — the
// exact-Host allowlist must reject it (with the baseline security headers
// still applied), while an allowed Host passes through to the CSRF check
// (which still rejects a forged cross-origin POST). The loopback peer+Host
// carve-out keeps the image's own healthcheck working under a browser-facing
// allowlist, a forged loopback Host from a remote peer stays rejected, and a
// nil policy is a pass-through (unset ALLOWED_HOSTS stays backward
// compatible).
func TestSecurityMiddleware_HostAllowlist(t *testing.T) {
	policy, invalid := webhttp.ParseHostList([]string{"vibekit.example.com"},
		webhttp.WithLoopbackExempt(),
		webhttp.WithHostAllowlistError("",
			"host not allowed; add it to ALLOWED_HOSTS to serve this hostname"))
	if len(invalid) > 0 {
		t.Fatalf("test allowlist has invalid entries: %v", invalid)
	}
	h := securityMiddleware(fallbackCSPPolicy(), policy, helloMux())

	do := func(method, host, origin, remoteAddr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+host+"/x", strings.NewReader(""))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if remoteAddr != "" {
			req.RemoteAddr = remoteAddr
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("rebound host rejected even though Origin agrees", func(t *testing.T) {
		rec := do(http.MethodPost, "attacker.evil:8080", "http://attacker.evil:8080", "192.168.1.50:44444")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (Origin/Host agreement must not admit a rebound host)", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "ALLOWED_HOSTS") {
			t.Errorf("403 body = %q, want it to name ALLOWED_HOSTS", rec.Body.String())
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error("host-gate 403 lost the baseline security headers")
		}
	})

	t.Run("allowed host passes through to the handler", func(t *testing.T) {
		rec := do(http.MethodPost, "vibekit.example.com", "http://vibekit.example.com", "192.168.1.50:44444")
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("allowed host still gets the CSRF check", func(t *testing.T) {
		rec := do(http.MethodPost, "vibekit.example.com", "http://attacker.evil", "192.168.1.50:44444")
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (the host gate must not swallow the cross-origin rejection)", rec.Code)
		}
	})

	t.Run("healthcheck shape: loopback peer + loopback Host admitted", func(t *testing.T) {
		rec := do(http.MethodGet, "127.0.0.1:8080", "", "127.0.0.1:54321")
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (the loopback carve-out must keep the container healthcheck working)", rec.Code)
		}
	})

	t.Run("forged loopback Host from remote peer rejected", func(t *testing.T) {
		rec := do(http.MethodGet, "127.0.0.1:8080", "", "192.168.1.50:44444")
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (a remote peer forging a loopback Host must not ride the carve-out)", rec.Code)
		}
	})

	t.Run("nil policy is a pass-through", func(t *testing.T) {
		open := securityMiddleware(fallbackCSPPolicy(), nil, helloMux())
		req := httptest.NewRequest(http.MethodGet, "http://anything.example/x", http.NoBody)
		rec := httptest.NewRecorder()
		open.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (unset ALLOWED_HOSTS must stay backward compatible)", rec.Code)
		}
	})
}

func BenchmarkSecurityMiddleware(b *testing.B) {
	h := securityMiddleware(fallbackCSPPolicy(), nil, helloMux())

	b.Run("GET_headers_only", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", http.NoBody)
		rec := httptest.NewRecorder()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			rec.Body.Reset()
			h.ServeHTTP(rec, req)
		}
	})

	b.Run("POST_same_origin", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/chat", http.NoBody)
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			rec.Body.Reset()
			h.ServeHTTP(rec, req)
		}
	})
}

// TestBuildCSPPolicy: against a synthetic FS, the produced CSP contains
// the expected sha256 token derived from the page's one inline <head>
// script (the anti-FOUC theme-init; the importmap died with the
// pre-bundler pipeline). Covers the whole startup path used by
// ListenAndServe. The importmap-free fixture also pins the new
// contract: HTML with no importmap builds a valid policy.
func TestBuildCSPPolicy(t *testing.T) {
	themeInit := `(function(){document.documentElement.setAttribute("data-theme","dark");})();`
	html := []byte(`<html>` +
		`<script data-theme-init>` + themeInit + `</script>` +
		`<script type="module" src="/app.js"></script></html>`)
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: html}}

	policy, err := buildCSPPolicy(staticFS)
	if err != nil {
		t.Fatalf("buildCSPPolicy: %v", err)
	}
	sum := sha256.Sum256([]byte(themeInit))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(policy, want) {
		t.Errorf("policy missing computed hash.\n  policy: %s\n  want token: %s", policy, want)
	}
}

// TestBuildCSPPolicy_NoInlineScript: index.html with no inline script at all
// fails construction — the required theme-init block is missing, and a CSP
// built anyway would block it at runtime.
func TestBuildCSPPolicy_NoInlineScript(t *testing.T) {
	html := []byte(`<html><script type="module" src="/app.js"></script></html>`)
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: html}}
	if _, err := buildCSPPolicy(staticFS); err == nil {
		t.Error("expected error when the page carries no inline script, got nil")
	}
}

// TestBuildCSPPolicy_MultipleInlineScripts: a second inline script fails
// construction. The page carries exactly one inline script by contract; a
// new one must be consciously reviewed (and the exactly-one check updated)
// rather than silently granted CSP allowance.
func TestBuildCSPPolicy_MultipleInlineScripts(t *testing.T) {
	html := []byte(`<html>` +
		`<script data-theme-init>(function(){})();</script>` +
		`<script>console.log("unreviewed")</script></html>`)
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: html}}
	if _, err := buildCSPPolicy(staticFS); err == nil {
		t.Error("expected error when the page carries two inline scripts, got nil")
	}
}

// TestBuildCSPPolicy_RealEmbeddedHTML: builds the policy from the real
// committed index.html and verifies the token against an independent
// recomputation (regex extraction + manual sha256), guarding the library
// delegation end-to-end rather than any hardcoded literal.
func TestBuildCSPPolicy_RealEmbeddedHTML(t *testing.T) {
	html, err := os.ReadFile(filepath.Join("..", "..", "static", "index.html"))
	if err != nil {
		t.Fatalf("read static/index.html: %v", err)
	}
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: html}}
	policy, err := buildCSPPolicy(staticFS)
	if err != nil {
		t.Fatalf("buildCSPPolicy over the real index.html: %v", err)
	}
	re := regexp.MustCompile(`(?s)<script data-theme-init>(.*?)</script>`)
	m := re.FindSubmatch(html)
	if m == nil {
		t.Fatal("no theme-init block in static/index.html")
	}
	sum := sha256.Sum256(m[1])
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(policy, want) {
		t.Errorf("policy missing the theme-init hash.\n  policy: %s\n  want token: %s", policy, want)
	}
}

func TestBuildCSPPolicy_NilFS(t *testing.T) {
	if _, err := buildCSPPolicy(nil); err == nil {
		t.Error("expected error for nil FS")
	}
}

func FuzzSecurityMiddleware_OriginCheck(f *testing.F) {
	f.Add("POST", "http://example.com", "example.com")
	f.Add("GET", "http://attacker.example", "example.com")
	f.Add("POST", "http://attacker.example", "example.com")
	f.Add("POST", "", "example.com")
	f.Add("DELETE", "http://example.com:8080", "example.com")
	f.Add("PUT", "null", "example.com")
	f.Add("PATCH", "http://example.com", "example.com:443")

	h := securityMiddleware(fallbackCSPPolicy(), nil, helloMux())

	f.Fuzz(func(t *testing.T, method, origin, host string) {
		if method == "" {
			return
		}
		// Skip methods that would cause httptest.NewRequest to panic
		// (contains spaces, control characters, or other invalid bytes).
		for _, b := range []byte(method) {
			if b <= 0x20 || b == 0x7f {
				t.Skip("invalid HTTP method character")
			}
		}
		if host == "" {
			t.Skip("empty host")
		}
		for _, b := range []byte(host) {
			if b < 0x20 || b == 0x7f {
				t.Skip("control char in host")
			}
		}
		req, err := http.NewRequest(method, "http://"+host+"/x", http.NoBody)
		if err != nil {
			t.Skip("invalid request:", err)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		// Invariant 1: no panics (implicit).
		// Invariant 2: GET always 200.
		if method == http.MethodGet && rec.Code != http.StatusOK {
			t.Errorf("GET with origin=%q got %d, want 200", origin, rec.Code)
		}
	})
}

func TestCSPPolicy_StructuralInvariants(t *testing.T) {
	policy := fallbackCSPPolicy()
	directives := make(map[string]string)
	for part := range strings.SplitSeq(policy, "; ") {
		fields := strings.SplitN(part, " ", 2)
		name := fields[0]
		if _, dup := directives[name]; dup {
			t.Errorf("duplicate directive: %s", name)
		}
		val := ""
		if len(fields) > 1 {
			val = fields[1]
		}
		directives[name] = val
	}

	required := []string{"default-src", "script-src", "style-src", "connect-src", "frame-ancestors", "img-src"}
	for _, d := range required {
		if _, ok := directives[d]; !ok {
			t.Errorf("missing required directive: %s", d)
		}
	}

	if fa := directives["frame-ancestors"]; fa != "'none'" {
		t.Errorf("frame-ancestors = %q, want 'none'", fa)
	}

	if ds := directives["default-src"]; ds != "'self'" {
		t.Errorf("default-src = %q, want 'self'", ds)
	}
}
