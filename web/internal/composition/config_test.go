package composition

import (
	"testing"
	"time"
)

func FuzzEnvDuration(f *testing.F) {
	f.Add("")
	f.Add("5s")
	f.Add("100ms")
	f.Add("invalid")
	f.Add("0")
	f.Add("-1h")
	f.Add("999999h")

	f.Fuzz(func(t *testing.T, input string) {
		fallback := 30 * time.Second
		result := envDuration("FUZZ_TEST_KEY_UNUSED", fallback)
		// With no env var set, must return fallback.
		if result != fallback {
			t.Fatalf("envDuration with unset key = %v, want %v", result, fallback)
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
