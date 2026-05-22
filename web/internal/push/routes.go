package push

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"vibekit/internal/api"
)

// RegisterRoutes wires /api/push/vapid-key, /api/push/subscribe, and
// /api/push/unsubscribe onto mux.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/push/vapid-key", s.handleVAPIDKey)
	mux.HandleFunc("/api/push/subscribe", s.handleSubscribe)
	mux.HandleFunc("/api/push/unsubscribe", s.handleUnsubscribe)
}

func (s *Service) handleVAPIDKey(w http.ResponseWriter, _ *http.Request) {
	api.WriteJSON(w, map[string]string{"publicKey": s.PublicKey()})
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

func (s *Service) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
		return
	}
	api.LimitBody(w, r, api.MaxJSONBody)
	var sub api.PushSubscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil || sub.Endpoint == "" {
		api.BadRequest(w, "invalid subscription")
		return
	}
	if !isAllowedPushEndpoint(sub.Endpoint) {
		api.BadRequest(w, "push endpoint not from a recognised browser push service")
		return
	}
	// Validate key material at the ingress boundary so Send's hot
	// path doesn't have to defend against structurally bad keys.
	// RFC 8291: p256dh is the 65-byte uncompressed P-256 point
	// (0x04 || X(32) || Y(32)); auth is 16 random bytes.
	pub, err := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if err != nil || len(pub) != 65 || pub[0] != 0x04 {
		api.BadRequest(w, "invalid p256dh key")
		return
	}
	auth, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil || len(auth) != 16 {
		api.BadRequest(w, "invalid auth secret")
		return
	}
	s.Subscribe(sub)
	api.Ok(w)
}

func (s *Service) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w)
		return
	}
	api.LimitBody(w, r, api.MaxJSONBody)
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
		api.BadRequest(w, "invalid endpoint")
		return
	}
	s.Unsubscribe(body.Endpoint)
	api.Ok(w)
}
