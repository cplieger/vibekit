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
	t.Setenv("KIRO_CLI_VERSION", "")
	cfg := ConfigFromEnv()

	if cfg.WorkDir == "" {
		t.Error("WorkDir is empty, want default")
	}
	if cfg.ConfigDir == "" {
		t.Error("ConfigDir is empty, want default")
	}
	if cfg.KiroCLIVersion != "" {
		t.Errorf("KiroCLIVersion = %q, want empty outside the container", cfg.KiroCLIVersion)
	}
	if cfg.AuthConfig.LoginURLTimeout <= 0 {
		t.Error("LoginURLTimeout is zero/negative, want positive default")
	}
	if cfg.AuthConfig.LoginTimeout <= 0 {
		t.Error("LoginTimeout is zero/negative, want positive default")
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
	t.Setenv("KIRO_CLI_VERSION", "9.9.9")
	t.Setenv("KIRO_CLI_SHA256", "abc")
	t.Setenv("KIRO_CLI_SHA256_ARM64", "def")
	t.Setenv("VIBEKIT_AUTH_LOGIN_URL_TIMEOUT", "10s")

	cfg := ConfigFromEnv()

	if cfg.WorkDir != "/custom/work" {
		t.Errorf("WorkDir = %q, want /custom/work", cfg.WorkDir)
	}
	if cfg.ConfigDir != "/custom/config" {
		t.Errorf("ConfigDir = %q, want /custom/config", cfg.ConfigDir)
	}
	if cfg.KiroCLIVersion != "9.9.9" || cfg.KiroCLISHA256 != "abc" || cfg.KiroCLISHA256ARM64 != "def" {
		t.Errorf("pins = %q/%q/%q, want the three exported literals verbatim",
			cfg.KiroCLIVersion, cfg.KiroCLISHA256, cfg.KiroCLISHA256ARM64)
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

// TestParseAllowedHosts pins the ALLOWED_HOSTS parser (the config-layer
// wrapper over webhttp.ParseHostList): unset/blank yields an INACTIVE policy
// (any Host accepted, the backward-compatible default), a valid list becomes
// an active canonicalized exact-match gate with the loopback carve-out, a
// malformed entry is dropped (drop-and-report) while the valid subset is
// kept, and an all-invalid list yields an ACTIVE EMPTY policy — deny-all,
// fail closed, never silently unprotected.
func TestParseAllowedHosts(t *testing.T) {
	allows := func(t *testing.T, policy *webhttp.HostPolicy, host, remoteAddr string) bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/x", nil)
		if remoteAddr != "" {
			req.RemoteAddr = remoteAddr
		}
		return policy.Allows(req)
	}

	t.Run("empty yields an inactive policy (any Host accepted)", func(t *testing.T) {
		policy := parseAllowedHosts("")
		if policy.Active() {
			t.Error("parseAllowedHosts(\"\") is active; want the permissive backward-compatible default")
		}
		if !allows(t, policy, "anything.example:8080", "") {
			t.Error("inactive policy rejected a request")
		}
	})

	t.Run("valid list gates exactly, with the loopback carve-out", func(t *testing.T) {
		policy := parseAllowedHosts("localhost, Vibekit.Example.COM.")
		if !policy.Active() || policy.Size() != 2 {
			t.Fatalf("policy active=%v size=%d, want active with 2 entries", policy.Active(), policy.Size())
		}
		for _, c := range []struct {
			host, peer string
			want       bool
		}{
			{"VIBEKIT.example.com:8080", "192.168.1.50:44444", true}, // case + port canonicalize
			{"attacker.evil:8080", "192.168.1.50:44444", false},
			{"127.0.0.1:8080", "127.0.0.1:54321", true},     // healthcheck shape rides the carve-out
			{"127.0.0.1:8080", "192.168.1.50:44444", false}, // forged loopback Host from remote peer
		} {
			if got := allows(t, policy, c.host, c.peer); got != c.want {
				t.Errorf("Allows(Host %q, peer %s) = %v, want %v", c.host, c.peer, got, c.want)
			}
		}
	})

	t.Run("malformed entry dropped, valid subset kept", func(t *testing.T) {
		policy := parseAllowedHosts("http://vibekit.example.com, localhost")
		if got := policy.Size(); got != 1 {
			t.Fatalf("policy size = %d, want 1 (the URL-shaped entry dropped, the valid one kept)", got)
		}
		if !allows(t, policy, "localhost:8080", "192.168.1.50:44444") {
			t.Error("valid entry localhost missing from the allowlist")
		}
	})

	t.Run("all-invalid fails closed (active empty)", func(t *testing.T) {
		policy := parseAllowedHosts(":8080")
		if !policy.Active() || policy.Size() != 0 {
			t.Fatalf("policy active=%v size=%d, want an active empty policy (fail closed, never fall open)", policy.Active(), policy.Size())
		}
		if allows(t, policy, "vibekit.example.com:8080", "192.168.1.50:44444") {
			t.Error("non-loopback request admitted by an active empty policy; all-invalid configuration must deny-all")
		}
	})
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
