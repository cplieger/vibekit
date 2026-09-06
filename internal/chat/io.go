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

// readCappedFile reads the file at path, enforcing the maxChatFileBytes cap and
// the TOCTOU grow-during-read guard.
//
// OpenRegular, not os.Open: os.Open on a FIFO blocks in open(2) with no context
// deadline able to rescue it, and the chats directory is reachable by the agent's
// own shell, so one mkfifo wedged every reader. The clean-and-reject guard is
// VACUOUS after Clean and kept anyway: CodeQL's go/path-injection analyzer reads
// it as the sanitizer, and judging the raw value would refuse a legal "..".
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
	// context.Background() because no read path here carries one.
	data, err := atomicfile.ReadBoundedFile(context.Background(), f, maxChatFileBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return data, nil
}

// readChatFile reads and decodes a chat JSON file, bounded by readCappedFile.
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

// chatHeaderOnDisk skips the Messages array on unmarshal: the RawMessage bounds
// List's memory to O(N × header_size) instead of O(N × full_chat). Embedding
// ChatHeader means a new header field needs no mapping step.
type chatHeaderOnDisk struct {
	Messages json.RawMessage `json:"messages"`
	vibekit.ChatHeader
}

// readChatHeader reads a chat JSON file and returns only the header fields,
// skipping full message deserialization.
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

// outcomeProbe is the ONE field this scan reads off a message; encoding/json
// discards every other key without allocating.
type outcomeProbe struct {
	TurnOutcome vibekit.TurnOutcome `json:"turn_outcome"`
}

// scanMessagesArray walks a chat file's raw messages array once and answers both
// header facts it carries: the message count, and the NEWEST turn outcome any of
// them stamped. Returns (0, "") for nil, empty or invalid input, and stays usable
// on a decode failure.
//
// Decode rather than jsoncap's Object: a non-object element leaves Object
// mid-token and desynchronises the count, while Decode advances past a complete
// value, leaving the stream on the next element. A syntax error stops the walk.
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
			// A non-object element: skipped past, so the count stays correct.
		default:
			return count, last
		}
		count++
	}
	return count, last
}
