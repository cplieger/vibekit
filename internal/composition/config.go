package composition

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/envx"
	"github.com/cplieger/toolbelt/v2"
	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/filehandler"
	"github.com/cplieger/webhttp"
)

// Config holds all environment/flag values needed to build the app.
type Config struct {
	WorkDir   string
	ConfigDir string
	VapidSub  string
	// KiroCLIVersion and the two digests are the Renovate-pinned literals
	// entrypoint.sh declares and exports. They are the manager's whole input:
	// no version means no managed install (bare `go run`), and a malformed
	// digest fails manager construction rather than a 528 MB download.
	KiroCLIVersion     string
	KiroCLISHA256      string
	KiroCLISHA256ARM64 string
	// ToolsDir is the tools engine's install tree root (bin/, opt/,
	// npm/, python/) on the persistent volume.
	ToolsDir string
	// ToolCatalogPath is the compiled tool catalog baked into the
	// image (missing = degraded catalog search). With the runtime
	// refresh below it is only the first-boot/offline fallback.
	ToolCatalogPath string
	// ToolCatalogURL is the published catalog the engine refreshes
	// from at boot and on the ToolCatalogRefresh schedule.
	ToolCatalogURL string
	// ToolCatalogOverlays are display-patch overlay files the engine
	// re-applies to every loaded catalog (the vibekit UI copy for
	// Sources/MCP tool rows). Missing files are dropped at wiring time
	// (bare `go run` outside the image).
	ToolCatalogOverlays []string
	// ToolCatalogRequire lists the tool names a fetched catalog must
	// resolve before it replaces the current one — the embedded
	// required-tools.txt, injected by main.
	ToolCatalogRequire []string
	// TrustedProxies is the set of reverse-proxy networks whose
	// X-Forwarded-For header webhttp.ClientIP is allowed to trust when
	// resolving the real client IP (access log + login/logout audit
	// logs). Parsed once from TRUSTED_PROXIES at startup. Empty/unset =
	// trust nothing = log the unspoofable socket peer (the spoof-safe
	// default for a directly-exposed deployment).
	TrustedProxies []*net.IPNet
	// HostPolicy is the exact-match Host allowlist parsed once from
	// ALLOWED_HOSTS at startup (webhttp.HostPolicy) — the anti-DNS-rebinding
	// gate the security middleware applies before the CSRF check. Unset or
	// blank = an inactive policy = any Host accepted (backward compatible;
	// the server warns at listen time).
	HostPolicy *webhttp.HostPolicy
	// BrowseRoots is the file browser's allow-list: the granted
	// directories the /api/file* surface can see. Always WorkDir +
	// ConfigDir, plus any extra grants from VIBEKIT_BROWSE_ROOTS
	// (colon-separated absolute paths, e.g. "/tmp:/data"). Everything
	// outside the grants is denied by default.
	BrowseRoots []string
	// ToolCatalogRefresh is the engine refresh cadence under toolbelt's
	// canonical policy (default 24h; zero = schedule disabled, keeping
	// the manual UI/API refresh).
	ToolCatalogRefresh time.Duration
	AuthConfig         auth.Config
}

// ConfigFromEnv reads configuration from environment variables with
// sensible defaults.
func ConfigFromEnv() Config {
	ac := auth.DefaultConfig
	ac.LoginURLTimeout = envx.Duration("VIBEKIT_AUTH_LOGIN_URL_TIMEOUT", ac.LoginURLTimeout)
	ac.LoginProcessCap = envx.Duration("VIBEKIT_AUTH_LOGIN_PROCESS_CAP", ac.LoginProcessCap)
	ac.LogoutTimeout = envx.Duration("VIBEKIT_AUTH_LOGOUT_TIMEOUT", ac.LogoutTimeout)
	ac.WhoamiTimeout = envx.Duration("VIBEKIT_AUTH_WHOAMI_TIMEOUT", ac.WhoamiTimeout)

	configDir := envx.String("KIRO_CONFIG_DIR", "/config")
	workDir := envx.String("KIRO_WORK_DIR", "/workspace")
	return Config{
		WorkDir:   workDir,
		ConfigDir: configDir,
		// The pins the entrypoint exports. Unset outside the container.
		KiroCLIVersion:     envx.String("KIRO_CLI_VERSION", ""),
		KiroCLISHA256:      envx.String("KIRO_CLI_SHA256", ""),
		KiroCLISHA256ARM64: envx.String("KIRO_CLI_SHA256_ARM64", ""),
		VapidSub:           envx.String("VAPID_SUBJECT", "mailto:vibekit@noreply.invalid"),
		ToolsDir:           envx.String("VIBEKIT_TOOLS_DIR", filepath.Join(configDir, "tools")),
		ToolCatalogPath:    envx.String("VIBEKIT_TOOL_CATALOG", "/opt/vibekit/tool-catalog.json"),
		ToolCatalogURL:     envx.String("VIBEKIT_TOOL_CATALOG_URL", toolbelt.DefaultCatalogURL),
		ToolCatalogRefresh: toolbelt.ParseCatalogRefresh(
			envx.String("VIBEKIT_TOOL_CATALOG_REFRESH", ""), "VIBEKIT_TOOL_CATALOG_REFRESH"),
		ToolCatalogOverlays: overlayFiles(os.Getenv("VIBEKIT_TOOL_CATALOG_OVERLAY")),
		TrustedProxies:      parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
		HostPolicy:          parseAllowedHosts(os.Getenv("ALLOWED_HOSTS")),
		BrowseRoots:         browseRoots(workDir, configDir, os.Getenv("VIBEKIT_BROWSE_ROOTS")),
		AuthConfig:          ac,
	}
}

