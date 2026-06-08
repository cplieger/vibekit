package composition

import (
	"log/slog"
	"os"
	"time"

	"github.com/cplieger/vibekit/internal/auth"
)

// Config holds all environment/flag values needed to build the app.
type Config struct {
	WorkDir    string
	ConfigDir  string
	CLIPath    string
	VapidSub   string
	AuthConfig auth.Config
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
		WorkDir:    envOr("KIRO_WORK_DIR", "/workspace"),
		ConfigDir:  envOr("KIRO_CONFIG_DIR", "/config"),
		CLIPath:    envOr("KIRO_CLI_PATH", "kiro-cli"),
		VapidSub:   envOr("VAPID_SUBJECT", "mailto:vibekit@noreply.invalid"),
		AuthConfig: ac,
	}
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
