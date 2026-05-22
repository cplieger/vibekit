package chat

import (
	"errors"
	"fmt"
	"net/http"

	"vibekit/internal/api"
)

// ErrorKind discriminates the class of chat store error. Using a
// typed enum instead of independent sentinels lets handlers dispatch
// via a single errors.As + switch on Kind.
type ErrorKind int

const (
	// ErrKindNotFound means the target chat has no file on disk.
	ErrKindNotFound ErrorKind = iota + 1
	// ErrKindTombstoned means the target chat was recently deleted.
	ErrKindTombstoned
	// ErrKindTooLarge means the plan draft exceeded maxPlanDraftBytes.
	ErrKindTooLarge
	// ErrKindIDInUse means the target chat ID is already used by an
	// active (non-archived) chat.
	ErrKindIDInUse
)

// StoreError is a typed error carrying a Kind discriminator and
// optional detail. Handlers can dispatch via errors.As + switch on Kind
// instead of N independent errors.Is chains.
type StoreError struct {
	Detail string
	Kind   ErrorKind
}

func (e *StoreError) Error() string {
	switch e.Kind {
	case ErrKindNotFound:
		if e.Detail != "" {
			return "chat not found: " + e.Detail
		}
		return "chat not found"
	case ErrKindTombstoned:
		if e.Detail != "" {
			return "chat recently deleted: " + e.Detail
		}
		return "chat recently deleted"
	case ErrKindTooLarge:
		if e.Detail != "" {
			return "plan draft too large: " + e.Detail
		}
		return "plan draft too large"
	case ErrKindIDInUse:
		if e.Detail != "" {
			return "chat id in use: " + e.Detail
		}
		return "chat id in use"
	default:
		return "chat store error"
	}
}

// Is supports errors.Is matching between *StoreError values.
// Two store errors are considered equal when their Kinds match;
// Detail is ignored. This lets tests assert on Kind without
// constructing an identical-Detail comparator.
func (e *StoreError) Is(target error) bool {
	if other, ok := target.(*StoreError); ok {
		return e.Kind == other.Kind
	}
	return false
}

// errInvalidChatID returns the canonical error for a malformed chat ID.
// Single source of truth for the error message format.
func errInvalidChatID(id api.ChatID) error {
	return fmt.Errorf("invalid chat id: %q", id)
}

// writeChatErr maps a StoreError to the appropriate HTTP response.
func writeChatErr(w http.ResponseWriter, err error) {
	var ce *StoreError
	if errors.As(err, &ce) {
		switch ce.Kind {
		case ErrKindNotFound, ErrKindTombstoned:
			api.NotFound(w, "chat not found")
		case ErrKindTooLarge:
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				map[string]string{"error": ce.Error()})
		case ErrKindIDInUse:
			api.Conflict(w, ce.Error())
		default:
			api.InternalError(w, err)
		}
		return
	}
	api.InternalError(w, err)
}
