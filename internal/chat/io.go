package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cplieger/jsoncap/v2"
	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// readCappedFile reads a file at path, enforcing the maxChatFileBytes
// size cap and the TOCTOU grow-during-read guard. Returns the raw bytes.
//
// Path is filepath.Clean'd up-front and rejected if it holds a ".."
// component or is non-absolute. Callers already pass paths derived from
// store.Dir() + a ValidChatID-checked chatID, but the local guard makes
// the safety property visible to CodeQL's go/path-injection analyzer
// (which doesn't follow ValidChatID across package boundaries).
//
// The traversal half is pathinside.HasDotDot, which matches a ".."
// COMPONENT rather than the two-character substring the test it replaced
// searched for. That is the whole behavioural delta: an ordinary chat
// file whose name happens to hold two adjacent dots ("a..b.json",
// "..extras.json") is now read instead of rejected as unsafe. Nothing
// that was refused for traversal is accepted — see below.
//
// KNOWN VACUITY, deliberately preserved: this predicate runs on the
// CLEANED value, and filepath.Clean has already collapsed every ".."
// and clamped at the filesystem root, so no ".." component can survive
// in an absolute cleaned path and the traversal test cannot fire. It is
// kept because it is what CodeQL reads as the sanitizer, and it is kept
// on `clean` rather than moved to the raw `path` on purpose: judging the
// raw value WOULD be a real gate, but it would also refuse every read
// under a KIRO_CONFIG_DIR an operator wrote with a ".." segment
// ("/config/../data"), which ConfigFromEnv accepts verbatim today.
// Turning this into a live gate is a config-normalisation change
// (canonicalise ConfigDir at load, then judge the raw path here), not a
// one-line swap — and failing every chat read on a legal operator
// config is the failure shape invariant 6 exists to prevent.
func readCappedFile(path, label string) ([]byte, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || pathinside.HasDotDot(clean) {
		return nil, fmt.Errorf("%s: rejected unsafe path %q", label, path)
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > maxChatFileBytes {
		return nil, fmt.Errorf("%s file too large: %d bytes (max %d)",
			label, st.Size(), maxChatFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxChatFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxChatFileBytes {
		return nil, fmt.Errorf("%s grew during load: read %d bytes (max %d)",
			label, len(data), maxChatFileBytes)
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
	h.MessageCount = countJSONArrayElements(h.Messages)
	return &h.ChatHeader, nil
}

// countJSONArrayElements counts top-level elements in a JSON array without
// materializing them: each element is token-skipped via jsoncap, so
// counting never allocates per-element buffers. Returns 0 for
// nil/empty/invalid input (count-so-far when an element mid-array is
// malformed).
//
// NOTE: internal/chat/archive/helpers.go carries an aligned copy (archive
// cannot import chat, and exporting a generic JSON utility from archive
// just for this would warp its surface); keep the two in sync.
func countJSONArrayElements(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	dec := jsoncap.NewDecoder(bytes.NewReader(raw), 0)
	if ok, err := dec.Open('['); err != nil || !ok {
		return 0
	}
	count := 0
	for dec.More() {
		if err := dec.Skip(); err != nil {
			break
		}
		count++
	}
	return count
}
