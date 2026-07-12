// Profile ARN sourcing for the v3 (KAS) host-mediated auth callback.
//
// The v3 agent asks the client for the bearer token AND expects a
// `profileArn` in the _kiro/auth/getAccessToken reply so KAS can route
// requests to the profile's region and (crucially) identify the account
// for _kiro/account/getUsage — without it getUsage returns
// "Invalid profileArn." (basic session/new + turns still work, so this
// is best-effort for the auth path but required for usage).
//
// kiro-cli stores the active profile in its state DB, not the SSO token
// file:
//
//	~/.local/share/kiro-cli/data.sqlite3
//	  state(key TEXT PRIMARY KEY, value BLOB)
//	  key="api.codewhisperer.profile"
//	  value={"arn":"arn:aws:codewhisperer:<region>:<acct>:profile/<id>", ...}
//
// vibekit deliberately carries no sqlite driver (a large dependency for
// one small value). Since the value is a tiny JSON object stored inline
// in the b-tree record (the row payload is [header][key-bytes][value-
// bytes], so the value's `{` immediately follows the key bytes), we read
// it with a targeted byte-scan: locate the key, brace-match the JSON that
// abuts it, parse `arn`, and validate the CodeWhisperer prefix. The DB is
// small (~160 KiB) and the value never overflows a page, so this is
// robust for the single-user container; a format change just yields ""
// and usage degrades gracefully (the auth path and turns are unaffected).

package kiroauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// arnPrefix gates which extracted values are accepted as a profile ARN.
const arnPrefix = "arn:aws:codewhisperer:"

// maxProfileDBSize caps the data.sqlite3 read (32 MiB) so a runaway file
// can't pin memory. kiro-cli's state DB is a few hundred KiB in practice.
const maxProfileDBSize = 32 << 20

// maxProfileJSON caps the brace-matched value object (4 KiB) — the
// profile record is well under 200 bytes.
const maxProfileJSON = 4 << 10

// profileKey is the state-table key whose value carries the profile ARN.
var profileKey = []byte("api.codewhisperer.profile")

// ProfileReader reads the active CodeWhisperer profile ARN from kiro-cli's
// state DB, caching by modtime and serialising reads through a mutex.
type ProfileReader struct {
	cachedMtime time.Time
	arn         string
	path        string
	mu          sync.Mutex
	loaded      bool
}

// DefaultProfileDBPath returns kiro-cli's state DB path, honouring
// XDG_DATA_HOME (falling back to $HOME/.local/share). Empty if neither
// XDG_DATA_HOME nor HOME resolves.
func DefaultProfileDBPath() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "kiro-cli", "data.sqlite3")
}

// NewProfileReader returns a ProfileReader for the given DB path (empty
// uses DefaultProfileDBPath).
func NewProfileReader(path string) *ProfileReader {
	if path == "" {
		path = DefaultProfileDBPath()
	}
	return &ProfileReader{path: path}
}

// Arn returns the active CodeWhisperer profile ARN, or an error when the
// DB is missing/unreadable or carries no recognisable profile entry.
// Cached by modtime so repeated getAccessToken callbacks don't re-scan.
func (r *ProfileReader) Arn() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.path == "" {
		return "", errors.New("kiro profile: data dir not resolvable")
	}
	fi, err := os.Stat(r.path)
	if err != nil {
		return "", fmt.Errorf("kiro profile db stat: %w", err)
	}
	if r.loaded && fi.ModTime().Equal(r.cachedMtime) {
		return r.arn, nil
	}
	if fi.Size() > maxProfileDBSize {
		return "", fmt.Errorf("kiro profile db too large: %d bytes", fi.Size())
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return "", fmt.Errorf("kiro profile db read: %w", err)
	}
	arn, err := scanProfileArn(data)
	if err != nil {
		return "", err
	}
	r.arn = arn
	r.cachedMtime = fi.ModTime()
	r.loaded = true
	return arn, nil
}

// scanProfileArn locates the profile-state key in the raw DB bytes and
// extracts the ARN from the JSON object that immediately follows it.
// Multiple stale copies may exist (older b-tree pages); the first copy
// whose abutting JSON carries a valid CodeWhisperer ARN wins. For the
// single-user container these copies agree; a mismatch after a profile
// switch self-heals on the next DB write.
func scanProfileArn(data []byte) (string, error) {
	from := 0
	for {
		i := bytes.Index(data[from:], profileKey)
		if i < 0 {
			break
		}
		valStart := from + i + len(profileKey)
		from = from + i + 1 // advance past this match for the next iteration
		// The record payload stores value bytes immediately after key
		// bytes, so the value object's '{' abuts the key. Occurrences
		// without an abutting object (e.g. an index entry) are skipped.
		if valStart >= len(data) || data[valStart] != '{' {
			continue
		}
		obj, ok := extractJSONObject(data[valStart:])
		if !ok {
			continue
		}
		var p struct {
			Arn string `json:"arn"`
		}
		if json.Unmarshal(obj, &p) != nil {
			continue
		}
		if strings.HasPrefix(p.Arn, arnPrefix) {
			return p.Arn, nil
		}
	}
	return "", errors.New("kiro profile: api.codewhisperer.profile not found in data.sqlite3")
}

// extractJSONObject returns the balanced-brace JSON object starting at
// b[0] (which must be '{'), string-aware so a '}' inside a quoted value
// doesn't close the object early. Bounded by maxProfileJSON.
func extractJSONObject(b []byte) ([]byte, bool) {
	if len(b) == 0 || b[0] != '{' {
		return nil, false
	}
	depth := 0
	inStr := false
	esc := false
	limit := min(len(b), maxProfileJSON)
	for i := range limit {
		c := b[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return b[:i+1], true
			}
		}
	}
	return nil, false
}
