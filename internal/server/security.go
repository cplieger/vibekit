// Package server — security middleware: CSP headers, the ALLOWED_HOSTS
// anti-DNS-rebinding gate, and stdlib CSRF protection.
//
// Applied once at the top of the mux in ListenAndServe. CSP is a
// defense-in-depth layer: if the markdown renderer ever leaks an XSS
// vector, CSP contains it. CSRF protection uses Go 1.25's
// net/http.CrossOriginProtection, which checks Sec-Fetch-Site first
// (preferred per OWASP Fetch Metadata) and falls back to comparing the
// Origin header host against the Host header.
package server

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/cplieger/webhttp/v2"
)

// cspTemplate is the CSP applied to every response, with a single %s
// placeholder for the sha256 hash of the page's one inline <head> script:
// the anti-FOUC theme-init IIFE (@cplieger/ui-primitives'
// themeInitSnippetFromJSON output, marked with data-theme-init). The hash is
// computed at Server construction from the embedded index.html, so edits to
// the block "just work" without anyone hand-updating a constant — and
// script-src stays locked to 'self' + that exact hash (never
// 'unsafe-inline').
//
// Other directives, briefly:
//
//	style-src 'unsafe-inline'  kept for CSSOM-adjacent styling paths (editor
//	                           highlighting, context-ring fills, terminal
//	                           rendering). NOTE (2026-07): the served markup
//	                           now carries ZERO style attributes (the two
//	                           context-ring SVG transitions moved to
//	                           style.css) and the client writes styles only
//	                           via element.style property assignment, which
//	                           style-src does not govern — so this relaxation
//	                           is likely droppable after a live pass over the
//	                           editor + terminal under a 'self'-only policy.
//	img-src 'self' data:        Seti UI file-type icons (data URIs)
//	connect-src 'self'          HTTP + WebSocket to the same origin
//	                           (the shell PTY is at /api/shell/ws)
const cspTemplate = "default-src 'self'; " +
	"connect-src 'self'; " +
	"img-src 'self' data:; " +
	"style-src 'self' 'unsafe-inline'; " +
	"script-src 'self' %s; " +
	"font-src 'self'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// buildCSPPolicy reads index.html from staticFS, hashes its inline <head>
// script via webhttp.InlineScriptHashes (byte-precise and quote-aware — the
// exact bytes a browser hashes for a script-src token), and assembles the
// full CSP string. Called once at Server construction.
//
// The page carries exactly ONE inline script today (the anti-FOUC theme-init
// marked data-theme-init); the exactly-one assertion preserves the old
// targeted extraction's strictness: zero hashes means the required block is
// missing (a build defect — startup fails rather than serving a CSP that
// would block it), and more than one means an unreviewed inline script was
// added (update this check consciously instead of silently granting it CSP
// allowance).
func buildCSPPolicy(staticFS fs.FS) (string, error) {
	if staticFS == nil {
		return "", errors.New("buildCSPPolicy: nil staticFS")
	}
	html, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		return "", fmt.Errorf("buildCSPPolicy: read index.html: %w", err)
	}
	hashes := webhttp.InlineScriptHashes(html)
	if len(hashes) != 1 {
		return "", fmt.Errorf("buildCSPPolicy: expected exactly one inline script in index.html, found %d (a new inline script must be reviewed and this check updated)", len(hashes))
	}
	// script-src 'self' <theme-init-hash>
	return fmt.Sprintf(cspTemplate, hashes[0]), nil
}

// securityMiddleware sets the response security-header baseline via
// webhttp.SecurityHeaders, applies the ALLOWED_HOSTS exact-match Host
// allowlist (webhttp.HostPolicy), and wraps the handler with
// http.NewCrossOriginProtection (Go 1.25+ stdlib) for CSRF, a concern
// webhttp does not ship so it stays app-side. SecurityHeaders sets
// X-Content-Type-Options: nosniff, X-Frame-Options: DENY (aligned with
// the CSP's frame-ancestors 'none'), Referrer-Policy pinned to vibekit's
// existing same-origin, and the dynamic CSP passed through WithCSP. The
// CSRF protection allows GET/HEAD/OPTIONS unconditionally and rejects
// state-changing cross-origin requests with 403 Forbidden via the
// stdlib's default deny handler.
//
// That GET exemption has a consequence worth naming, because this layer reads
// as if it covers everything: a WebSocket handshake is a GET, so the shell PTY
// at /api/shell/ws is NOT guarded here. Its cross-origin gate is the terminal
// engine's — coder/websocket's same-origin default beneath terminal.Handler —
// and nothing in this file substitutes for it. Widening it is an engine-side
// origin policy, never a change here.
//
// Layering, outermost first: SecurityHeaders -> host allowlist -> CSRF.
// The host gate sits BEFORE the CSRF check because a DNS-rebinding request
// makes Origin and Host agree, so the origin comparison alone cannot reject
// it (CWE-346) — the exact-Host check is what breaks that chain — and
// INSIDE SecurityHeaders so its 403, like the CSRF 403, still carries the
// baseline headers. A nil or inactive policy collapses to a pass-through
// per the library's off-contract.
func securityMiddleware(cspPolicy string, hostPolicy *webhttp.HostPolicy, next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	return webhttp.SecurityHeaders(
		webhttp.WithCSP(cspPolicy),
		webhttp.WithReferrerPolicy("same-origin"),
	)(hostPolicy.Middleware()(csrf.Handler(next)))
}
