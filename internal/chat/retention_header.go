package chat

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/jsoncap/v2"
	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/chat/archive"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// The projection's keys, which are vibekit.Chat's JSON names — so a rename there
// has to land here too, the one cost of projecting by key instead of by struct.
const (
	keyUpdatedAt     = "updated_at"
	keySessionID     = "acp_session_id"
	keyPriorSessions = "prior_acp_session_ids"
	keyDraft         = "draft"
)

// LoadRetentionHeader reads the retention projection of chatID's file.
//
// The purge used to answer its three small questions by decoding the WHOLE chat,
// once per chat per pass, over files that reach several MB of tool output no
// retention decision looks at.
func (s *Store) LoadRetentionHeader(chatID vibekit.ChatID) (archive.RetentionHeader, error) {
	path, err := s.pathFor(chatID)
	if err != nil {
		return archive.RetentionHeader{}, err
	}
	return readRetentionHeader(path, "chat "+string(chatID))
}

// readRetentionHeader streams a chat file and decodes ONLY the retention fields,
// token-skipping every other value.
//
// A projection rather than a sidecar the writer keeps in step: a second copy can
// disagree with the record, and the reaper would then age a chat from a stamp that
// is not the chat's. The walk does not stop at the last field it wants, though
// write order would allow it — a later `draft` key would be missed and a chat
// somebody is typing in purged.
func readRetentionHeader(path, label string) (archive.RetentionHeader, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || pathinside.HasDotDot(clean) {
		return archive.RetentionHeader{}, fmt.Errorf("%s: rejected unsafe path %q", label, path)
	}
	// OpenRegular, not os.Open, for the reason readCappedFile documents: a FIFO at
	// a chat file name blocks in open(2) with no deadline able to rescue it.
	f, info, err := atomicfile.OpenRegular(clean)
	if err != nil {
		return archive.RetentionHeader{}, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > maxChatFileBytes {
		return archive.RetentionHeader{}, fmt.Errorf("%s: %d bytes exceeds the %d-byte cap",
			label, info.Size(), maxChatFileBytes)
	}
	h, err := decodeRetentionHeader(bufio.NewReader(io.LimitReader(f, maxChatFileBytes)))
	if err != nil {
		return archive.RetentionHeader{}, fmt.Errorf("parse %s: %w", label, err)
	}
	return h, nil
}

// decodeRetentionHeader is the projection itself, over any reader, so the parsing
// contract is testable without a file. Not one Message, block, tool call or diff
// is materialized: the messages array is walked at the token level.
func decodeRetentionHeader(r io.Reader) (archive.RetentionHeader, error) {
	var (
		h        archive.RetentionHeader
		sessions vibekit.ChatHeader
	)
	dec := jsoncap.NewDecoder(r, 0)
	err := dec.Object(func(key string) error {
		// EqualFold, not ==, because encoding/json matches a field tag
		// case-insensitively and encoding/json is the OTHER reader of this same
		// file. A chat carrying "Draft" would read as draft-free here and be
		// unlinked with unsent words in it, while the store's full load found the
		// draft — two readers of one file disagreeing silently. jsoncap.Object's
		// own doc comment asks for this predicate for exactly that reason.
		switch {
		case strings.EqualFold(key, keyUpdatedAt):
			return dec.Decode(&h.UpdatedAt)
		case strings.EqualFold(key, keySessionID):
			return dec.Decode(&sessions.ACPSessionID)
		case strings.EqualFold(key, keyPriorSessions):
			return dec.Decode(&sessions.PriorACPSessionIDs)
		case strings.EqualFold(key, keyDraft):
			// The draft's PRESENCE, not its text: keeping the words would put a
			// draft-sized copy of every chat in memory to answer a boolean.
			var draft string
			if derr := dec.Decode(&draft); derr != nil {
				return derr
			}
			h.Drafting = draft != ""
			return nil
		default:
			return dec.Skip()
		}
	})
	if err != nil {
		return archive.RetentionHeader{}, err
	}
	// vibekit's own composition of the two id fields, called rather than
	// reimplemented so this view cannot disagree about a chat's retention set.
	h.SessionChain = sessions.SessionChain()
	return h, nil
}
