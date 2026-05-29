package server

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

func helloMux() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestSecurityMiddleware_SetsCSP(t *testing.T) {
	h := securityMiddleware(helloMux())
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

	h := securityMiddleware(helloMux())
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
	h := securityMiddleware(helloMux())

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

// TestCSPImportMapHash guards against CSP drift: if the inline
// <script type="importmap"> in static/index.html is edited without
// updating cspPolicy, the browser silently blocks xterm.js module
// resolution (the shell tab goes dark with no user-visible error).
// Recompute the hash from the embedded HTML and assert the CSP
// contains it verbatim.
func TestCSPImportMapHash(t *testing.T) {
	html, err := os.ReadFile("../../static/index.html")
	if err != nil {
		t.Fatalf("read static/index.html: %v", err)
	}
	// Match the importmap block exactly as the browser sees it:
	// everything between the opening <script type="importmap"> tag
	// and the matching </script>. DOTALL for multi-line content.
	re := regexp.MustCompile(`(?s)<script type="importmap">(.*?)</script>`)
	m := re.FindSubmatch(html)
	if m == nil {
		t.Fatal("no <script type=\"importmap\"> block in static/index.html")
	}
	sum := sha256.Sum256(m[1])
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(cspPolicy, want) {
		t.Errorf("cspPolicy missing current importmap hash.\n"+
			"expected script-src token: %s\n"+
			"cspPolicy: %s\n"+
			"fix: update the sha256-... value in security.go's script-src directive",
			want, cspPolicy)
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

	h := securityMiddleware(helloMux())

	f.Fuzz(func(t *testing.T, method, origin, host string) {
		if method == "" {
			return
		}
		req := httptest.NewRequest(method, "http://"+host+"/x", http.NoBody)
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
	directives := make(map[string]string)
	for part := range strings.SplitSeq(cspPolicy, "; ") {
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

	// script-src must contain exactly one sha256 token.
	scriptSrc := directives["script-src"]
	sha256Re := regexp.MustCompile(`'sha256-[A-Za-z0-9+/=]+'`)
	matches := sha256Re.FindAllString(scriptSrc, -1)
	if len(matches) != 1 {
		t.Errorf("script-src has %d sha256 tokens, want exactly 1: %q", len(matches), scriptSrc)
	}
}
