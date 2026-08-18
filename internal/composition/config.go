package composition

import (
	"cmp"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/envx/v2"
	"github.com/cplieger/pinstall/v2"
	"github.com/cplieger/toolbelt/v2"
	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/bridge"
	"github.com/cplieger/vibekit/internal/filebrowse"
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
	// TrustedInstallUIDs names identities whose write access to the kiro-cli
	// install tree does not invalidate custody, parsed from
	// TRUSTED_INSTALL_UIDS. Empty is the default and the right setting for
	// almost every deployment: pinstall then refuses to install into a tree any
	// other identity can write, which is what stops a planted binary being run
	// as this container's user. It is deliberately NOT a compiled-in value —
	// only the deployment knows which account on its volume is already at least
	// as privileged as this process, and baking one in would make that claim on
	// behalf of every deployment that pulled the image.
	TrustedInstallUIDs []int
	// HostPolicy is the exact-match Host allowlist parsed once from
	// ALLOWED_HOSTS at startup (webhttp.HostPolicy) — the anti-DNS-rebinding
	// gate the security middleware applies before the CSRF check. Unset or
	// blank = an inactive policy = any Host accepted (backward compatible;
	// the server warns at listen time).
	HostPolicy *webhttp.HostPolicy
	// BridgeEnvAllow re-permits names the bridge's credential screen would
	// otherwise drop on its way down to kiro-cli, parsed from
	// bridge.EnvAllowVar. Nil is the shipped configuration and the right one:
	// nothing in the image puts a credential in this environment, so the
	// override exists for the operator who has a legitimate variable whose name
	// merely reads like one.
	BridgeEnvAllow map[string]struct{}
	// BrowseRoots is the file browser's allow-list: the granted
	// directories the /api/file* surface can see. Always WorkDir +
	// ConfigDir, plus any extra grants from VIBEKIT_BROWSE_ROOTS
	// (colon-separated absolute paths, e.g. "/tmp:/data"). Everything
	// outside the grants is denied by default.
	BrowseRoots []string
	// ACPArgs are operator-supplied kiro-cli launch flags from
	// VIBEKIT_KIRO_ACP_ARGS, already filtered by bridge.ParseACPArgs (which
	// refuses --agent-engine and the two inert trust flags). Appended to every
	// CHAT bridge's argv, never to the utility bridge's. An escape hatch for a
	// flag upstream adds, not a capability switch: vibekit already pins v3 and
	// already emits --model / --effort.
	ACPArgs []string
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
	ac.LoginTimeout = envx.Duration("VIBEKIT_AUTH_LOGIN_TIMEOUT", ac.LoginTimeout)
	ac.LogoutTimeout = envx.Duration("VIBEKIT_AUTH_LOGOUT_TIMEOUT", ac.LogoutTimeout)
	ac.WhoamiTimeout = envx.Duration("VIBEKIT_AUTH_WHOAMI_TIMEOUT", ac.WhoamiTimeout)

	configDir := cmp.Or(envx.String("KIRO_CONFIG_DIR"), "/config")
	workDir := cmp.Or(envx.String("KIRO_WORK_DIR"), "/workspace")
	return Config{
		WorkDir:   workDir,
		ConfigDir: configDir,
		// The pins the entrypoint exports. Unset outside the container.
		KiroCLIVersion:     envx.String("KIRO_CLI_VERSION"),
		KiroCLISHA256:      envx.String("KIRO_CLI_SHA256"),
		KiroCLISHA256ARM64: envx.String("KIRO_CLI_SHA256_ARM64"),
		VapidSub:           cmp.Or(envx.String("VAPID_SUBJECT"), "mailto:vibekit@noreply.invalid"),
		ToolsDir:           cmp.Or(envx.String("VIBEKIT_TOOLS_DIR"), filepath.Join(configDir, "tools")),
		ToolCatalogPath:    cmp.Or(envx.String("VIBEKIT_TOOL_CATALOG"), "/opt/vibekit/tool-catalog.json"),
		ToolCatalogURL:     cmp.Or(envx.String("VIBEKIT_TOOL_CATALOG_URL"), toolbelt.DefaultCatalogURL),
		ToolCatalogRefresh: toolbelt.ParseCatalogRefresh(
			envx.String("VIBEKIT_TOOL_CATALOG_REFRESH"), "VIBEKIT_TOOL_CATALOG_REFRESH"),
		ToolCatalogOverlays: overlayFiles(os.Getenv("VIBEKIT_TOOL_CATALOG_OVERLAY")),
		TrustedProxies:      parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
		TrustedInstallUIDs:  parseTrustedInstallUIDs(os.Getenv("TRUSTED_INSTALL_UIDS")),
		HostPolicy:          parseAllowedHosts(os.Getenv("ALLOWED_HOSTS")),
		BrowseRoots:         browseRoots(workDir, configDir, os.Getenv("VIBEKIT_BROWSE_ROOTS")),
		ACPArgs:             bridge.ParseACPArgs(os.Getenv("VIBEKIT_KIRO_ACP_ARGS")),
		BridgeEnvAllow:      bridge.ParseEnvAllowlist(os.Getenv(bridge.EnvAllowVar)),
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
	extra, invalid := filebrowse.ParseBrowseRoots(raw)
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

// parseTrustedInstallUIDs parses the comma-separated TRUSTED_INSTALL_UIDS
// list of numeric uids whose write access to the kiro-cli install tree does not
// invalidate pinstall's custody check. Unset or empty yields nil, which leaves
// the check fully enforcing — the correct default, and the one the image ships.
//
// Setting a uid is an ASSERTION, not a preference: pinstall's own field
// documentation states that each entry claims the identity is already at least
// as privileged as the installing process. That is true of an administrator who
// already holds root on the host, and false of the unprivileged account an
// application runs as — listing the latter hands it a binary this process later
// executes. Only the deployment can tell those apart, which is why the value
// lives in the environment and never in the image.
//
// Malformed entries are dropped with one by-name warning rather than failing the
// boot, the same warn-and-drop shape parseTrustedProxies and parseAllowedHosts
// use. The count is reported and the values are NOT: a mis-wired compose could
// put a secret on any key, and a warning is a durable, queryable log record.
// pinstall.ParseIdentities returns a count rather than the refused text for that
// exact reason, so the promise is now the library's to keep as well as this
// function's.
//
// The name carries the WT_ prefix rather than VIBEKIT_ because it is not
// vibekit's question — web-terminal-kiro installs kiro-cli through the same
// library and reads the same variable, so one name lets one document answer it
// for both. The knob is pinstall's in substance, and so is the PARSING: the rule
// this used to implement locally follows from Config.TrustedUIDs' contract, so it
// now lives beside that field as pinstall.ParseIdentities and both consumers call
// it. What stays here is what is genuinely vibekit's — the variable it reads and
// the words its operator sees.
func parseTrustedInstallUIDs(raw string) []int {
	uids, rejected := pinstall.ParseIdentities(raw)
	if rejected > 0 {
		slog.Warn("config: ignoring unusable TRUSTED_INSTALL_UIDS entries (want whole numbers above 0)",
			"invalid_count", rejected,
			"hint", "list only numeric uids, comma-separated, each an account already at least as privileged as this server")
	}
	return uids
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
