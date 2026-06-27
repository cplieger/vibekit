package composition

import (
	"testing"
	"time"
)

// TestEnvDuration pins envDuration's three branches: a valid value is
// parsed (not the fallback), an unparseable value falls back (not the
// zero duration a failed parse would otherwise yield), and an
// empty/unset value falls back.
func TestEnvDuration(t *testing.T) {
	const key = "VIBEKIT_ENVDURATION_TEST"
	const fallback = 10 * time.Second

	t.Run("valid value parses, not fallback", func(t *testing.T) {
		t.Setenv(key, "5s")
		if got := envDuration(key, fallback); got != 5*time.Second {
			t.Errorf("envDuration(%q) = %v, want %v", "5s", got, 5*time.Second)
		}
	})

	t.Run("unparseable value returns fallback, not zero", func(t *testing.T) {
		t.Setenv(key, "not-a-duration")
		if got := envDuration(key, fallback); got != fallback {
			t.Errorf("envDuration(%q) = %v, want fallback %v", "not-a-duration", got, fallback)
		}
	})

	t.Run("empty value returns fallback", func(t *testing.T) {
		t.Setenv(key, "")
		if got := envDuration(key, fallback); got != fallback {
			t.Errorf("envDuration(empty) = %v, want fallback %v", got, fallback)
		}
	})
}

func TestConfigFromEnv_Defaults(t *testing.T) {
	// Unset all relevant env vars (they shouldn't be set in test env).
	cfg := ConfigFromEnv()

	if cfg.WorkDir == "" {
		t.Error("WorkDir is empty, want default")
	}
	if cfg.ConfigDir == "" {
		t.Error("ConfigDir is empty, want default")
	}
	if cfg.CLIPath == "" {
		t.Error("CLIPath is empty, want default")
	}
	if cfg.AuthConfig.LoginURLTimeout <= 0 {
		t.Error("LoginURLTimeout is zero/negative, want positive default")
	}
	if cfg.AuthConfig.LoginProcessCap <= 0 {
		t.Error("LoginProcessCap is zero/negative, want positive default")
	}
	if cfg.AuthConfig.LogoutTimeout <= 0 {
		t.Error("LogoutTimeout is zero/negative, want positive default")
	}
	if cfg.AuthConfig.WhoamiTimeout <= 0 {
		t.Error("WhoamiTimeout is zero/negative, want positive default")
	}
}

func TestConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("KIRO_WORK_DIR", "/custom/work")
	t.Setenv("KIRO_CONFIG_DIR", "/custom/config")
	t.Setenv("KIRO_CLI_PATH", "/usr/bin/custom-cli")
	t.Setenv("VIBEKIT_AUTH_LOGIN_URL_TIMEOUT", "10s")

	cfg := ConfigFromEnv()

	if cfg.WorkDir != "/custom/work" {
		t.Errorf("WorkDir = %q, want /custom/work", cfg.WorkDir)
	}
	if cfg.ConfigDir != "/custom/config" {
		t.Errorf("ConfigDir = %q, want /custom/config", cfg.ConfigDir)
	}
	if cfg.CLIPath != "/usr/bin/custom-cli" {
		t.Errorf("CLIPath = %q, want /usr/bin/custom-cli", cfg.CLIPath)
	}
	if cfg.AuthConfig.LoginURLTimeout != 10*time.Second {
		t.Errorf("LoginURLTimeout = %v, want 10s", cfg.AuthConfig.LoginURLTimeout)
	}
}
