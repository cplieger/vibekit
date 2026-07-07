package server

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func helloMux() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// testCSP is a fixed CSP string for the security middleware tests that
// don't care about importmap hashing — they assert structural
// properties (origin checks, headers set, etc.) of the middleware.
// Production uses buildCSPPolicy(staticFS) instead.
func testCSP() string { return fallbackCSPPolicy() }

func TestSecurityMiddleware_SetsCSP(t *testing.T) {
	h := securityMiddleware(testCSP(), helloMux())
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

	h := securityMiddleware(testCSP(), helloMux())
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

func BenchmarkSecurityMiddleware(b *testing.B) {
	h := securityMiddleware(testCSP(), helloMux())

	b.Run("GET_headers_only", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", http.NoBody)
		rec := httptest.NewRecorder()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
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
		for range b.N {
			rec.Body.Reset()
			h.ServeHTTP(rec, req)
		}
	})
}

// TestImportMapHashToken: extracts the inline importmap from the real
// embedded HTML and confirms the produced sha256 token matches a manual
// recomputation. This guards the parsing + hashing logic, not any
// hardcoded literal.
func TestImportMapHashToken(t *testing.T) {
	html, err := os.ReadFile(filepath.Join("..", "..", "static", "index.html"))
	if err != nil {
		t.Fatalf("read static/index.html: %v", err)
	}
	got, err := importMapHashToken(html)
	if err != nil {
		t.Fatalf("importMapHashToken: %v", err)
	}
	// Manual recomputation as a sanity check: the function does what
	// the docstring says, against the real file.
	re := regexp.MustCompile(`(?s)<script type="importmap">(.*?)</script>`)
	m := re.FindSubmatch(html)
	if m == nil {
		t.Fatal("no importmap block in test fixture")
	}
	sum := sha256.Sum256(m[1])
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if got != want {
		t.Errorf("importMapHashToken = %q, want %q", got, want)
	}
}

// TestImportMapHashToken_Missing: index.html without an importmap block
// returns an error rather than a misleading empty hash.
func TestImportMapHashToken_Missing(t *testing.T) {
	if _, err := importMapHashToken([]byte("<html><body>no importmap here</body></html>")); err == nil {
		t.Error("expected error for HTML with no importmap, got nil")
	}
}

// TestBuildCSPPolicy: against a synthetic FS, the produced CSP contains
// the expected sha256 tokens derived from BOTH inline <head> scripts (the
// importmap and the anti-FOUC theme-init). Covers the whole startup path
// used by ListenAndServe.
func TestBuildCSPPolicy(t *testing.T) {
	importMap := `{"imports":{"x":"/y.mjs"}}`
	themeInit := `(function(){document.documentElement.setAttribute("data-theme","dark");})();`
	html := []byte(`<html><script type="importmap">` + importMap + `</script>` +
		`<script data-theme-init>` + themeInit + `</script></html>`)
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: html}}

	policy, err := buildCSPPolicy(staticFS)
	if err != nil {
		t.Fatalf("buildCSPPolicy: %v", err)
	}
	for _, body := range []string{importMap, themeInit} {
		sum := sha256.Sum256([]byte(body))
		want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !strings.Contains(policy, want) {
			t.Errorf("policy missing computed hash.\n  policy: %s\n  want token: %s", policy, want)
		}
	}
}

// TestBuildCSPPolicy_MissingThemeInit: index.html with an importmap but no
// theme-init block fails construction — a required inline script would
// otherwise be blocked by the CSP at runtime.
func TestBuildCSPPolicy_MissingThemeInit(t *testing.T) {
	html := []byte(`<html><script type="importmap">{"imports":{}}</script></html>`)
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: html}}
	if _, err := buildCSPPolicy(staticFS); err == nil {
		t.Error("expected error when theme-init block is missing, got nil")
	}
}

// TestThemeInitHashToken: extracts the inline theme-init script from the real
// embedded HTML and confirms the produced sha256 token matches a manual
// recomputation. Guards the parsing + hashing logic, not any hardcoded literal.
func TestThemeInitHashToken(t *testing.T) {
	html, err := os.ReadFile(filepath.Join("..", "..", "static", "index.html"))
	if err != nil {
		t.Fatalf("read static/index.html: %v", err)
	}
	got, err := themeInitHashToken(html)
	if err != nil {
		t.Fatalf("themeInitHashToken: %v", err)
	}
	re := regexp.MustCompile(`(?s)<script data-theme-init>(.*?)</script>`)
	m := re.FindSubmatch(html)
	if m == nil {
		t.Fatal("no theme-init block in test fixture")
	}
	sum := sha256.Sum256(m[1])
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if got != want {
		t.Errorf("themeInitHashToken = %q, want %q", got, want)
	}
}

// TestThemeInitHashToken_Missing: HTML without a theme-init block returns an
// error rather than a misleading empty hash.
func TestThemeInitHashToken_Missing(t *testing.T) {
	if _, err := themeInitHashToken([]byte("<html><body>no theme init here</body></html>")); err == nil {
		t.Error("expected error for HTML with no theme-init block, got nil")
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

	h := securityMiddleware(testCSP(), helloMux())

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
	policy := testCSP()
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
