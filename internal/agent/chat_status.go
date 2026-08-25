package agent

// Last-declared chat status per chat.
//
// This is the one thing the connect-time turn_state synthesis needs that the
// assistant buffer does NOT hold. `chat_status` comes from KAS's focus_update
// channel (the model's update_session_information tool), which is a session
// event rather than turn content, so it appears in no message and in no
// replay — the deleted turn_mirror.go was its only holder.
//
// Deliberately ephemeral and deliberately tiny: one entry per chat, replaced
// on each event, dropped when the turn ends. It is NEVER persisted, matching
// the live event's contract (see vibekit.md's chat_status row: cleared
// client-side on the next prompt and on transport:gap, precisely so a bare
// replay cannot resurrect a stale "in_progress").

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
// report a finished turn's label as current — EXCEPT waiting_on_user, which is
// the one status whose whole meaning is that the turn ended and a person still
// owes an answer.
//
// Clearing that one unconditionally is what made the amber dot unrecoverable.
// The client renders `waiting_on_user` as a dot that survives turn end (it is
// the only status that does), but the cache dropped it in the same breath as the
// turn — so the dot existed only for a client that happened to be connected when
// the event fired. A refresh lost it, and so did a second device joining later,
// which is exactly the state a person picking the work up on another screen
// needs to see. Keeping it costs one map entry per waiting chat, cleared by the
// next status the agent declares or by the chat going away.
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
