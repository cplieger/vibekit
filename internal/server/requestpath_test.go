package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/webhttp"
)

// The fixture mirrors the real mount shape ListenAndServe builds: the SPA/static
// catch-all at "/" (the REAL spaHandler, not a stub, so the static leg's
// before/after behaviour is the production one), a plain-path API route
// (/api/health, the baked probe), a method-pattern mutating route
// (POST /api/kiro-cli/rescan, the README's repair call), an exact+subtree pair
// (/api/chats and /api/chats/, the shape every vibekit subtree uses), and a
// wildcard route (DELETE /api/knowledge/{name}) so the decoded-path verdict is
// exercised against a real path parameter.
const (
	indexBody = "<html>index</html>"
	assetBody = "console.log('asset')"
)

func requestPathFixtureFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":     {Data: []byte(indexBody)},
		"app.js":         {Data: []byte(assetBody)},
		"chunks/lazy.js": {Data: []byte(assetBody)},
	}
}

// requestPathMux returns the fixture mux plus a pointer that records which
// handler ran, so a refusal can be distinguished from a handler that answered
// 400 itself.
func requestPathMux() (*http.ServeMux, *string) {
	var reached string
	mark := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			reached = name
			if v := r.PathValue("name"); v != "" {
				reached = name + ":" + v
			}
			api.WriteJSON(w, healthBody{Status: "ok"})
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/", spaHandler(requestPathFixtureFS()))
	mux.HandleFunc("/api/health", mark("health"))
	mux.Handle("POST "+kiroRescanPath, mark("rescan"))
	mux.HandleFunc("/api/chats", mark("chats"))
	mux.HandleFunc("/api/chats/", mark("chat-one"))
	mux.HandleFunc("DELETE /api/knowledge/{name}", mark("knowledge"))
	return mux, &reached
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://example.com"+target, http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestCanonicalAPIPath is the behaviour contract of the guard: on the API
// surface a non-canonical spelling is REFUSED (400, vibekit's bare error
// envelope, handler never reached) where ServeMux would have answered 307 — a
// status a `curl -f` sender reads as success — while a canonical spelling
// reaches its handler untouched and the static/SPA mount keeps every redirect
// and fallback it had.
func TestCanonicalAPIPath(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		target   string
		wantCode int
		// wantReached is the handler name the mux must have run ("" = none).
		wantReached string
		wantLoc     string
		wantBody    string
	}{
		// --- the API surface: canonical reaches the handler ---
		{
			name:   "canonical probe reaches the health handler",
			method: http.MethodGet, target: "/api/health",
			wantCode: http.StatusOK, wantReached: "health",
		},
		{
			name:   "canonical repair call reaches the mutating handler",
			method: http.MethodPost, target: kiroRescanPath,
			wantCode: http.StatusOK, wantReached: "rescan",
		},
		{
			name:   "a dot INSIDE a path segment is canonical and routes",
			method: http.MethodDelete, target: "/api/knowledge/my.base",
			wantCode: http.StatusOK, wantReached: "knowledge:my.base",
		},
		{
			name:   "a segment ENDING in dots is canonical and routes",
			method: http.MethodGet, target: "/api/chats/c-1..",
			wantCode: http.StatusOK, wantReached: "chat-one",
		},

		// --- the API surface: non-canonical is refused, not redirected ---
		{
			name:   "doubled slash on the probe is refused",
			method: http.MethodGet, target: "//api/health",
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},
		{
			name:   "dot segment on the probe is refused",
			method: http.MethodGet, target: "/api/./health",
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},
		{
			name:   "dotdot segment on the probe is refused",
			method: http.MethodGet, target: "/api/x/../health",
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},
		{
			name:   "doubled slash on the mutating repair call is refused",
			method: http.MethodPost, target: "/" + kiroRescanPath,
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},
		{
			name:   "dotdot on the mutating repair call is refused",
			method: http.MethodPost, target: "/api/kiro-cli/../kiro-cli/rescan",
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},
		// The encoded spellings are the ones ServeMux does NOT redirect: they
		// are canonical on the wire, match no pattern once decoded, and so land
		// on the SPA catch-all with 200 + index.html. Feeding the DECODED path
		// to the library is what converts those into refusals.
		{
			name:   "encoded dotdot below the prefix is refused (would be 200 index.html)",
			method: http.MethodGet, target: "/api/%2e%2e/api/health",
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},
		{
			name:   "encoded dotdot ABOVE the prefix is refused (clean lands on the API)",
			method: http.MethodGet, target: "/%2e%2e/api/health",
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},
		{
			name:   "a path parameter that decodes to dotdot never reaches the handler",
			method: http.MethodDelete, target: "/api/knowledge/%2e%2e",
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},
		// A trailing ".." pops the segment before it, so this one's CLEAN is
		// "/api" — outside the guarded prefix. It is refused on the RAW leg of
		// the scope test, which is why that leg exists.
		{
			name:   "a trailing dotdot that cleans ABOVE the prefix is refused",
			method: http.MethodGet, target: "/api/health/..",
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},

		// --- the static/SPA mount: unchanged, redirects included ---
		{
			name:   "static asset still serves",
			method: http.MethodGet, target: "/app.js",
			wantCode: http.StatusOK, wantBody: assetBody,
		},
		{
			name:   "doubled slash on an asset still gets ServeMux's redirect",
			method: http.MethodGet, target: "//app.js",
			wantCode: http.StatusTemporaryRedirect, wantLoc: "/app.js",
		},
		{
			name:   "dot segment on an asset still gets ServeMux's redirect",
			method: http.MethodGet, target: "/./chunks/lazy.js",
			wantCode: http.StatusTemporaryRedirect, wantLoc: "/chunks/lazy.js",
		},
		{
			name:   "a directory path still falls back to index.html",
			method: http.MethodGet, target: "/chunks/",
			wantCode: http.StatusOK, wantBody: indexBody,
		},
		{
			name:   "a client route still falls back to index.html",
			method: http.MethodGet, target: "/chat/abc",
			wantCode: http.StatusOK, wantBody: indexBody,
		},
		{
			name:   "an /api spelling whose CLEAN lands on static is still refused",
			method: http.MethodGet, target: "/api/../app.js",
			wantCode: http.StatusBadRequest, wantBody: msgNonCanonicalPath,
		},
		// The trailing-slash class is outside CanonicalRequestPath's claim, and
		// this pins that the guard leaves it exactly where it was: /api/health/
		// is canonical, matches no pattern, and falls to the SPA as it always
		// did.
		{
			name:   "a canonical trailing slash on an API route is untouched",
			method: http.MethodGet, target: "/api/health/",
			wantCode: http.StatusOK, wantBody: indexBody,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, reached := requestPathMux()
			rec := do(canonicalAPIPath(mux), tc.method, tc.target)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if *reached != tc.wantReached {
				t.Errorf("handler reached = %q, want %q", *reached, tc.wantReached)
			}
			if got := rec.Header().Get("Location"); got != tc.wantLoc {
				t.Errorf("Location = %q, want %q", got, tc.wantLoc)
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

// TestCanonicalAPIPath_RefusalIsVibekitsEnvelope: the refusal must be the
// repo's bare {"error": …} shape, and — the load-bearing half — a status a
// non-following sender reads as failure. `curl -f` keys on >= 400, which is why
// the whole guard exists rather than leaving the 307 in place.
func TestCanonicalAPIPath_RefusalIsVibekitsEnvelope(t *testing.T) {
	mux, _ := requestPathMux()
	rec := do(canonicalAPIPath(mux), http.MethodPost, "/"+kiroRescanPath)

	if rec.Code < 400 {
		t.Errorf("status = %d, want >= 400 so a `curl -f` sender exits non-zero", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, api.MIMETypeJSON) {
		t.Errorf("Content-Type = %q, want %s", got, api.MIMETypeJSON)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"`+msgNonCanonicalPath+`"}` {
		t.Errorf("body = %s, want vibekit's bare error envelope", got)
	}
	// The refusal names the class, never the caller's bytes.
	if strings.Contains(rec.Body.String(), "rescan") {
		t.Errorf("body = %s, want no echo of the request path", rec.Body.String())
	}
}

// TestMiddlewareStack_GuardOrder pins the PLACEMENT, read off the production
// stack (s.middlewareStack) rather than a hand-assembled copy: the canonical
// -path gate runs inside the WT_ALLOWED_HOSTS allowlist and inside the CSRF check,
// so neither 403 is shadowed by a 400 about spelling, and it runs outside the
// routes it protects.
func TestMiddlewareStack_GuardOrder(t *testing.T) {
	policy, invalid := webhttp.ParseHostList([]string{"vibekit.example.com"},
		webhttp.WithLoopbackExempt(),
		webhttp.WithHostAllowlistError("", "host not allowed; add it to WT_ALLOWED_HOSTS to serve this hostname"))
	if len(invalid) > 0 {
		t.Fatalf("test allowlist has invalid entries: %v", invalid)
	}
	mux, reached := requestPathMux()
	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	s := New(WithHostPolicy(policy))
	h := webhttp.Chain(mux, s.middlewareStack(fallbackCSPPolicy(), idem)...)

	post := func(host, origin string) *httptest.ResponseRecorder {
		// A non-canonical spelling of the mutating repair route: whichever gate
		// answers first decides the status.
		req := httptest.NewRequest(http.MethodPost, "http://"+host+"/"+kiroRescanPath, strings.NewReader(""))
		req.RemoteAddr = "192.168.1.50:44444"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("host allowlist wins over the path refusal", func(t *testing.T) {
		rec := post("attacker.evil", "http://attacker.evil")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (the host gate must stay outside the path guard)", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "WT_ALLOWED_HOSTS") {
			t.Errorf("403 body = %q, want the host-gate refusal", rec.Body.String())
		}
	})

	t.Run("CSRF check wins over the path refusal", func(t *testing.T) {
		rec := post("vibekit.example.com", "http://attacker.evil")
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (the CSRF check must stay outside the path guard)", rec.Code)
		}
	})

	t.Run("past both gates the path guard refuses before any route", func(t *testing.T) {
		rec := post("vibekit.example.com", "http://vibekit.example.com")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), msgNonCanonicalPath) {
			t.Errorf("body = %q, want %q", rec.Body.String(), msgNonCanonicalPath)
		}
		if *reached != "" {
			t.Errorf("handler %q ran; the guard must refuse outside the routing", *reached)
		}
		// The baseline security headers still apply to the refusal (the guard is
		// inside SecurityHeaders).
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error("the path refusal lost the baseline security headers")
		}
	})
}

// FuzzCanonicalAPIPath asserts three invariants against the unguarded mux as an
// oracle, for any request target:
//
//  1. No third outcome. The guarded chain is either transparent (same status
//     and Location as the bare mux) or exactly the 400 refusal envelope.
//  2. The misleading class is gone. A 3xx never comes back for a request that
//     addresses or would land on the API surface — that redirect is the whole
//     defect, and no spelling may reach it.
//  3. No over-refusal. When the guard refuses where the bare mux did not, the
//     path really was non-canonical, so a well-formed request can never be
//     turned away.
func FuzzCanonicalAPIPath(f *testing.F) {
	for _, seed := range []string{
		"/api/health", "//api/health", "/api/./health", "/api/x/../health",
		"/api/%2e%2e/api/health", "/%2e%2e/api/health", "/api/health/",
		"/api/health/..", "/api/kiro-cli/rescan", "//api/kiro-cli/rescan",
		"/api/chats/c-1", "/api/knowledge/my.base", "/api/knowledge/%2e%2e",
		"/api/chats//", "/app.js", "//app.js", "/./app.js", "/api/../app.js",
		"/chat/abc", "/", "//", "/.", "/..", "/api", "/api/", "/api//",
		"///api///health//", "/api/a%2fb", "/%2f/api/health", "/api/health%20",
		"/api/health?x=1",
	} {
		for method := range uint8(4) {
			f.Add(method, seed)
		}
	}

	// Methods come from a fixed set rather than the fuzzer so every input is a
	// legal request line and no case is skipped for an unrelated reason.
	methods := []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodPut}
	guardedMux, _ := requestPathMux()
	bareMux, _ := requestPathMux()
	guarded := canonicalAPIPath(guardedMux)
	wantRefusal := `{"error":"` + msgNonCanonicalPath + `"}`

	f.Fuzz(func(t *testing.T, methodIdx uint8, target string) {
		if !strings.HasPrefix(target, "/") {
			t.Skip("not an origin-form request target")
		}
		method := methods[int(methodIdx)%len(methods)]
		req, err := http.NewRequest(method, "http://example.com"+target, http.NoBody)
		if err != nil {
			t.Skip("unparseable target")
		}

		guardedRec := httptest.NewRecorder()
		guarded.ServeHTTP(guardedRec, req.Clone(req.Context()))
		bareRec := httptest.NewRecorder()
		bareMux.ServeHTTP(bareRec, req.Clone(req.Context()))

		clean, canonical := webhttp.CanonicalRequestPath(req.URL.Path)
		refused := guardedRec.Code == http.StatusBadRequest &&
			strings.TrimSpace(guardedRec.Body.String()) == wantRefusal

		// (1) refusal or transparency, nothing else.
		if !refused {
			if guardedRec.Code != bareRec.Code {
				t.Fatalf("target %q: guarded status %d != unguarded %d and it is not the refusal",
					target, guardedRec.Code, bareRec.Code)
			}
			if got, want := guardedRec.Header().Get("Location"), bareRec.Header().Get("Location"); got != want {
				t.Fatalf("target %q: guarded Location %q != unguarded %q", target, got, want)
			}
		}

		// (2) no redirect survives anywhere on the API surface.
		if guardedRec.Code >= 300 && guardedRec.Code < 400 && onAPISurface(req.URL.Path, clean) {
			t.Fatalf("target %q: status %d on the API surface (clean %q); the misleading redirect class must be gone",
				target, guardedRec.Code, clean)
		}

		// (3) a canonical path is never refused by the guard. The bare mux is
		// the control: if it answered 400 itself, that 400 is not the guard's.
		if refused && bareRec.Code != http.StatusBadRequest && canonical {
			t.Fatalf("target %q: refused a CANONICAL path (clean %q)", target, clean)
		}
	})
}