// defaultCatalogOverlay is the image path of vibekit's display-patch
// overlay file (shipped by the Dockerfile beside the binary).
const defaultCatalogOverlay = "/opt/vibekit/catalog-overlays.json"

// overlayFiles resolves the catalog-overlay list. The DEFAULT image
// path missing is expected outside the container (bare `go run`), so it
// is dropped silently; an EXPLICITLY configured path that does not
// resolve is an operator mistake and warns loudly instead of silently
// running overlay-less.
func overlayFiles(explicit string) []string {
	path := explicit
	if path == "" {
		path = defaultCatalogOverlay
	}
	path = filepath.Clean(path)
	if _, err := os.Stat(path); err != nil {
		if explicit != "" {
			slog.Warn("config: VIBEKIT_TOOL_CATALOG_OVERLAY does not resolve; running without catalog overlays",
				"path", path, "error", err)
		}
		return nil
	}
	return []string{path}
}

// browseRoots assembles the file browser's allow-list: the two
// standard mounts plus any extra VIBEKIT_BROWSE_ROOTS grants. Like
// parseTrustedProxies this is the LENIENT parser: malformed entries
// are logged and skipped rather than aborting startup — a typo in the
// deployment config must not take the whole UI down, and the two
// standard mounts always survive.
func browseRoots(workDir, configDir, raw string) []string {
	extra, invalid := filehandler.ParseBrowseRoots(raw)
	if len(invalid) > 0 {
		slog.Warn("config: ignoring malformed VIBEKIT_BROWSE_ROOTS entries (want absolute paths, colon-separated)",
			"entries", invalid)
	}
	roots := make([]string, 0, 2+len(extra))
	roots = append(roots, workDir, configDir)
	return append(roots, extra...)
}

// parseTrustedProxies parses a comma-separated list of trusted
// reverse-proxy networks into the []*net.IPNet form webhttp.ClientIP /
// webhttp.WithClientIP expect. The per-entry parsing is delegated to the
// shared webhttp.ParseCIDRs: each entry is a CIDR ("10.0.0.0/8",
// "2001:db8::/32") or a bare IP ("192.0.2.10"), which is treated as a
// single-host network (/32 or /128) so an operator can list a proxy's
// address without remembering the mask; surrounding whitespace is trimmed
// and empty entries are skipped, so an unset or empty TRUSTED_PROXIES
// yields nil: trust nothing, i.e. log the unspoofable socket peer — the
// spoof-safe default for a directly-exposed deployment.
//
// This is the LENIENT caller of ParseCIDRs: malformed entries are logged
// and skipped, and the valid subset is used, rather than aborting startup.
// It deliberately fails SAFE (fall back to the socket peer for the bad
// entries) rather than fail OPEN (blindly trust a forwarded header): a
// typo in the deployment config must never turn a spoofable header into
// the logged client IP, and must never disable proxy awareness entirely.
func parseTrustedProxies(raw string) []*net.IPNet {
	nets, invalid := webhttp.ParseCIDRs(strings.Split(raw, ","))
	if len(invalid) > 0 {
		slog.Warn("config: ignoring malformed TRUSTED_PROXIES entries (want CIDR or IP)",
			"entries", invalid)
	}
	return nets
}

// parseAllowedHosts parses the comma-separated ALLOWED_HOSTS list of exact
// hostnames / IPs vibekit answers for into a webhttp.HostPolicy — the shared
// exact-match Host allowlist that closes the DNS-rebinding hole the CSRF
// check alone leaves open (a rebinding attack makes Origin and Host AGREE,
// so http.CrossOriginProtection admits it; only an exact-Host check breaks
// that chain, CWE-346 — and vibekit's HTTP surface is otherwise
// unauthenticated, with a PTY at /api/shell/ws). The library owns the
// mechanism (webhttp.CanonicalHost canonicalization, X-Forwarded-Host
// ignored, the loopback peer+Host carve-out that keeps the image's own
// healthcheck working under any allowlist); this parser owns the app policy:
// the carve-out is enabled, the 403 names ALLOWED_HOSTS, and — like
// parseTrustedProxies above — it is the LENIENT caller: malformed entries
// (a pasted URL, a lone ":8080") are logged and dropped per ParseHostList's
// drop-and-report contract, never aborting startup.
//
// An unset or all-blank var yields an INACTIVE policy — "any Host accepted",
// the backward-compatible default; the server warns at listen time. Any
// non-blank entry engages the gate, so an all-invalid list yields an active
// EMPTY policy: deny-all except the loopback carve-out, failing closed
// rather than silently unprotected — warned here by name, since every
// browser request would otherwise 403 with no hint why.
func parseAllowedHosts(raw string) *webhttp.HostPolicy {
	policy, invalid := webhttp.ParseHostList(strings.Split(raw, ","),
		webhttp.WithLoopbackExempt(),
		webhttp.WithHostAllowlistError("",
			"host not allowed; add it to ALLOWED_HOSTS to serve this hostname"))
	if len(invalid) > 0 {
		slog.Warn("config: dropping malformed ALLOWED_HOSTS entries; they cannot match any browser-sent Host",
			"entries", invalid,
			"hint", "use bare hostnames or IPs only (no scheme, path, or CIDR), e.g. localhost,192.168.1.5,vibekit.example.com")
	}
	if policy.Active() && policy.Size() == 0 {
		slog.Warn("config: ALLOWED_HOSTS has no usable entries; rejecting every non-loopback request (fail closed)",
			"hint", "fix the entries listed in the preceding warning to restore browser access")
	}
	return policy
}
