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
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/cplieger/webhttp"
)

// cspTemplate is the CSP applied to every response, with a single %s
// placeholder for the space-joined sha256 hashes of the two inline <head>
// scripts: the ES-module importmap and the anti-FOUC theme-init IIFE
// (@cplieger/ui-primitives' themeInitSnippetFromJSON output, marked with
// data-theme-init). Both hashes are computed at Server construction from the
// embedded index.html, so edits to either block "just work" without anyone
// hand-updating a constant — and script-src stays locked to 'self' + those
// exact hashes (never 'unsafe-inline').
//
// Other directives, briefly:
//
//	style-src 'unsafe-inline'  inline styles for editor highlighting,
//	                           context-bar fills, terminal rendering
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

// importMapRe extracts everything between the inline importmap script's
// opening and closing tags. DOTALL so the JSON body can be multi-line.
var importMapRe = regexp.MustCompile(`(?s)<script type="importmap">(.*?)</script>`)

// themeInitRe extracts the inline anti-FOUC theme-init script (the one marked
// with the data-theme-init attribute). Its body is the verbatim output of
// @cplieger/ui-primitives' themeInitSnippetFromJSON("vibekit.ui-state",
// "theme"); hashing it lets script-src stay 'self' + specific hashes without
// 'unsafe-inline'. DOTALL for symmetry with importMapRe.
var themeInitRe = regexp.MustCompile(`(?s)<script data-theme-init>(.*?)</script>`)

// inlineHashToken returns a CSP-quoted sha256 token for the inline <script>
// body matched by re (which must capture the script's text content in group 1).
// Caller hands the result straight into cspTemplate. Returns an error if the
// block is missing, so startup fails loudly rather than serving a CSP that
// would silently block a required inline script.
func inlineHashToken(html []byte, re *regexp.Regexp, what string) (string, error) {
	m := re.FindSubmatch(html)
	if m == nil {
		return "", fmt.Errorf("no %s block in index.html", what)
	}
	sum := sha256.Sum256(m[1])
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'", nil
}

// importMapHashToken returns the CSP token for the inline importmap block.
func importMapHashToken(html []byte) (string, error) {
	return inlineHashToken(html, importMapRe, `<script type="importmap">`)
}

// themeInitHashToken returns the CSP token for the inline anti-FOUC theme-init
// block.
func themeInitHashToken(html []byte) (string, error) {
	return inlineHashToken(html, themeInitRe, `<script data-theme-init>`)
}

// buildCSPPolicy reads index.html from staticFS, hashes both inline <head>
// scripts (the importmap and the anti-FOUC theme-init), and assembles the full
// CSP string. Called once at Server construction. If anything goes wrong (file
// missing, either regex miss), we return an error so startup fails loudly
// rather than serve a CSP that would block the import-map (breaking ES module
// loading) or the theme-init (flashing the wrong theme).
func buildCSPPolicy(staticFS fs.FS) (string, error) {
	if staticFS == nil {
		return "", errors.New("buildCSPPolicy: nil staticFS")
	}
	html, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		return "", fmt.Errorf("buildCSPPolicy: read index.html: %w", err)
	}
	importMapHash, err := importMapHashToken(html)
	if err != nil {
		return "", fmt.Errorf("buildCSPPolicy: %w", err)
	}
	themeInitHash, err := themeInitHashToken(html)
	if err != nil {
		return "", fmt.Errorf("buildCSPPolicy: %w", err)
	}
	// script-src 'self' <importmap-hash> <theme-init-hash>
	return fmt.Sprintf(cspTemplate, importMapHash+" "+themeInitHash), nil
}

// fallbackCSPPolicy assembles a CSP without an importmap hash — used
// only in tests that don't care about the importmap directive. The
// 'unsafe-inline' relaxation makes those tests pass; production never
// goes through this path because Server construction always runs
// buildCSPPolicy against the real embedded FS.
func fallbackCSPPolicy() string {
	return fmt.Sprintf(cspTemplate, "'unsafe-inline'")
}

// securityMiddleware sets the response security-header baseline via
// webhttp.SecurityHeaders and wraps the handler with
// http.NewCrossOriginProtection (Go 1.25+ stdlib) for CSRF, a concern
// webhttp does not ship so it stays app-side. SecurityHeaders sets
// X-Content-Type-Options: nosniff, X-Frame-Options: DENY (aligned with
// the CSP's frame-ancestors 'none'), Referrer-Policy pinned to vibekit's
// existing same-origin, and the dynamic CSP passed through WithCSP. The
// CSRF protection allows GET/HEAD/OPTIONS unconditionally and rejects
// state-changing cross-origin requests with 403 Forbidden via the
// stdlib's default deny handler. Because SecurityHeaders wraps the CSRF
// handler from the outside, the baseline headers are set on every
// response (including the 403) so the deny path stays consistent with
// the rest of the surface.
func securityMiddleware(cspPolicy string, next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	return webhttp.SecurityHeaders(
		webhttp.WithCSP(cspPolicy),
		webhttp.WithReferrerPolicy("same-origin"),
	)(csrf.Handler(next))
}
