package push

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzIsAllowedPushEndpoint explores the URL grammar space to verify the
// SSRF boundary invariant: any accepted endpoint must have scheme "https"
// and a hostname matching the pushEndpointRules allowlist.
//
// The oracle below re-states the byte-exact host match, which means it catches a
// folded implementation today (measured: rewriting the production comparison as
// strings.EqualFold fails this target) but only for as long as nobody mirrors the
// change into the oracle — and mirroring is the natural thing to do when a target
// goes red right after an intentional edit. At that point the allow-list would
// quietly grow every fold alias of every vendor host with the target still green.
// The case decision is therefore ALSO pinned by direct assertion, in
// TestIsAllowedPushEndpointIsByteExactOnHost, which names the aliases and the
// direction of failure. Do not "align" this oracle with a folded implementation.
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
	// Case and fold aliases. The corpus reached none of these before: random
	// mutation does not produce a valid vendor host with one rune swapped for
	// its fold partner, and the weekly fuzz runner keeps no corpus between runs,
	// so a committed seed is the only durable coverage of this class.
	f.Add("https://FCM.GOOGLEAPIS.COM/fcm/send/abc")
	f.Add("https://updates.push.\u017Fervices.mozilla.com/wpush/v2/xyz")
	f.Add("https://sn1-wns.not\u0130fy.windows.com/wnsapi/foo")

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
