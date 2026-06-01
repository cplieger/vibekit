package gc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"vibekit/internal/checkpoint/types"
)

// maxEventLogBytes caps the event log size the GC will read (100 MiB).
// Logs exceeding this are treated as corrupted to prevent OOM.
const maxEventLogBytes = 100 * 1024 * 1024

// readEventLog reads a JSONL event log and returns the events.
func readEventLog(path string) ([]types.BlobRef, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxEventLogBytes {
		return nil, fmt.Errorf("event log %s exceeds size cap (%d > %d)", path, info.Size(), maxEventLogBytes)
	}

	var events []types.BlobRef
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		var ev types.BlobRef
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	return events, sc.Err()
}

// streamEventSHAs reads the event log line-by-line and extracts blob
// SHAs without allocating the full []types.BlobRef slice. Memory is bounded
// to O(unique SHAs) rather than O(total events).
func streamEventSHAs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxEventLogBytes {
		return nil, fmt.Errorf("event log %s exceeds size cap (%d > %d)", path, info.Size(), maxEventLogBytes)
	}

	var shas []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		var ev types.BlobRef
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.BeforeSHA != "" {
			shas = append(shas, ev.BeforeSHA)
		}
		if ev.AfterSHA != "" {
			shas = append(shas, ev.AfterSHA)
		}
	}
	return shas, sc.Err()
}
