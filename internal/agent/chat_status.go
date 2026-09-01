package agent

// Last-declared chat status per chat.
//
// This is the one thing the connect-time turn_state synthesis needs that the
// assistant buffer does NOT hold: `chat_status` comes from KAS's focus_update
// channel (update_session_information), a session event rather than turn
// content, so it appears in no message and in no replay.
//
// Deliberately ephemeral and tiny: one entry per chat, replaced on each
// event, dropped when the turn ends. Never persisted, matching the live
// event's contract — cleared client-side on the next prompt and on
// transport:gap, so a bare replay cannot resurrect a stale "in_progress".

import (
	"maps"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
)

type chatStatusCache struct {
	byChat map[vibekit.ChatID]vibekit.ChatStatusPayload
	mu     sync.Mutex
}

func newChatStatusCache() *chatStatusCache {
	return &chatStatusCache{byChat: make(map[vibekit.ChatID]vibekit.ChatStatusPayload)}
}

// Set records a chat's latest self-declared status. A status can precede the
// turn's first content chunk (the agent declares intent before producing
// output), which is why this is keyed on the chat rather than hung off a turn.
func (c *chatStatusCache) Set(chatID vibekit.ChatID, p vibekit.ChatStatusPayload) {
	if chatID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byChat[chatID] = p
}

// Get returns a chat's last status.
func (c *chatStatusCache) Get(chatID vibekit.ChatID) vibekit.ChatStatusPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byChat[chatID]
}

// Snapshot copies every retained status, for the connect-time replay.
func (c *chatStatusCache) Snapshot() map[vibekit.ChatID]vibekit.ChatStatusPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.byChat)
}

// ClearAtTurnEnd drops a chat's status at turn end, so a later connect cannot
// report a finished turn's label as current — EXCEPT waiting_on_user, the one
// status whose whole meaning is that the turn ended and a person still owes
// an answer.
//
// The client renders `waiting_on_user` as a dot that survives turn end, so
// clearing it unconditionally made the dot exist only for a client connected
// when the event fired: a refresh, or a second device joining later, lost it
// — exactly the state someone picking the work up on another screen needs.
// Kept until the next status the agent declares or the chat going away.
func (c *chatStatusCache) ClearAtTurnEnd(chatID vibekit.ChatID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byChat[chatID].Status == vibekit.ChatStatusWaitingOnUser {
		return
	}
	delete(c.byChat, chatID)
}

// Clear drops a chat's status unconditionally. For a chat going away (closed or
// deleted), where no status can still be true of it.
func (c *chatStatusCache) Clear(chatID vibekit.ChatID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byChat, chatID)
}
