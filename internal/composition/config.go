package composition

import (
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/auth"
)

// Config holds all environment/flag values needed to build the app.
type Config struct {
	WorkDir   string
	ConfigDir string
	CLIPath   string
	VapidSub  string
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
	ac.LoginURLTimeout = envDuration("VIBEKIT_AUTH_LOGIN_URL_TIMEOUT", ac.LoginURLTimeout)
	ac.LoginProcessCap = envDuration("VIBEKIT_AUTH_LOGIN_PROCESS_CAP", ac.LoginProcessCap)
	ac.LogoutTimeout = envDuration("VIBEKIT_AUTH_LOGOUT_TIMEOUT", ac.LogoutTimeout)
	ac.WhoamiTimeout = envDuration("VIBEKIT_AUTH_WHOAMI_TIMEOUT", ac.WhoamiTimeout)

	return Config{
		WorkDir:        envOr("KIRO_WORK_DIR", "/workspace"),
		ConfigDir:      envOr("KIRO_CONFIG_DIR", "/config"),
		CLIPath:        envOr("KIRO_CLI_PATH", "kiro-cli"),
		VapidSub:       envOr("VAPID_SUBJECT", "mailto:vibekit@noreply.invalid"),
		TrustedProxies: parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
		AuthConfig:     ac,
	}
}

// parseTrustedProxies parses a comma-separated list of trusted
// reverse-proxy networks into the []*net.IPNet form webhttp.ClientIP /
// webhttp.WithClientIP expect. Each entry is a CIDR ("10.0.0.0/8",
// "2001:db8::/32") or a bare IP ("192.0.2.10"), which is treated as a
// single-host network (/32 or /128) so an operator can list a proxy's
// address without remembering the mask. Surrounding whitespace is
// trimmed and empty entries are skipped, so an unset or empty
// TRUSTED_PROXIES yields nil: trust nothing, i.e. log the unspoofable
// socket peer — the spoof-safe default for a directly-exposed deployment.
//
// A malformed entry is logged and skipped rather than aborting startup.
// This deliberately fails SAFE (fall back to the socket peer) rather
// than fail OPEN (blindly trust a forwarded header): a typo in the
// deployment config must never turn a spoofable header into the logged
// client IP.
func parseTrustedProxies(raw string) []*net.IPNet {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	nets := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(entry); err == nil {
			nets = append(nets, ipnet)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			nets = append(nets, hostNet(ip))
			continue
		}
		slog.Warn("config: ignoring malformed TRUSTED_PROXIES entry (want CIDR or IP)",
			"entry", entry)
	}
	return nets
}

// hostNet returns a single-host network for ip: a /32 for IPv4 or a
// /128 for IPv6, so a bare IP in TRUSTED_PROXIES matches only itself.
func hostNet(ip net.IP) *net.IPNet {
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envDuration parses the env var named key as a time.Duration, or
// returns fallback if the var is unset or unparseable. Unparseable
// values log a warning so operators notice typos in deployment config.
func envDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("config: ignoring malformed env var, using default",
			"key", key, "value", raw, "default", fallback, "error", err)
		return fallback
	}
	return d
}
