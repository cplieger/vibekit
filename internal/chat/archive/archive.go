// Package archive implements chat RETENTION: the age-based purge and its
// scheduler. Nothing is archived and no chat moves — "archived" is computed from
// a chat's age against the retention window, never stored as a state.
package archive

import (
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
)

const chatFileSuffix = ".json"

// RetentionHeader is the projection of a chat a retention decision reads. Nothing
// else: a chat's messages, blocks, tool calls and diffs decide nothing here and
// cost megabytes to decode. The store implements the read.
type RetentionHeader struct {
	// SessionChain is every KAS session the chat has run on, current one last.
	SessionChain []string
	// UpdatedAt is last activity in Unix milliseconds; zero or negative means the
	// chat records none and purgeOne falls back to the file mtime.
	UpdatedAt int64
	// Drafting reports composer text typed and not sent, which Store.SetDraft
	// writes WITHOUT stamping UpdatedAt.
	Drafting bool
}

// StoreAccess is what retention needs from the chat store.
type StoreAccess interface {
	// Lock returns the per-chat mutex.
	Lock(chatID vibekit.ChatID) *sync.Mutex
	// Dir returns the store's base directory.
	Dir() string
	// LoadRetentionHeader reads a chat's retention projection without
	// materializing its messages.
	LoadRetentionHeader(chatID vibekit.ChatID) (RetentionHeader, error)
}

// Service implements the archive lifecycle operations.
type Service struct {
	store   StoreAccess
	onPurge func(chatID vibekit.ChatID, sessionChain []string)
	// isLive reports a running bridge; such a chat is never purged, however old.
	isLive func(chatID vibekit.ChatID) bool
	// hasOpenTab reports an open TAB, a different fact from isLive: a reader can
	// have a chat open with no bridge running.
	hasOpenTab func(chatID vibekit.ChatID) bool
}

// New creates an archive Service backed by the given StoreAccess.
func New(store StoreAccess, opts ...Option) *Service {
	s := &Service{store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Option configures the archive Service.
type Option func(*Service)

// WithLiveChats registers the predicate reporting active use. A live chat is
// exempt from purging regardless of age; retention is about abandoned work.
// Injected because this package cannot see the runtime that owns bridges.
func WithLiveChats(fn func(chatID vibekit.ChatID) bool) Option {
	return func(s *Service) { s.isLive = fn }
}

// WithOpenTabs registers the predicate reporting an OPEN TAB, which exempts a
// chat from purging regardless of age. It covers the reader the draft predicate
// misses: reading a chat stamps nothing the age test can see. Retention is
// therefore OPT-OUT for a chat left open forever, which is accepted. Injected
// because this package cannot see the tab store.
func WithOpenTabs(fn func(chatID vibekit.ChatID) bool) Option {
	return func(s *Service) { s.hasOpenTab = fn }
}

// WithOnPurge registers a callback fired after a chat is purged, carrying every
// KAS session it ran on, read before the file was removed. The purge reaps its OWN
// session directories through this rather than leaning on the orphan sweep, whose
// keep-list is derived by reading every chat file.
func WithOnPurge(fn func(chatID vibekit.ChatID, sessionChain []string)) Option {
	return func(s *Service) { s.onPurge = fn }
}
