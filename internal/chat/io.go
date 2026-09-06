package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/jsoncap/v2"
	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// readCappedFile reads a file at path, enforcing the maxChatFileBytes size
// cap and the TOCTOU grow-during-read guard. Returns the raw bytes.
//
// THE NAME IS OPENED THROUGH atomicfile.OpenRegular, NOT os.Open: os.Open on
// a FIFO blocks in open(2) until a writer appears with no context deadline
// able to rescue it (measured on go1.27.0: os.Open, os.ReadFile and
// io.ReadAll all hang past 2s, while the same open with O_NONBLOCK returns
// immediately). This directory is on the /config volume, reachable by the
// agent's own shell, so `mkfifo <chats>/<valid-chat-id>.json` was a
// one-command permanent wedge across every reader that serialises behind
// it. OpenRegular refuses a directory, FIFO, device node or socket with
// ErrNotRegular and a symlink at the final component with
// ErrSymlinkTarget, both on the open itself, so the object judged and the
// object read are the same one.
//
// Path is filepath.Clean'd up-front and rejected if it holds a ".."
// component or is non-absolute — a guard that makes the safety property
// visible to CodeQL's go/path-injection analyzer, which doesn't follow
// ValidChatID across package boundaries.
//
// KNOWN VACUITY, deliberately preserved: this predicate runs on the
// CLEANED value, so no ".." component can survive and the traversal test
// cannot fire. It is kept because CodeQL reads it as the sanitizer, and
// judging the raw value instead would also refuse a legal
// KIRO_CONFIG_DIR containing a ".." segment.
func readCappedFile(path, label string) ([]byte, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || pathinside.HasDotDot(clean) {
		return nil, fmt.Errorf("%s: rejected unsafe path %q", label, path)
	}
	f, _, err := atomicfile.OpenRegular(clean)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// ReadBoundedFile is the size cap AND the grow-during-read guard: it
	// stats the descriptor, refuses over maxChatFileBytes, and refuses
	// again if the file grew past the limit while being read.
	// context.Background() because no read path here carries one — threading
	// one through would change the archive interface for a bound that is
	// already enforced.
	data, err := atomicfile.ReadBoundedFile(context.Background(), f, maxChatFileBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return data, nil
}

// readChatFile reads a chat JSON file at path, enforcing the
// maxChatFileBytes size cap and the TOCTOU grow-during-read guard.
func readChatFile(path, label string) (*vibekit.Chat, error) {
	data, err := readCappedFile(path, label)
	if err != nil {
		return nil, err
	}
	var c vibekit.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// chatHeaderOnDisk is a partial-unmarshal struct that skips the Messages
// array. Embeds vibekit.ChatHeader so new fields automatically flow through
// without a manual mapping step. json.RawMessage avoids allocating/parsing
// the message objects, bounding List's memory to O(N × header_size)
// instead of O(N × full_chat).
type chatHeaderOnDisk struct {
	Messages json.RawMessage `json:"messages"`
	vibekit.ChatHeader
}

// readChatHeader reads a chat JSON file and returns only the header fields,
// skipping full message deserialization. The message count is derived from
// counting top-level array elements in the raw JSON.
func readChatHeader(path, label string) (*vibekit.ChatHeader, error) {
	data, err := readCappedFile(path, label)
	if err != nil {
		return nil, err
	}
	var h chatHeaderOnDisk
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	h.MessageCount, h.LastTurnOutcome = scanMessagesArray(h.Messages)
	return &h.ChatHeader, nil
}

// outcomeProbe is the ONE field this scan reads off a message. Every other key
// is skipped by encoding/json's own unknown-field handling, which allocates
// nothing for what it discards.
type outcomeProbe struct {
	TurnOutcome vibekit.TurnOutcome `json:"turn_outcome"`
}

// scanMessagesArray walks a chat file's raw messages array once and answers both
// header facts it carries: how many messages there are, and the NEWEST turn
// outcome any of them stamped (see vibekit.ChatHeader.LastTurnOutcome). Returns
// (0, "") for nil/empty/invalid input, and keeps the outcome empty on any decode
// failure so a malformed file still yields a usable header.
//
// One pass rather than two, because the count already required walking every
// element: the outcome is read from the element the walk is at instead of
// discarding it.
//
// An element that is not an object is COUNTED and contributes no outcome. That
// is why the element is consumed with Decode rather than jsoncap's Object: a
// non-object element leaves Object's Open('{') mid-token — for a nested array it
// consumes only the '[' — which desynchronises the count for the rest of the
// array. Decode reads one complete value and advances past it before it
// unmarshals, so an UnmarshalTypeError still leaves the stream positioned on the
// next element. A SYNTAX error does not, so the walk stops there.
func scanMessagesArray(raw json.RawMessage) (count int, last vibekit.TurnOutcome) {
	if len(raw) == 0 {
		return 0, ""
	}
	dec := jsoncap.NewDecoder(bytes.NewReader(raw), 0)
	if ok, err := dec.Open('['); err != nil || !ok {
		return 0, ""
	}
	for dec.More() {
		var probe outcomeProbe
		err := dec.Decode(&probe)
		var typeErr *json.UnmarshalTypeError
		switch {
		case err == nil:
			if probe.TurnOutcome != "" {
				last = probe.TurnOutcome
			}
		case errors.As(err, &typeErr):
			// A non-object element: consumed and skipped past, so counting
			// stays correct and there is no outcome to read.
		default:
			return count, last
		}
		count++
	}
	return count, last
}
