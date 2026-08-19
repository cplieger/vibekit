// File line tracker HTTP handler.
//
// The LineTracker implementation lives in internal/buffer. This file
// provides the HTTP handler that exposes it via
// GET /api/file-changes?chat_id=<id>&path=<path>.

package hub

import (
	"net/http"

	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp"
)

// handleFileChanges delegates to the line tracker for
// GET /api/file-changes?chat_id=<id>&path=<path>.
func (h *Hub) handleFileChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	chatID := r.URL.Query().Get("chat_id")
	if chatID == "" {
		httpreply.BadRequest(w, "chat_id is required")
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		httpreply.BadRequest(w, "path query param is required")
		return
	}
	ranges := h.lines.Get(vibekit.ChatID(chatID), path)
	if ranges == nil {
		ranges = []buffer.LineRange{}
	}
	webhttp.WriteJSON(w, map[string]any{"changes": ranges})
}
