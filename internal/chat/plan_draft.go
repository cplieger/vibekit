package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
)

// maxPlanDraftBytes caps a plan draft so a runaway client can't fill the
// config directory. Plans are markdown; 256 KB is generous for any
// reasonable plan.
const maxPlanDraftBytes = 256 * 1024

// GetPlanDraft returns the plan-draft markdown for chatID, or "" with no
// error if no draft exists. Files larger than maxPlanDraftBytes are
// rejected before load so an out-of-band writer (anything with access
// to the config volume) cannot OOM the process by planting a giant
// file at the draft path.
func (s *Store) GetPlanDraft(ctx context.Context, chatID api.ChatID) (string, error) {
	path, err := s.planDraftPathFor(chatID)
	if err != nil {
		return "", err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	f, err := os.Open(path) //nolint:gosec // G304,G703: path built from validated chat ID
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.Size() > maxPlanDraftBytes {
		return "", fmt.Errorf("plan draft %s too large: %d bytes (max %d)",
			chatID, st.Size(), maxPlanDraftBytes)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPlanDraftBytes+1))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// SetPlanDraft writes the draft markdown for chatID atomically. Caller
// must have sanitised size upstream; this function enforces the byte cap
// and returns an error if exceeded.
//
// Refuses to write and returns *StoreError{Kind: ErrKindNotFound} if
// the chat file does not exist (orphan draft pollution), or
// {Kind: ErrKindTombstoned} if the chat was recently deleted
// (delete-during-edit race).
//
// The check + write are serialised under the per-chat mutex, same
// ordering rule as Mutate, so a concurrent Delete can't open the door.
func (s *Store) SetPlanDraft(ctx context.Context, chatID api.ChatID, content string) error {
	if len(content) > maxPlanDraftBytes {
		return &StoreError{Kind: ErrKindTooLarge, Detail: fmt.Sprintf("%d bytes (max %d)", len(content), maxPlanDraftBytes)}
	}
	// Validate once; both sibling paths derive from the same id with a
	// fixed suffix so there is no second validation to perform.
	if !chatIDPattern(chatID) {
		return errInvalidChatID(chatID)
	}
	chatPath := filepath.Join(s.dir, string(chatID)+chatFileSuffix)
	draftPath := filepath.Join(s.dir, string(chatID)+planDraftSuffix)
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	if s.isTombstoned(chatID) {
		return &StoreError{Kind: ErrKindTombstoned, Detail: string(chatID)}
	}
	if _, statErr := os.Stat(chatPath); errors.Is(statErr, os.ErrNotExist) { //nolint:gosec // G703: path within workspace
		return &StoreError{Kind: ErrKindNotFound, Detail: string(chatID)}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The len(content) pre-check above stays: it produces the typed
	// *StoreError{ErrKindTooLarge} the HTTP layer maps to a specific 413.
	// WithMaxBytes enforces the same bound inside the write itself, so no
	// future caller can bypass the cap.
	_, err := atomicfile.WriteFile(ctx, draftPath, []byte(content),
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(maxPlanDraftBytes))
	return err
}

// DeletePlanDraft removes the draft file for chatID. No-op if missing.
func (s *Store) DeletePlanDraft(_ context.Context, chatID api.ChatID) error {
	path, err := s.planDraftPathFor(chatID)
	if err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // G703: path within workspace
		return err
	}
	slog.Debug("chat plan_draft delete", "chat_id", chatID)
	return nil
}

// planDraftPathFor returns the on-disk path for a chat's plan-draft file.
func (s *Store) planDraftPathFor(chatID api.ChatID) (string, error) {
	if !chatIDPattern(chatID) {
		return "", errInvalidChatID(chatID)
	}
	return filepath.Join(s.dir, string(chatID)+planDraftSuffix), nil
}
