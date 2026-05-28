package gc

import (
	"bufio"
	"encoding/json"
	"os"
)

// gcEvent is the minimal event struct needed for blob reference
// collection. Only BeforeSHA and AfterSHA are used by the GC.
type gcEvent struct {
	BeforeSHA string `json:"before_sha,omitempty"`
	AfterSHA  string `json:"after_sha,omitempty"`
}

// readEventLog reads a JSONL event log and returns the events.
func readEventLog(path string) ([]gcEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []gcEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		var ev gcEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	return events, sc.Err()
}
