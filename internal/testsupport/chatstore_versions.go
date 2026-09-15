package testsupport

import (
	"strconv"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// chatVersions is the per-chat `chat` counter both fakes mint from, so a
// stamp built from a fake's return is non-empty and moves per write exactly as
// the real store's does. Not the real registry: a fake reaches no resolver.
type chatVersions struct {
	counters map[vibekit.ChatID]uint64
	mu       sync.Mutex
}

// bump mints the next `chat` version for id.
func (v *chatVersions) bump(id vibekit.ChatID) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.counters == nil {
		v.counters = make(map[vibekit.ChatID]uint64)
	}
	v.counters[id]++
	return strconv.FormatUint(v.counters[id], 10)
}

// current reads the `chat` version the last write to id minted, "" before any.
func (v *chatVersions) current(id vibekit.ChatID) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.counters[id]
	if !ok {
		return ""
	}
	return strconv.FormatUint(n, 10)
}

// stamped returns evt carrying a `chat` stamp for chatID at version.
func stamped(evt vibekit.ServerEvent, chatID vibekit.ChatID, version string) vibekit.ServerEvent {
	evt.Subject = vibekit.NewSubjectStamp("chat", string(chatID), version)
	return evt
}
