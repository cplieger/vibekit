//go:build vibekit_test

package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v3"
)

// testPortEnv names the listener port for a test binary. The browser-mode suite
// starts one beside whatever already holds the default port.
const testPortEnv = "VIBEKIT_TEST_PORT"

func init() {
	if p := os.Getenv(testPortEnv); p != "" {
		listenPort = p
	}
}

// sseProbe is the runtime as the SSE control surface reads it. *agent.Runtime
// satisfies it; a server wired to a narrower engine mounts no hooks.
type sseProbe interface {
	SSEClientCount() int
	SSEConnects() (legacy, v3 uint64)
	CloseNextSSEAfter(n int)
}

// pushProbe is the push service as the control surface reads it: the presence
// table and the send filter's counters. *push.Service satisfies it; a server
// wired to a narrower push service reports an empty table.
type pushProbe interface {
	PresenceRows() []push.PresenceRow
	PresenceTransitions() (alive, expired uint64)
	Suppressed(kind vibekit.PushKind) uint64
}

// sseProbeResponse is GET /api/test/sse's body.
type sseProbeResponse struct {
	Suppressed      map[vibekit.PushKind]uint64 `json:"push_suppressed_total"`
	Presence        []presenceRow               `json:"presence"`
	Clients         int                         `json:"clients"`
	LegacyConnect   uint64                      `json:"legacy_connect"`
	V3Connect       uint64                      `json:"v3_connect"`
	PresenceAlive   uint64                      `json:"presence_alive"`
	PresenceExpired uint64                      `json:"presence_expired"`
}

// presenceRow is one tag's fold: its connection count, its last acknowledgement
// (RFC 3339) and the verdict the send filter reads.
type presenceRow struct {
	Tag         string `json:"tag"`
	LastAliveAt string `json:"lastAliveAt"`
	Connected   int    `json:"connected"`
	Gone        bool   `json:"gone"`
}

type closeAfterRequest struct {
	After int `json:"after"`
}

// registerTestHooks mounts the SSE control surface the browser-mode suite drives:
// the connection census with the presence table, and the close-after cut. Test
// builds only.
func (s *Server) registerTestHooks(mux *http.ServeMux) {
	probe, ok := s.agent.(sseProbe)
	if !ok {
		return
	}
	pushP, _ := s.push.(pushProbe)
	slog.Warn("test-only SSE control surface mounted under /api/test/; this is a vibekit_test build")
	mux.HandleFunc("GET /api/test/sse", func(w http.ResponseWriter, _ *http.Request) {
		legacy, v3 := probe.SSEConnects()
		resp := sseProbeResponse{
			Presence:      []presenceRow{},
			Suppressed:    map[vibekit.PushKind]uint64{},
			Clients:       probe.SSEClientCount(),
			LegacyConnect: legacy,
			V3Connect:     v3,
		}
		if pushP != nil {
			for _, row := range pushP.PresenceRows() {
				resp.Presence = append(resp.Presence, presenceRow{
					Tag:         row.Tag,
					LastAliveAt: row.LastAliveAt.UTC().Format(time.RFC3339Nano),
					Connected:   row.Connected,
					Gone:        row.Gone,
				})
			}
			resp.PresenceAlive, resp.PresenceExpired = pushP.PresenceTransitions()
			for _, kr := range push.Kinds() {
				resp.Suppressed[kr.Kind] = pushP.Suppressed(kr.Kind)
			}
		}
		webhttp.WriteJSON(w, resp)
	})
	mux.HandleFunc("POST /api/test/sse/close-after", func(w http.ResponseWriter, r *http.Request) {
		var req closeAfterRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || req.After <= 0 {
			httpreply.BadRequest(w, "after must be a positive frame count")
			return
		}
		probe.CloseNextSSEAfter(req.After)
		w.WriteHeader(http.StatusNoContent)
	})
}
