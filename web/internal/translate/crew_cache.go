package translate

import (
	"sync"

	"vibekit/internal/api"
)

// crewMsgKey identifies a per-chat per-group crew message.
type crewMsgKey struct {
	ChatID api.ChatID
	Group  string
}

// crewCache manages the per-chat per-group crew message ID cache.
type crewCache struct {
	msgIDs   map[crewMsgKey]string
	byChatID map[api.ChatID][]crewMsgKey
	mu       sync.Mutex
}

func newCrewCache() *crewCache {
	return &crewCache{
		msgIDs:   make(map[crewMsgKey]string),
		byChatID: make(map[api.ChatID][]crewMsgKey),
	}
}

// ClearChat removes all cached entries for the given chatID.
func (cc *crewCache) ClearChat(chatID api.ChatID) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	for _, key := range cc.byChatID[chatID] {
		delete(cc.msgIDs, key)
	}
	delete(cc.byChatID, chatID)
}

func (cc *crewCache) lookup(chatID api.ChatID, groupKey string) (id string, ok bool) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	id, ok = cc.msgIDs[crewMsgKey{ChatID: chatID, Group: groupKey}]
	return id, ok
}

func (cc *crewCache) remember(chatID api.ChatID, groupKey, messageID string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	key := crewMsgKey{ChatID: chatID, Group: groupKey}
	if _, exists := cc.msgIDs[key]; !exists {
		cc.byChatID[chatID] = append(cc.byChatID[chatID], key)
	}
	cc.msgIDs[key] = messageID
}
