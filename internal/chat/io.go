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

// openChatFile opens path for reading, with the FileInfo the open produced. OpenRegular and NOT
// os.Open: os.Open on a FIFO blocks in open(2) with no deadline able to rescue it (go1.27.0),
// and this directory is writable by the agent's own shell, so one mkfifo wedges every reader.
func openChatFile(path, label string) (*os.File, os.FileInfo, error) {
	// For CodeQL's go/path-injection analyzer, which does not follow ValidChatID across packages.
	// KNOWN VACUITY: it runs on the CLEANED value, so the traversal test cannot fire.
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || pathinside.HasDotDot(clean) {
		return nil, nil, fmt.Errorf("%s: rejected unsafe path %q", label, path)
	}
	return atomicfile.OpenRegular(clean)
}

// readCappedFile reads a whole file at path under fileCap, plus the TOCTOU grow-during-read
// guard. Whole-file is correct HERE and only here: readChatFile runs one chat at a time under
// that chat's own lock, while the header path carries an 8x multiplier and streams instead.
func readCappedFile(path, label string, fileCap chatFileCap) ([]byte, error) {
	f, info, err := openChatFile(path, label)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// ReadBoundedFile is the size cap AND the grow-during-read guard: it stats the descriptor,
	// refuses over the bound, and refuses again if the file grew past it while being read.
	// context.Background() because no read path here carries one.
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

// chatHeaderOnDisk is the header projection's decode target. Embedding ChatHeader means a new
// header field flows through with no mapping step; MessageCount is counted separately.
type chatHeaderOnDisk struct {
	vibekit.ChatHeader
}

// readChatHeader STREAMS a chat file and returns only its header fields, because
// readHeadersParallel runs this at 8 workers per chat. Two independent gates: maxHeaderScanBytes
// bounds the scan even when fileCap is unlimited, and fileCap refuses what the full read would
// refuse anyway, so the sidebar and the transcript agree about which chats exist.
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

// decodeChatHeader is the projection itself, over any reader, so the parsing contract is testable
// without a file. Every member except `messages` is captured RAW and handed to encoding/json in
// one object, which keeps chatHeaderOnDisk's field mapping automatic.
func decodeChatHeader(r io.Reader) (*vibekit.ChatHeader, error) {
	head := make(map[string]json.RawMessage)
	count := 0
	dec := jsoncap.NewDecoder(r, 0)
	err := dec.Object(func(key string) error {
		// EqualFold because encoding/json matches a field tag case-insensitively and is the OTHER
		// reader of this same file, so a chat carrying "Messages" must not be captured whole.
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

// countStreamedArrayElements consumes one JSON array from dec, counting its top-level elements
// and materializing none of them. A JSON null counts 0; any other non-array value is an error,
// which agrees with readChatFile — a `messages` member that is not an array fails Unmarshal into
// vibekit.Chat, so tolerating it here listed a chat in the sidebar that could not be opened.
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
