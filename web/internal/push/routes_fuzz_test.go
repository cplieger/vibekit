package push

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzIsAllowedPushEndpoint explores the URL grammar space to verify the
// SSRF boundary invariant: any accepted endpoint must have scheme "https"
// and a hostname matching the pushEndpointRules allowlist.
func FuzzIsAllowedPushEndpoint(f *testing.F) {
	// Seed corpus from the existing table-driven test cases.
	f.Add("https://fcm.googleapis.com/fcm/send/abc")
	f.Add("https://updates.push.services.mozilla.com/wpush/v2/xyz")
	f.Add("https://web.push.apple.com/Q123")
	f.Add("https://sn1-wns.notify.windows.com/wnsapi/foo")
	f.Add("http://fcm.googleapis.com/fcm/send/abc")
	f.Add("https://localhost/push")
	f.Add("https://127.0.0.1/push")
	f.Add("https://169.254.169.254/latest/meta-data/")
	f.Add("https://evilnotify.windows.com/abc")
	f.Add("https://fcm.googleapis.com.evil.com/fcm/send/x")
	f.Add("https://fcm.googleapis.com:443/send/abc")
	f.Add("")
	f.Add("https://")
	f.Add("file:///etc/passwd")

	f.Fuzz(func(t *testing.T, endpoint string) {
		accepted := isAllowedPushEndpoint(endpoint)
		if !accepted {
			return
		}
		// Invariant: accepted endpoints MUST be https with a host on the allowlist.
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatalf("accepted unparseable URL: %q", endpoint)
		}
		if u.Scheme != "https" {
			t.Fatalf("accepted non-https scheme %q: %q", u.Scheme, endpoint)
		}
		if u.Port() != "" {
			t.Fatalf("accepted URL with explicit port: %q", endpoint)
		}
		host := u.Hostname()
		if host == "" {
			t.Fatalf("accepted URL with empty host: %q", endpoint)
		}
		matched := false
		for _, rule := range pushEndpointRules {
			if rule.Host != "" && host == rule.Host {
				matched = true
				break
			}
			if rule.Suffix != "" && strings.HasSuffix(host, rule.Suffix) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("accepted URL with host not on allowlist: %q (host=%q)", endpoint, host)
		}
	})
}
