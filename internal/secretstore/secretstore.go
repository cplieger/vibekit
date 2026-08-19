// Package secretstore persists the opaque credential blobs KAS asks vibekit
// to hold on its behalf via the v3 `_kiro/secret/*` requests.
//
// # Why this exists
//
// KAS owns the whole MCP OAuth flow — discovery, Dynamic Client Registration,
// PKCE, token exchange and refresh all happen inside the agent process. What it
// does NOT own is persistence: `CredentialStorageManager` keeps an in-process
// memory copy and consults the client store only on a miss, and there is no
// KAS-side file. So "do MCP credentials survive a bridge restart?" is answered
// entirely by whoever implements these three handlers.
//
// Without them (vibekit before 2026-08, which did not declare the capability)
// every bridge spawn re-ran discovery and a fresh `POST /register` — measured:
// 0 secret calls and one DCR per process. Replaying a stored blob in a new
// process dropped that to zero DCRs, with KAS reusing the client_id it was
// handed. That churn is the defect this package fixes.
//
// It does NOT make redirect-based MCP OAuth work end to end. KAS binds its own
// loopback redirect listener and advertises a container-local
// `http://localhost:<ephemeral>/oauth/callback`, while vibekit's browser is
// remote. This stops the re-registration, nothing more.
//
// # Storage model
//
// One file at <configDir>/mcp-secrets.json, mode 0600, written atomically via
// cplieger/atomicfile. A flat `{"secrets": {"<key>": "<base64 value>"}}` map.
//
// Values are base64 so the store's byte-exactness is unconditional. A value
// placed straight into a JSON string does NOT round-trip: encoding/json
// replaces invalid UTF-8 with U+FFFD, so any non-UTF-8 byte comes back
// corrupted. In production that is unreachable (KAS's values are its own
// JSON.stringify output arriving over a JSON wire, so they are valid UTF-8 by
// construction) — but the package promises to return the exact bytes it was
// given, and a silent corruption in a credential path would surface as an
// unexplained OAuth failure with nothing pointing here. A fuzz target pins it.
// Keys stay plaintext: they are what an operator greps to answer "is this
// server's credential cached?". This is an encoding, NOT encryption; the file's
// protection is its 0600 mode.
//
// Keys and values are OPAQUE. KAS derives keys as
// `kiro.mcp.<sha256(lowercased-trimmed-url + "|" + sorted "k:v" headers)>.<kind>`
// with kind ∈ {client, tokens, verifier}, and values are JSON blobs it
// serialized itself (a DCR result, a token set, a PKCE verifier). vibekit
// parses neither. Deriving the key here, or validating the blob shape, would
// couple this file to a contract KAS is free to change and buy nothing — the
// store's whole job is to hand back the exact bytes it was given.
//
// A keyed map in ONE file, rather than a file per key, is deliberate: a key is
// attacker-adjacent input (it reaches us over the wire), and a map lookup
// cannot be made to traverse a path the way filepath.Join(dir, key) can.
//
// # Secrets
//
// The values are OAuth client secrets, access and refresh tokens, and PKCE
// verifiers. They are never logged — every log line in this package and its
// callers carries the KEY only, which is a sha256 of a URL plus a kind suffix
// and so is safe. The file is 0600 in the same directory as the chat files,
// which already hold conversation content; the threat model is unchanged (the
// user's own container).
package secretstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/filemode"
)

// Bounds. The measured blobs are 90–211 bytes, so both limits are far above
// anything KAS produces; they exist so a buggy or hostile flow cannot grow the
// file without limit. A rejected store surfaces to KAS as an RPC error, which
// it rethrows — the connect attempt fails loudly instead of silently filling
// the disk.
const (
	// MaxValueBytes caps one credential blob.
	MaxValueBytes = 64 << 10
	// MaxKeyBytes caps a key. KAS's own keys are ~80 bytes.
	MaxKeyBytes = 512
	// MaxEntries caps how many distinct keys the store holds. Three keys per
	// MCP server, so this is a ceiling of ~340 servers.
	MaxEntries = 1024
	// maxFileBytes bounds the whole file on both read and write.
	maxFileBytes = 8 << 20

	fileMode = 0o600
	dirMode  = 0o700
	fileName = "mcp-secrets.json"
)

