package chat

import "net/http"

// Router owns the HTTP handler surface for the chat package. It holds
// a *Store reference and delegates all persistence to it, separating
// the HTTP routing/serialisation concern from the data layer.
//
// The Store's RegisterRoutes method delegates to Router.Register so
// the api.ChatStore interface contract is preserved while the HTTP
// concern is structurally separated.
type Router struct {
	store *Store
}

// NewRouter creates a Router backed by the given Store.
func NewRouter(s *Store) *Router {
	return &Router{store: s}
}

// Register wires GET /api/chats (list), GET /api/chats/{id}
// (one chat with paginated messages).
func (rt *Router) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/chats", rt.handleList)
	mux.HandleFunc("/api/chats/", rt.handleOne)
}
