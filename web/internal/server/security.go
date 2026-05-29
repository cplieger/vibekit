// Package server — security middleware: CSP headers and stdlib CSRF protection.
//
// Applied once at the top of the mux in ListenAndServe. CSP is a
// defense-in-depth layer: if the markdown renderer ever leaks an XSS
// vector, CSP contains it. CSRF protection uses Go 1.25's
// net/http.CrossOriginProtection, which checks Sec-Fetch-Site first
// (preferred per OWASP Fetch Metadata) and falls back to comparing the
// Origin header host against the Host header.
package server

import (
	"net/http"
)

// cspPolicy is the Content-Security-Policy applied to every response.
//
// `style-src 'unsafe-inline'` is required because we set inline styles
// for editor line highlighting, context-bar fills, and xterm.js terminal
// rendering. Could be tightened with `nonce-…` or CSS custom properties
// later.
//
// `img-src 'self' data:` permits the Seti UI file-type icons (inline
// data URIs) and any future image uploads served from our own origin.
//
// `connect-src 'self'` covers both HTTP and WebSocket (ws:/wss:) to the
// same origin; the shell PTY uses a WebSocket at /api/shell/ws.
//
// `script-src` includes a hash for the inline <script type="importmap">
// that maps xterm.js bare specifiers to vendor paths. The hash must be
// updated if the import map content changes; TestCSPImportMapHash in
// security_test.go recomputes it from static/index.html and fails when
// the policy drifts.
const cspPolicy = "default-src 'self'; " +
	"connect-src 'self'; " +
	"img-src 'self' data:; " +
	"style-src 'self' 'unsafe-inline'; " +
	"script-src 'self' 'sha256-CRaVblhbhU9xq4tOtCccTE1/Sa/Zx+JZMSfcv9gO4cQ='; " +
	"font-src 'self'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// securityMiddleware applies CSP headers and wraps the handler with
// http.NewCrossOriginProtection (Go 1.25+ stdlib). The CSRF protection
// allows GET/HEAD/OPTIONS unconditionally and rejects state-changing
// cross-origin requests with 403 Forbidden via the stdlib's default
// deny handler. CSP/nosniff/Referrer-Policy are set on every response
// (including the 403) so the deny path stays consistent with the rest
// of the surface.
func securityMiddleware(next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	csrfWrapped := csrf.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", cspPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		csrfWrapped.ServeHTTP(w, r)
	})
}