// ErrTooLarge is returned when a key or value exceeds its bound, or when the
// store is full.
var ErrTooLarge = errors.New("secretstore: value rejected (over limit)")

// file is the on-disk shape: key → base64(value). A named field rather than a
// bare map so a future addition does not require a format migration.
type file struct {
	Secrets map[string]string `json:"secrets"`
}

// Store is a process-global keyed credential store.
//
// One Store serves EVERY bridge. KAS's key namespace is global (the key is
// derived from the MCP server's URL and headers, with nothing session- or
// workspace-scoped in it), so sharing one store across bridges is what lets a
// second chat reuse the first chat's registration instead of re-running DCR.
type Store struct {
	// Field order is govet fieldalignment's: the map (a pointer) first, then
	// the string header, then the mutex.
	secrets map[string]string
	path    string
	mu      sync.RWMutex
}

// New opens (or creates) the store under configDir. A missing file is not an
// error — it is the first-run state. An unparseable file is moved aside and
// treated as empty: these are re-derivable credentials, so refusing to boot
// over them would be worse than re-running one DCR.
func New(configDir string) (*Store, error) {
	s := &Store{
		path:    filepath.Join(configDir, fileName),
		secrets: map[string]string{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", fileName, err)
	}
	if len(data) > maxFileBytes {
		return fmt.Errorf("read %s: %d bytes exceeds the %d-byte bound", fileName, len(data), maxFileBytes)
	}
	// The values here are OAuth client secrets, refresh and access tokens and PKCE
	// verifiers, and the package doc is explicit that the base64 is an encoding,
	// NOT encryption: the file's 0600 is its whole protection. os.Chmod only ASKS
	// for that mode, so on a filesystem that stores 0660 for the request the old
	// code tightened nothing and reported nothing. EnforceFile re-stats the
	// descriptor it chmod'ed, and refuses a symlink at the name rather than
	// tightening whatever the name points at this instant.
	//
	// POSTURE CHANGED (was warn-and-continue): an unverifiable mode FAILS the
	// load. A group-readable token file is the exact exposure the 0600 exists to
	// prevent, so continuing would mean writing every credential KAS hands us
	// into a file we know we cannot protect. Failing here does not brick boot —
	// hub treats a secretstore that will not open as best-effort, logs one ERROR
	// and runs with h.secrets nil, which degrades MCP OAuth to the per-spawn DCR
	// it did before this package existed. That is vibekit invariant 6's shape:
	// remove the exposure, do not abort startup over persistent-volume state.
	//
	// What this does NOT do is delete the file. On a filesystem that widens every
	// mode, removing it would destroy re-derivable credentials on every single
	// boot with no path to recovery, trading a reported confidentiality problem
	// for repeated silent data loss. The exposure of a file that was already wide
	// when we found it is not ours to undo; growing it is.
	if _, chErr := filemode.EnforceFile(s.path, fileMode); chErr != nil {
		return fmt.Errorf("refusing to use %s: its mode could not be verified as %#o, so the credentials in it may be readable by other users on this host: %w",
			fileName, fileMode, chErr)
	}
	var f file
	if uErr := json.Unmarshal(data, &f); uErr != nil {
		s.moveCorruptAside(uErr)
		return nil
	}
	for k, encoded := range f.Secrets {
		raw, dErr := base64.StdEncoding.DecodeString(encoded)
		if dErr != nil {
			// Drop the one unreadable entry rather than the whole store: the
			// others are still good, and KAS re-derives whatever is missing.
			slog.Warn("secretstore: undecodable entry dropped; it will be re-derived",
				"key", k, "error", dErr)
			continue
		}
		s.secrets[k] = string(raw)
	}
	return nil
}

// moveCorruptAside renames an unparseable store out of the way so the next
// write starts clean. Best-effort: a failed rename is logged and the caller
// proceeds with an empty map, which the next persist overwrites anyway.
//
// The quarantine name carries a UTC timestamp and the PID, matching
// internal/mcp's store, because a FIXED name makes the second corruption
// destroy the first forensic copy — and the first is the evidence worth
// keeping, since this file holds OAuth client secrets, refresh tokens and PKCE
// verifiers. Two boots in the same second are what the PID separates.
func (s *Store) moveCorruptAside(cause error) {
	corrupt := fmt.Sprintf("%s.corrupt.%s.%d",
		s.path,
		time.Now().UTC().Format("20060102-150405"),
		os.Getpid())
	if rErr := os.Rename(s.path, corrupt); rErr != nil {
		slog.Error("secretstore: preserve corrupt store failed",
			"path", s.path, "error", rErr, "parse_error", cause)
		return
	}
	slog.Warn("secretstore: store unparseable, moved aside; MCP credentials will be re-derived",
		"from", s.path, "to", corrupt, "parse_error", cause)
}

// Get returns the value for key and whether it was present.
//
// A miss is not an error: KAS treats an absent credential as "not registered
// yet" and runs the flow that produces one.
func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.secrets[key]
	return v, ok
}

