//go:build !vibekit_test

package server

import "net/http"

// registerTestHooks mounts nothing: the SSE control surface under /api/test/ exists
// only in a binary built with -tags vibekit_test (testhooks_vibekittest.go).
func (s *Server) registerTestHooks(*http.ServeMux) {}
