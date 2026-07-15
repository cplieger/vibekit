package composition

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/envx"
	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/webhttp"
)

// Config holds all environment/flag values needed to build the app.
type Config struct {
	WorkDir   string
	ConfigDir string
	CLIPath   string
	VapidSub  string
	// ToolsDir is the tools engine's install tree root (bin/, opt/,
	// npm/, python/) on the persistent volume.
	ToolsDir string
	// ToolCatalogPath is the compiled tool catalog baked into the
	// image (missing = degraded catalog search).
	ToolCatalogPath string
	// TrustedProxies is the set of reverse-proxy networks whose
	// X-Forwarded-For header webhttp.ClientIP is allowed to trust when
	// resolving the real client IP (access log + login/logout audit
	// logs). Parsed once from TRUSTED_PROXIES at startup. Empty/unset =
	// trust nothing = log the unspoofable socket peer (the spoof-safe
	// default for a directly-exposed deployment).
	TrustedProxies []*net.IPNet
	AuthConfig     auth.Config
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
	return Config{
		WorkDir:         envx.String("KIRO_WORK_DIR", "/workspace"),
		ConfigDir:       configDir,
		CLIPath:         envx.String("KIRO_CLI_PATH", "kiro-cli"),
		VapidSub:        envx.String("VAPID_SUBJECT", "mailto:vibekit@noreply.invalid"),
		ToolsDir:        envx.String("VIBEKIT_TOOLS_DIR", filepath.Join(configDir, "tools")),
		ToolCatalogPath: envx.String("VIBEKIT_TOOL_CATALOG", "/opt/vibekit/tool-catalog.json"),
		TrustedProxies:  parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
		AuthConfig:      ac,
	}
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