// Set stores value under key and persists the whole store.
//
// Persist errors are RETURNED, not swallowed. KAS rethrows a client-side
// `_kiro/secret/store` failure, so a silent write failure would present as a
// credential that reads back empty on the next spawn — the exact churn this
// package exists to stop, but now invisible.
func (s *Store) Set(ctx context.Context, key, value string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if len(value) > MaxValueBytes {
		return fmt.Errorf("%w: value is %d bytes, limit %d", ErrTooLarge, len(value), MaxValueBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.secrets[key]; !exists && len(s.secrets) >= MaxEntries {
		return fmt.Errorf("%w: store holds %d entries, limit %d", ErrTooLarge, len(s.secrets), MaxEntries)
	}
	prev, had := s.secrets[key]
	s.secrets[key] = value
	if err := s.persistLocked(ctx); err != nil {
		// Roll the in-memory map back so it never claims a durability the
		// disk does not have.
		if had {
			s.secrets[key] = prev
		} else {
			delete(s.secrets, key)
		}
		return err
	}
	return nil
}

// Delete removes key. Deleting an absent key is a no-op and not an error (the
// post-state the caller asked for already holds), and it does not write.
func (s *Store) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, had := s.secrets[key]
	if !had {
		return nil
	}
	delete(s.secrets, key)
	if err := s.persistLocked(ctx); err != nil {
		s.secrets[key] = prev
		return err
	}
	return nil
}

// persistLocked writes the whole store atomically. Caller holds s.mu.
func (s *Store) persistLocked(ctx context.Context) error {
	encoded := make(map[string]string, len(s.secrets))
	for k, v := range s.secrets {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	data, err := json.MarshalIndent(&file{Secrets: encoded}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", fileName, err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(maxFileBytes)); err != nil {
		return fmt.Errorf("write %s: %w", fileName, err)
	}
	return nil
}

// validateKey rejects an empty, over-long, or non-UTF-8 key.
//
// The key is opaque otherwise — it never becomes a path component, so no
// character needs filtering. UTF-8 is the one exception, and it is a
// CORRECTNESS bound rather than a taste one: the key is stored in a JSON object
// key position, where encoding/json replaces invalid UTF-8 with U+FFFD, so an
// accepted non-UTF-8 key would be written under a different name than it was
// stored under and would never be found again. Rejecting it says so; base64ing
// the key instead would hide it, at the cost of the plaintext key an operator
// greps for. Unreachable in production (KAS's keys are ASCII, and they arrive
// over a JSON wire), and found by FuzzKeysAndValues rather than by reasoning.
func validateKey(key string) error {
	if key == "" {
		return errors.New("secretstore: empty key")
	}
	if len(key) > MaxKeyBytes {
		return fmt.Errorf("%w: key is %d bytes, limit %d", ErrTooLarge, len(key), MaxKeyBytes)
	}
	if !utf8.ValidString(key) {
		return errors.New("secretstore: key is not valid UTF-8")
	}
	return nil
}
