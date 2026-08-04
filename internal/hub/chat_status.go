package hub

// Last-declared chat status per chat.
//
// This is the one thing the connect-time turn_state synthesis needs that the
// assistant buffer does NOT hold. `chat_status` comes from KAS's focus_update
// channel (the model's update_session_information tool), which is a session
// event rather than turn content, so it appears in no message and in no
// replay — hub/turn_mirror.go was its only holder.
//
// Deliberately ephemeral and deliberately tiny: one entry per chat, replaced
// on each event, dropped when the turn ends. It is NEVER persisted, matching
// the live event's contract (see vibekit.md's chat_status row: cleared
// client-side on the next prompt and on transport:gap, precisely so a bare
// replay cannot resurrect a stale "in_progress").

import (
	"sync"

	"github.com/cplieger/vibekit/internal/api"
)

type chatStatusCache struct {
	byChat map[api.ChatID]api.ChatStatusPayload
	mu     sync.Mutex
}

func newChatStatusCache() *chatStatusCache {
	return &chatStatusCache{byChat: make(map[api.ChatID]api.ChatStatusPayload)}
}

// Set records a chat's latest self-declared status. A status can precede the
// turn's first content chunk (the agent declares intent before producing
// output), which is why this is keyed on the chat rather than hung off a turn.
func (c *chatStatusCache) Set(chatID api.ChatID, p api.ChatStatusPayload) {
	if chatID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byChat[chatID] = p
}

// Get returns a chat's last status.
func (c *chatStatusCache) Get(chatID api.ChatID) api.ChatStatusPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byChat[chatID]
}

// Clear drops a chat's status at turn end, so a later connect cannot report a
// finished turn's label as current.
func (c *chatStatusCache) Clear(chatID api.ChatID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byChat, chatID)
}
