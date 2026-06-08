package hub

import (
	"net/http"

	"github.com/cplieger/vibekit/internal/command"
)

// RegisterSlashRoutes delegates to the command package's implementation.
func (h *Hub) RegisterSlashRoutes(mux *http.ServeMux) {
	command.RegisterSlashRoutes(mux, h)
}
