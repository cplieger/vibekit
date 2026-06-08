package hub

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/metrics"
)

// sseController owns the SSE client set and replay ring buffer. It has
// its own mutex so SSE fan-out doesn't contend with bridge/command
// operations that share Hub.mu.
type sseController struct {
	clients map[*sseClient]struct{}
	replay  *replayRing
	seq     atomic.Uint64
	mu      sync.Mutex
}

func newSSEController(ringSize int) *sseController {
	return &sseController{
		clients: make(map[*sseClient]struct{}),
		replay:  newReplayRing(ringSize),
	}
}

// emit marshals and fans out an event to all subscribed SSE clients.
func (sc *sseController) emit(evt api.ServerEvent, data []byte) {
	se := sseEvent{data: data, chatID: evt.ChatID, eventID: sc.seq.Add(1)}
	sc.mu.Lock()
	sc.replay.Append(se)
	for client := range sc.clients {
		if client.chatID != "" && evt.ChatID != "" && client.chatID != evt.ChatID {
			continue
		}
		select {
		case client.ch <- se:
		default:
			slog.Warn("evicting slow SSE client", "chat_filter", client.chatID)
			client.cancel()
			delete(sc.clients, client)
		}
	}
	sc.mu.Unlock()
}

// add registers a new SSE client.
func (sc *sseController) add(client *sseClient) {
	sc.mu.Lock()
	sc.clients[client] = struct{}{}
	sc.mu.Unlock()
	metrics.SSEClients.Inc()
}

// remove unregisters an SSE client.
func (sc *sseController) remove(client *sseClient) {
	sc.mu.Lock()
	delete(sc.clients, client)
	sc.mu.Unlock()
	metrics.SSEClients.Dec()
}

// closeAll cancels all connected SSE clients (used during shutdown).
func (sc *sseController) closeAll() {
	sc.mu.Lock()
	for client := range sc.clients {
		client.cancel()
	}
	sc.mu.Unlock()
}

// bounds returns (floor, head) of the replay buffer.
func (sc *sseController) bounds() (floor, head uint64) {
	ringFloor, ringHead := sc.replay.Bounds()
	head = sc.seq.Load()
	if ringHead > 0 {
		floor = ringFloor
	}
	return floor, head
}
