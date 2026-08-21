package push

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// RegisterRoutes wires /api/push/vapid-key, /api/push/subscribe, and
// /api/push/unsubscribe onto mux.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/push/vapid-key", s.handleVAPIDKey)
	mux.HandleFunc("/api/push/subscribe", s.handleSubscribe)
	mux.HandleFunc("/api/push/unsubscribe", s.handleUnsubscribe)
}

// vapidKeyResponse is the typed wire shape for the VAPID public key endpoint.
type vapidKeyResponse struct {
	PublicKey string `json:"publicKey"`
}

func (s *Service) handleVAPIDKey(w http.ResponseWriter, _ *http.Request) {
	webhttp.WriteJSON(w, vapidKeyResponse{PublicKey: s.PublicKey()})
}

func (s *Service) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodPost) {
		return
	}
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var sub vibekit.PushSubscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil || sub.Endpoint == "" {
		httpreply.BadRequest(w, "invalid subscription")
		return
	}
	if !isAllowedPushEndpoint(sub.Endpoint) {
		httpreply.BadRequest(w, "push endpoint not from a recognised browser push service")
		return
	}
	// Validate key material at the ingress boundary so Send's hot
	// path doesn't have to defend against structurally bad keys.
	// RFC 8291: p256dh is the 65-byte uncompressed P-256 point
	// (0x04 || X(32) || Y(32)); auth is 16 random bytes.
	pub, err := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if err != nil || len(pub) != 65 || pub[0] != 0x04 {
		httpreply.BadRequest(w, "invalid p256dh key")
		return
	}
	auth, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil || len(auth) != 16 {
		httpreply.BadRequest(w, "invalid auth secret")
		return
	}
	s.Subscribe(sub)
	webhttp.Ok(w)
}

func (s *Service) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodPost) {
		return
	}
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
		httpreply.BadRequest(w, "invalid endpoint")
		return
	}
	s.Unsubscribe(body.Endpoint)
	webhttp.Ok(w)
}
