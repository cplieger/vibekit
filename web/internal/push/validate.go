package push

import (
	"fmt"
	"net/url"
	"strings"
)

func init() {
	for i, r := range pushEndpointRules {
		hasHost := r.Host != ""
		hasSuffix := r.Suffix != ""
		if hasHost == hasSuffix {
			panic(fmt.Sprintf("push: pushEndpointRules[%d]: exactly one of Host or Suffix must be set", i))
		}
		if hasSuffix && !strings.HasPrefix(r.Suffix, ".") {
			panic(fmt.Sprintf("push: pushEndpointRules[%d]: Suffix must start with '.'", i))
		}
	}
}

// pushEndpointRule declares a single vendor's match semantics for push
// endpoint validation. Exactly one of Host or Suffix is set.
type pushEndpointRule struct {
	Host   string // exact-match (empty if suffix-only)
	Suffix string // suffix-match; must start with "." (empty if exact-only)
}

// pushEndpointRules is the declarative policy table of Web Push endpoint
// hosts recognised by the major browsers. The subscribe endpoint stores
// a URL that the server will later POST to; an unvalidated endpoint is a
// textbook SSRF primitive. Limiting to real browser push services
// neutralises the primitive without coupling us to specific DNS
// resolution or IP ranges. Adding a new vendor is one line.
var pushEndpointRules = []pushEndpointRule{
	{Host: "fcm.googleapis.com"},                // Chrome, Edge, others on Chromium
	{Host: "updates.push.services.mozilla.com"}, // Firefox
	{Host: "web.push.apple.com"},                // Safari
	{Suffix: ".notify.windows.com"},             // WNS (Edge on Windows)
	{Suffix: ".push.apple.com"},                 // Apple future-proofing
}

// isAllowedPushEndpoint validates that the endpoint URL is https and
// targets one of the known browser push services. Explicit ports are
// rejected so the stored endpoint matches what vapidHeader later
// derives as the JWT audience (which uses u.Host, port included).
// Returns false for any rejection path.
func isAllowedPushEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	if u.Scheme != "https" {
		return false
	}
	if u.Port() != "" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	for _, rule := range pushEndpointRules {
		if rule.Host != "" && host == rule.Host {
			return true
		}
		if rule.Suffix != "" && strings.HasSuffix(host, rule.Suffix) {
			return true
		}
	}
	return false
}
