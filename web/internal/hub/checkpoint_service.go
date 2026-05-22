package hub

import (
	"context"

	"vibekit/internal/api"
	"vibekit/internal/checkpoint"
)

// CheckpointService is a type alias for the interface declared in the
// api package. Kept here so existing hub code continues to compile
// without a mass-rename; new code should reference api.CheckpointService
// directly.
type CheckpointService = api.CheckpointService

// checkpointAdapter wraps *checkpoint.Store (which uses plain string
// chatID parameters) to satisfy api.CheckpointService (which uses
// api.ChatID). The checkpoint package cannot import api (circular dep
// via api→checkpoint for Tag/FileChange types), so this adapter
// performs the trivial string↔ChatID conversion at the boundary.
type checkpointAdapter struct {
	store *checkpoint.Store
}

// Compile-time interface assertion.
var _ CheckpointService = (*checkpointAdapter)(nil)

func (a *checkpointAdapter) Snapshot(ctx context.Context, chatID api.ChatID, relPath string, newContent []byte, messageCount int) (checkpoint.Tag, error) {
	return a.store.Snapshot(ctx, string(chatID), relPath, newContent, messageCount)
}

func (a *checkpointAdapter) Restore(ctx context.Context, chatID api.ChatID, tag checkpoint.Tag) (int, error) {
	return a.store.Restore(ctx, string(chatID), tag)
}

func (a *checkpointAdapter) RestorePreview(ctx context.Context, chatID api.ChatID, tag checkpoint.Tag) ([]string, error) {
	return a.store.RestorePreview(ctx, string(chatID), tag)
}

func (a *checkpointAdapter) CheckoutFile(ctx context.Context, chatID api.ChatID, tag checkpoint.Tag, relPath string) error {
	return a.store.CheckoutFile(ctx, string(chatID), tag, relPath)
}

func (a *checkpointAdapter) Diff(ctx context.Context, chatID api.ChatID, from, to checkpoint.Tag) ([]checkpoint.FileChange, error) {
	return a.store.Diff(ctx, string(chatID), from, to)
}

func (a *checkpointAdapter) Conflicts(ctx context.Context, chatID api.ChatID) ([]checkpoint.ConflictPayload, error) {
	return a.store.Conflicts(ctx, string(chatID))
}

func (a *checkpointAdapter) ReadBlob(ctx context.Context, chatID api.ChatID, sha string) ([]byte, error) {
	return a.store.ReadBlob(ctx, string(chatID), sha)
}

func (a *checkpointAdapter) OldestTag(ctx context.Context, chatID api.ChatID) checkpoint.Tag {
	return a.store.OldestTag(ctx, string(chatID))
}

func (a *checkpointAdapter) AdvanceTurn(ctx context.Context, chatID api.ChatID, messageCount int) {
	a.store.AdvanceTurn(ctx, string(chatID), messageCount)
}

func (a *checkpointAdapter) Cleanup(ctx context.Context, chatID api.ChatID) {
	a.store.Cleanup(ctx, string(chatID))
}

func (a *checkpointAdapter) StartBackgroundTasks(ctx context.Context) {
	a.store.StartBackgroundTasks(ctx)
}

func (a *checkpointAdapter) Stop() {
	a.store.Stop()
}
