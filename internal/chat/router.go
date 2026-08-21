package chat

import (
	"net/http"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp/v2"
)

// Router owns the HTTP handler surface for the chat package. It holds
// a *Store reference and delegates all persistence to it, separating
// the HTTP routing/serialisation concern from the data layer.
//
// The Store's RegisterRoutes method delegates to Router.Register, which
// separates the HTTP concern without changing the Store's method set — so the
// chat-store contracts its consumers declare (agent/deps.go, command/deps.go) see
// the same store either way.
type Router struct {
	store *Store
}

// NewRouter creates a Router backed by the given Store.
func NewRouter(s *Store) *Router {
	return &Router{store: s}
}

// Register wires GET /api/chats (list), GET /api/chats/search (across chats)
// and GET /api/chats/{id} (one chat with paginated messages).
//
// The search route is registered as an exact literal so it wins over the
// `/api/chats/` prefix pattern; without it, "search" would be read as a chat id.
func (rt *Router) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/chats", rt.handleList)
	mux.HandleFunc("/api/chats/search", rt.handleSearchAll)
	mux.HandleFunc("/api/chats/", rt.handleOne)
}

// handleSearchAll serves GET /api/chats/search?q=: which CHATS match, ranked.
//
// A different question from the per-chat handleSearch, which stays scoped to the
// chat being read (user decision): this answers "which conversation was that
// in", so it returns chats with their best line rather than every hit.
func (rt *Router) handleSearchAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	webhttp.WriteJSON(w, rt.store.SearchAll(r.Context(), r.URL.Query().Get("q")))
}
