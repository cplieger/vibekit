// Package archive implements chat RETENTION: the age-based purge and its
// scheduler.
//
// It no longer archives anything. Chats do not move — "archived" is computed
// from a chat's age against the retention window rather than stored as a state —
// so the move/list/restore/summary surface this package used to carry is gone,
// along with the `archive/` subdirectory it wrote to. What remains is the one
// job KAS has no concept of: deciding which chats a user wants kept.
//
// Kept separate from the core chat store so the purge scheduler stays
// independently testable.
package archive

import (
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
)

const chatFileSuffix = ".json"

// StoreAccess is the narrow interface the archive subsystem requires
// from the chat store. Keeps the dependency minimal and testable.
type StoreAccess interface {
	// Lock returns the per-chat mutex for serialization.
	Lock(chatID vibekit.ChatID) *sync.Mutex
	// Dir returns the store's base directory.
	Dir() string
	// Load reads a chat from the active directory.
	Load(chatID vibekit.ChatID) (*vibekit.Chat, error)
}

// Service implements the archive lifecycle operations.
type Service struct {
	store   StoreAccess
	onPurge func(chatID vibekit.ChatID, sessionChain []string)
	// isLive reports whether a chat is in active use (a running bridge). A
	// live chat is never purged, however old — see purgeOne.
	isLive func(chatID vibekit.ChatID) bool
	// hasOpenTab reports whether a chat has an open TAB. The second exemption,
	// and a different fact from isLive: a reader can have a chat open on the strip
	// with no bridge running, which is precisely the reader the age test cannot
	// see. See purgeOne for what it costs.
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

// WithLiveChats registers the predicate that reports whether a chat is in
// active use. A live chat is exempt from purging regardless of age: retention
// is about abandoned work, and deleting a chat out from under its own open tab
// is never what a retention window meant.
//
// Injected because this package cannot see the runtime that owns bridges.
func WithLiveChats(fn func(chatID vibekit.ChatID) bool) Option {
	return func(s *Service) { s.isLive = fn }
}

// WithOpenTabs registers the predicate that reports whether a chat has an OPEN
// TAB. A chat with one is exempt from purging regardless of age.
//
// The second of the two exemptions the retention design owes, and the one the
// server could not answer until the tab set became a modelled collection: before
// it, "which tabs are open" was a string list inside a preferences document that
// validated nothing against reality.
//
// It answers the case the draft predicate misses. A reader who is READING an old
// chat rather than typing into it leaves no trace the age test can see —
// Store.SetDraft deliberately does not stamp UpdatedAt, and simply having the
// chat open stamps nothing at all — so before this the tab vanished under the
// cursor, on every device at once.
//
// THIS MAKES RETENTION OPT-OUT for a chat left open forever, and that is
// ACCEPTED. It is the honest reading of "in use": it is what a reader expects of
// a tab they deliberately kept, and the alternative is closing a tab under
// someone to satisfy a timer.
//
// Injected because this package cannot see the tab store.
func WithOpenTabs(fn func(chatID vibekit.ChatID) bool) Option {
	return func(s *Service) { s.hasOpenTab = fn }
}

// WithOnPurge registers a callback fired after a chat is purged.
//
// sessionChain is every KAS session the purged chat ran on, read before the
// chat file was removed. The purge reaps its OWN session directories through
// this: retention must not depend on the hourly orphan sweep finding them,
// because that sweep's keep-list is derived by reading every chat file and is
// the destructive leg of the system.
func WithOnPurge(fn func(chatID vibekit.ChatID, sessionChain []string)) Option {
	return func(s *Service) { s.onPurge = fn }
}
