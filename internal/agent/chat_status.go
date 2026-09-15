package agent

// Last-declared chat status per chat.
//
// This is the one input the connect-time status_snapshot needs that the assistant
// buffer does NOT hold: `chat_status` comes from KAS's focus_update channel
// (update_session_information), a session event rather than turn content, so it
// appears in no message and in no replay.
//
// Deliberately ephemeral and tiny: one entry per chat, MERGED on each event
// (MergeStamped owns why), dropped when the turn ends. Never persisted, matching the
// live event's contract — cleared client-side on the next prompt and on
// transport:gap, so a bare replay cannot resurrect a stale "in_progress".

import (
	"cmp"
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

type chatStatusCache struct {
	byChat map[vibekit.ChatID]vibekit.ChatStatusPayload
	// versions holds the `status` counter. The projection it certifies is the
	// RETAINED WAITING SET (what status_snapshot carries), so a write that moves
	// only a non-waiting row does not mint: ClearAtTurnEnd never does, ClearWaiting
	// and Clear do only when the row they remove is waiting_on_user, and Merge
	// always does, because the frame it feeds is published to every client.
	versions *subject.Versions
	mu       sync.Mutex
}

func newChatStatusCache() *chatStatusCache {
	return &chatStatusCache{byChat: make(map[vibekit.ChatID]vibekit.ChatStatusPayload)}
}

// MergeStamped records a chat's latest self-declared status against what the chat already
// holds and returns the effective payload, which is what the caller publishes, with the
// `status` mint the published frame carries; both come out of one critical section, which
// is why it is the one helper bus.emit is allowed to stamp from. KAS's focus channel is
// omit-if-unchanged on every field, so an EMPTY field means ABSENT and never a clear;
// replacing the payload destroyed a retained waiting_on_user on the next description-only
// declaration. A both-empty payload IS a clear and is tested BEFORE the merge, or it would
// merge to whatever the entry held and re-publish it. A status can precede the turn's first
// content chunk (the agent declares intent before producing output), which is why this is
// keyed on the chat rather than hung off a turn. A chat-less declaration merges against
// nothing and mints nothing; its stamp is the current version, read in the same section.
func (c *chatStatusCache) MergeStamped(chatID vibekit.ChatID, p vibekit.ChatStatusPayload) (vibekit.ChatStatusPayload, *vibekit.SubjectStamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if chatID == "" {
		// A global event has no chat to merge against, and returning the zero payload here
		// would blank the frame emit publishes.
		current, _ := c.registry().Current(subject.KindStatus, "")
		return p, statusStamp(current)
	}
	// An entry exists iff the last declaration carried something. Only
	// Runtime.DischargeWaiting produces a both-empty payload; handleFocusUpdate returns
	// early on one, which is what keeps the discharge's frame distinguishable.
	if p.Status == "" && p.Description == "" {
		delete(c.byChat, chatID)
		return p, statusStamp(c.registry().BumpCounter(subject.KindStatus, ""))
	}
	prev := c.byChat[chatID]
	p.Status = cmp.Or(p.Status, prev.Status)
	p.Description = cmp.Or(p.Description, prev.Description)
	c.byChat[chatID] = p
	return p, statusStamp(c.registry().BumpCounter(subject.KindStatus, ""))
}

// registry returns the versions the cache mints into, defaulting to a private one so
// a cache built without wiring still returns honest stamps. Callers hold c.mu.
func (c *chatStatusCache) registry() *subject.Versions {
	if c.versions == nil {
		c.versions = &subject.Versions{}
	}
	return c.versions
}

// statusStamp is the `status` stamp at version.
func statusStamp(version string) *vibekit.SubjectStamp {
	return vibekit.NewSubjectStamp(string(subject.KindStatus), "", version)
}

// waitingRowsLocked is the retained waiting_on_user set minus the chats in busy, in
// chat order: a chat whose turn is running must still suppress a stale
// waiting_on_user, and a PRIME's chat is covered the same way. Callers hold c.mu.
func (c *chatStatusCache) waitingRowsLocked(busy map[vibekit.ChatID]openTurnFacts) []vibekit.StatusRow {
	rows := make([]vibekit.StatusRow, 0, len(c.byChat))
	for id, p := range c.byChat {
		if _, isBusy := busy[id]; isBusy || p.Status != vibekit.ChatStatusWaitingOnUser {
			continue
		}
		rows = append(rows, vibekit.StatusRow{ChatID: id, Status: p.Status, Description: p.Description})
	}
	slices.SortFunc(rows, func(a, b vibekit.StatusRow) int { return cmp.Compare(a.ChatID, b.ChatID) })
	return rows
}

// SnapshotStamped is the status_snapshot payload with its `status` stamp, for the
// v3 connect hook. The COUNTER IS READ FIRST, then the rows: a mutation landing
// between the two puts its row in the set and its bump outside the stamp, so the
// client holds a set at least as new as its version and the next digest answers
// changed. The reverse order would allow a stamp newer than the set and a false
// unchanged across a connection loss. Both reads are under c.mu here, so the order
// is belt and braces for this store; it is normative for the pending snapshot,
// whose three stores cannot share a section.
func (c *chatStatusCache) SnapshotStamped(busy map[vibekit.ChatID]openTurnFacts) (vibekit.StatusSnapshotPayload, *vibekit.SubjectStamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	version, _ := c.registry().Current(subject.KindStatus, "")
	rows := c.waitingRowsLocked(busy)
	return vibekit.StatusSnapshotPayload{Rows: rows}, statusStamp(version)
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

// ClearWaiting drops a chat's status only when it IS the retained waiting_on_user
// claim, and reports whether one went. Every other status belongs to the turn that
// declared it and ClearAtTurnEnd owns its removal: a steer arrives mid-turn, where
// the live entry is the agent's own in_progress line rather than a claim the user
// just answered.
//
// Removing a waiting row changes the certified projection, so it mints under c.mu:
// without the bump a digest between this delete and the discharge's own frame read
// unchanged for a set that shrank. The discharge's MergeStamped then bumps a second
// time, which is one spurious changed and never a false unchanged.
func (c *chatStatusCache) ClearWaiting(chatID vibekit.ChatID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byChat[chatID].Status != vibekit.ChatStatusWaitingOnUser {
		return false
	}
	delete(c.byChat, chatID)
	c.registry().BumpCounter(subject.KindStatus, "")
	return true
}

// Clear drops a chat's status unconditionally. For a chat going away (closed or
// deleted), where no status can still be true of it. Mints only when the row it
// removes was the retained waiting_on_user claim, for ClearWaiting's reason.
func (c *chatStatusCache) Clear(chatID vibekit.ChatID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiting := c.byChat[chatID].Status == vibekit.ChatStatusWaitingOnUser
	delete(c.byChat, chatID)
	if waiting {
		c.registry().BumpCounter(subject.KindStatus, "")
	}
}
