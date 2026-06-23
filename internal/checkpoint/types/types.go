// Package types defines checkpoint domain types shared between the
// checkpoint implementation and its consumers (api, hub). This leaf
// package has zero internal dependencies, breaking the api→checkpoint
// import cycle.
package types

import (
	"errors"
	"strings"
)

// Tag is a validated checkpoint tag with grammar "N" or "N.K" where
// N is the turn number and K is the tool index within that turn.
type Tag string

// ParseTag validates and returns a Tag. Returns an error if the input
// doesn't match the "N" or "N.K" grammar.
func ParseTag(s string) (Tag, error) {
	if s == "" {
		return "", errors.New("checkpoint: empty tag")
	}
	turnStr, toolStr, hasDot := strings.Cut(s, ".")
	if !hasDot {
		toolStr = ""
	}
	if !isDigits(turnStr) || (hasDot && !isDigits(toolStr)) {
		return "", errors.New("checkpoint: invalid tag format")
	}
	return Tag(s), nil
}

// String returns the tag's string representation.
func (t Tag) String() string { return string(t) }

// isDigits reports whether s is a non-empty string of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ErrTagNotFound is returned when a tag does not exist in the store.
// Separate from a generic error so the HTTP layer can map it to 404.
var ErrTagNotFound = errors.New("tag not found")

// FileStatus is a typed string for diff result statuses.
type FileStatus string

// FileAdded and the following constants define the valid FileStatus diff result values.
const (
	FileAdded    FileStatus = "A"
	FileModified FileStatus = "M"
	FileDeleted  FileStatus = "D"
)

// Valid reports whether s is one of the three known diff statuses.
func (s FileStatus) Valid() bool {
	switch s {
	case FileAdded, FileModified, FileDeleted:
		return true
	}
	return false
}

// FileChange is one entry returned by Diff.
type FileChange struct {
	Status       FileStatus `json:"status"`
	Path         string     `json:"path"`
	LinesAdded   int        `json:"lines_added"`
	LinesRemoved int        `json:"lines_removed"`
}

// ConflictPayload is what the broadcaster receives per conflict.
type ConflictPayload struct {
	Path        string `json:"path"`
	OtherChat   string `json:"other_chat"`
	ExpectedSHA string `json:"expected_sha"`
	ActualSHA   string `json:"actual_sha"`
	Tag         string `json:"tag"`
	TS          int64  `json:"ts"`
}

// BlobRef is the minimal struct for extracting blob SHA references from
// the event JSONL log. Shared between the checkpoint and gc packages to
// keep the JSON tag contract as a single source of truth.
type BlobRef struct {
	BeforeSHA string `json:"before_sha,omitempty"`
	AfterSHA  string `json:"after_sha,omitempty"`
}
