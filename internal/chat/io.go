package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/jsoncap/v2"
	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// keyMessages is vibekit.Chat's JSON name for the transcript array, the one key
// the header scan must recognise rather than capture.
const keyMessages = "messages"

// openChatFile opens path for reading, with the FileInfo the open produced.
//
// atomicfile.OpenRegular, NOT os.Open: os.Open on a FIFO blocks in open(2) with no
// deadline able to rescue it (measured on go1.27.0, past 2s), and this directory
// is on the /config volume the agent's own shell can reach, so
// `mkfifo <chats>/<valid-chat-id>.json` was a one-command permanent wedge for
// every serialised reader. OpenRegular refuses a non-regular file and a
// final-component symlink on the open itself.
func openChatFile(path, label string) (*os.File, os.FileInfo, error) {
	// The guard exists to make the safety property visible to CodeQL's
	// go/path-injection analyzer, which does not follow ValidChatID across
	// packages. KNOWN VACUITY, deliberately preserved: it runs on the CLEANED
	// value, so no ".." component can survive and the traversal test cannot fire.
	// Judging the raw value would refuse a legal KIRO_CONFIG_DIR holding "..".
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || pathinside.HasDotDot(clean) {
		return nil, nil, fmt.Errorf("%s: rejected unsafe path %q", label, path)
	}
	return atomicfile.OpenRegular(clean)
}

// readCappedFile reads a whole file at path under fileCap, plus the TOCTOU
// grow-during-read guard. Returns the raw bytes.
//
// Whole-file is correct HERE and only here: readChatFile runs one chat at a
// time under that chat's own lock. The header path carries the 8x
// readHeadersParallel multiplier and streams instead.
func readCappedFile(path, label string, fileCap chatFileCap) ([]byte, error) {
	f, info, err := openChatFile(path, label)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// ReadBoundedFile is the size cap AND the grow-during-read guard: it stats
	// the descriptor, refuses over the bound, and refuses again if the file grew
	// past it while being read. chatFileCap.readBound says what an unlimited cap
	// passes and why the guard survives it.
	// context.Background() because no read path here carries one — threading
	// one through would change the archive interface for a bound that is
	// already enforced.
	data, err := atomicfile.ReadBoundedFile(context.Background(), f, fileCap.readBound(info.Size()))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return data, nil
}

// readChatFile reads a chat JSON file at path, enforcing fileCap and the
// TOCTOU grow-during-read guard.
func readChatFile(path, label string, fileCap chatFileCap) (*vibekit.Chat, error) {
	data, err := readCappedFile(path, label, fileCap)
	if err != nil {
		return nil, err
	}
	var c vibekit.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// chatHeaderOnDisk is the header projection's decode target. Embeds
// vibekit.ChatHeader so a new header field flows through with no mapping step
// here; MessageCount is not on the wire and is counted separately.
type chatHeaderOnDisk struct {
	vibekit.ChatHeader
}

// readChatHeader STREAMS a chat file and returns only its header fields, because
// readHeadersParallel runs this at 8 workers per chat: reading first cost 8x the
// largest chats at once, and nothing here holds a message byte.
//
// Two independent gates: maxHeaderScanBytes bounds the scan even when fileCap is
// unlimited, and fileCap refuses what the full read would refuse anyway, so the
// sidebar and the transcript agree about which chats exist.
func readChatHeader(path, label string, fileCap chatFileCap) (*vibekit.ChatHeader, error) {
	f, info, err := openChatFile(path, label)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > maxHeaderScanBytes {
		return nil, errFileTooLarge(label, info.Size(), maxHeaderScanBytes)
	}
	if !fileCap.unlimited() && info.Size() > int64(fileCap) {
		return nil, errFileTooLarge(label, info.Size(), int64(fileCap))
	}
	h, err := decodeChatHeader(bufio.NewReader(io.LimitReader(f, maxHeaderScanBytes)))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	return h, nil
}

// decodeChatHeader is the projection itself, over any reader, so the parsing
// contract is testable without a file. Every member except `messages` is captured
// RAW and handed to encoding/json in one object, which keeps chatHeaderOnDisk's
// field mapping automatic where a case per key would make a new ChatHeader field
// an edit here.
func decodeChatHeader(r io.Reader) (*vibekit.ChatHeader, error) {
	head := make(map[string]json.RawMessage)
	count := 0
	dec := jsoncap.NewDecoder(r, 0)
	err := dec.Object(func(key string) error {
		// EqualFold for the reason readRetentionHeader documents: encoding/json
		// matches a field tag case-insensitively and is the OTHER reader of this
		// same file, so a chat carrying "Messages" must not be captured whole.
		if strings.EqualFold(key, keyMessages) {
			n, cerr := countStreamedArrayElements(dec)
			count = n
			return cerr
		}
		var raw json.RawMessage
		if derr := dec.Decode(&raw); derr != nil {
			return derr
		}
		head[key] = raw
		return nil
	})
	if err != nil {
		return nil, err
	}
	reassembled, err := json.Marshal(head)
	if err != nil {
		return nil, err
	}
	var h chatHeaderOnDisk
	if err := json.Unmarshal(reassembled, &h); err != nil {
		return nil, err
	}
	h.MessageCount = count
	return &h.ChatHeader, nil
}

// countStreamedArrayElements consumes one JSON array from dec, counting its
// top-level elements and materializing none of them. A JSON null counts 0; any
// other non-array value is an error, which is stricter than the whole-file read
// this replaced and agrees with readChatFile — a `messages` member that is not
// an array fails json.Unmarshal into vibekit.Chat, so tolerating it here listed
// a chat in the sidebar that could not be opened.
func countStreamedArrayElements(dec *jsoncap.Decoder) (int, error) {
	ok, err := dec.Open('[')
	if err != nil || !ok {
		return 0, err
	}
	count := 0
	for dec.More() {
		if serr := dec.Skip(); serr != nil {
			return count, serr
		}
		count++
	}
	return count, dec.Close()
}
