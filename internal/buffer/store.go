package buffer

import (
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// Store is a concurrency-safe store for per-chat assistant buffers.
// It owns its own mutex so buffer operations don't contend with
// unrelated Hub state.
type Store struct {
	bufs map[vibekit.ChatID]*Buffer
	mu   sync.Mutex
}

// NewStore creates a new buffer store.
func NewStore() *Store {
	return &Store{bufs: make(map[vibekit.ChatID]*Buffer)}
}

// GetOrInit returns the chat's in-flight assistant buffer, creating
// one if this is the start of a new turn.
func (bs *Store) GetOrInit(chatID vibekit.ChatID) *Buffer {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if buf, ok := bs.bufs[chatID]; ok {
		return buf
	}
	buf := &Buffer{
		ToolStartTimes: make(map[string]int64),
	}
	bs.bufs[chatID] = buf
	return buf
}

// Take returns and removes the chat's assistant buffer.
func (bs *Store) Take(chatID vibekit.ChatID) (*Buffer, bool) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	buf, ok := bs.bufs[chatID]
	if ok {
		delete(bs.bufs, chatID)
	}
	return buf, ok
}

// Get returns the buffer for a chat without removing it.
func (bs *Store) Get(chatID vibekit.ChatID) *Buffer {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.bufs[chatID]
}

// Delete removes the buffer for a chat.
func (bs *Store) Delete(chatID vibekit.ChatID) {
	bs.mu.Lock()
	delete(bs.bufs, chatID)
	bs.mu.Unlock()
}
