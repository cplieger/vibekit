package forges

import (
	"encoding/json"
	"net/http"

	"github.com/cplieger/vibekit/internal/api"
)

func (h *HTTPHandler) handleGitHubDeviceStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w, http.MethodPost)
		return
	}
	resp, err := StartGitHubDeviceFlow(r.Context())
	if err != nil {
		api.ServerError(w, "device flow failed", err)
		return
	}
	api.WriteJSON(w, resp)
}

func (h *HTTPHandler) handleGitHubDevicePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.MethodNotAllowed(w, http.MethodPost)
		return
	}
	var body struct {
		DeviceCode string `json:"device_code"`
	}
	api.LimitBody(w, r, api.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.BadRequest(w, "invalid json")
		return
	}
	res, err := PollGitHubDeviceFlow(r.Context(), body.DeviceCode)
	if err != nil {
		api.ServerError(w, "poll failed", err)
		return
	}
	if res.Status == stateComplete {
		h.manager.Invalidate()
		_ = h.manager.Refresh(r.Context())
		h.notifyChanged(r.Context())
	}
	api.WriteJSON(w, res)
}
