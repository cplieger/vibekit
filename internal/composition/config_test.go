package composition

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/webhttp"
)

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

// containsIP reports whether any network in nets contains ipStr.
func containsIP(nets []*net.IPNet, ipStr string) bool {
	ip := net.ParseIP(ipStr)
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// TestParseTrustedProxies pins the CIDR/IP list parser: an empty or
// whitespace-only value yields nil (the unconfigured, spoof-safe
// socket-peer default), valid CIDRs and bare IPs parse to matching
// networks, whitespace and empty entries are skipped, and a malformed
// entry is dropped (fail-safe) rather than aborting the parse.
func TestParseTrustedProxies(t *testing.T) {
	t.Run("empty yields nil (unconfigured default)", func(t *testing.T) {
		if got := parseTrustedProxies(""); got != nil {
			t.Errorf("parseTrustedProxies(%q) = %v, want nil", "", got)
		}
	})
	t.Run("whitespace-only yields nil", func(t *testing.T) {
		if got := parseTrustedProxies("   "); got != nil {
			t.Errorf("parseTrustedProxies(spaces) = %v, want nil", got)
		}
	})

	tests := []struct {
		name        string
		raw         string
		wantLen     int
		contains    []string
		notContains []string
	}{
		{
			name:        "single CIDR",
			raw:         "10.0.0.0/8",
			wantLen:     1,
			contains:    []string{"10.1.2.3"},
			notContains: []string{"192.168.1.1"},
		},
		{
			name:        "multiple CIDRs",
			raw:         "10.0.0.0/8,192.168.0.0/16",
			wantLen:     2,
			contains:    []string{"10.1.2.3", "192.168.1.1"},
			notContains: []string{"172.16.0.1"},
		},
		{
			name:        "bare IPv4 becomes a single host",
			raw:         "203.0.113.7",
			wantLen:     1,
			contains:    []string{"203.0.113.7"},
			notContains: []string{"203.0.113.8"},
		},
		{
			name:        "bare IPv6 becomes a single host",
			raw:         "2001:db8::1",
			wantLen:     1,
			contains:    []string{"2001:db8::1"},
			notContains: []string{"2001:db8::2"},
		},
		{
			name:     "whitespace around entries is trimmed",
			raw:      "  10.0.0.0/8 , 192.168.0.0/16 ",
			wantLen:  2,
			contains: []string{"10.9.9.9", "192.168.9.9"},
		},
		{
			name:     "malformed entries are skipped, valid ones kept",
			raw:      "10.0.0.0/8, not-an-ip, 192.168.0.0/16",
			wantLen:  2,
			contains: []string{"10.1.1.1", "192.168.1.1"},
		},
		{
			name:     "empty entries are skipped",
			raw:      "10.0.0.0/8,,,",
			wantLen:  1,
			contains: []string{"10.0.0.1"},
		},
		{
			name:    "all-malformed yields empty",
			raw:     "nonsense, also-bad",
			wantLen: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTrustedProxies(tc.raw)
			if len(got) != tc.wantLen {
				t.Fatalf("parseTrustedProxies(%q) len = %d, want %d (got %v)",
					tc.raw, len(got), tc.wantLen, got)
			}
			for _, ip := range tc.contains {
				if !containsIP(got, ip) {
					t.Errorf("parseTrustedProxies(%q) should contain %s", tc.raw, ip)
				}
			}
			for _, ip := range tc.notContains {
				if containsIP(got, ip) {
					t.Errorf("parseTrustedProxies(%q) should NOT contain %s", tc.raw, ip)
				}
			}
		})
	}
}

// TestConfigFromEnv_TrustedProxies pins that TRUSTED_PROXIES flows from
// the environment into Config: unset leaves the spoof-safe nil default,
// and a comma-separated value parses to the network set.
func TestConfigFromEnv_TrustedProxies(t *testing.T) {
	t.Run("unset yields nil (socket-peer default)", func(t *testing.T) {
		t.Setenv("TRUSTED_PROXIES", "")
		if cfg := ConfigFromEnv(); cfg.TrustedProxies != nil {
			t.Errorf("TrustedProxies = %v, want nil when unset", cfg.TrustedProxies)
		}
	})
	t.Run("set parses to network set", func(t *testing.T) {
		t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8, 192.168.0.0/16")
		cfg := ConfigFromEnv()
		if len(cfg.TrustedProxies) != 2 {
			t.Fatalf("len(TrustedProxies) = %d, want 2", len(cfg.TrustedProxies))
		}
		if !containsIP(cfg.TrustedProxies, "10.1.2.3") {
			t.Error("TrustedProxies should contain 10.1.2.3")
		}
	})
}

// TestTrustedProxies_ClientIPResolution is the end-to-end contract: the
// parsed set, fed to webhttp.ClientIP exactly as server.go and the auth
// audit logs do, must resolve the real client only from a trusted
// proxy's X-Forwarded-For and otherwise fall back to the unspoofable
// socket peer. (webhttp.ClientIP exists in the pinned v1.1.1, so this
// test does not depend on the unreleased WithClientIP.)
func TestTrustedProxies_ClientIPResolution(t *testing.T) {
	const (
		peer      = "198.51.100.4" // the socket peer (TCP RemoteAddr host)
		xffClient = "203.0.113.9"  // pretend-real client carried in XFF
	)
	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
		r.RemoteAddr = peer + ":5000"
		r.Header.Set("X-Forwarded-For", xffClient)
		return r
	}

	t.Run("unconfigured: XFF ignored, socket peer wins", func(t *testing.T) {
		trusted := parseTrustedProxies("")
		if got := webhttp.ClientIP(newReq(), trusted...); got != peer {
			t.Errorf("ClientIP unconfigured = %q, want socket peer %q", got, peer)
		}
	})
	t.Run("configured, peer trusted: XFF client wins", func(t *testing.T) {
		trusted := parseTrustedProxies("198.51.100.0/24")
		if got := webhttp.ClientIP(newReq(), trusted...); got != xffClient {
			t.Errorf("ClientIP trusted peer = %q, want XFF client %q", got, xffClient)
		}
	})
	t.Run("configured, peer NOT trusted: XFF ignored", func(t *testing.T) {
		trusted := parseTrustedProxies("10.0.0.0/8")
		if got := webhttp.ClientIP(newReq(), trusted...); got != peer {
			t.Errorf("ClientIP untrusted peer = %q, want socket peer %q", got, peer)
		}
	})
}
