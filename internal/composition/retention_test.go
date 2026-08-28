package composition

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/settings"
)

// TestChatRetention_ThreeLegs walks one config.json through the three states the
// retention read has to tell apart. All three legs are required, and each one
// answers a different question:
//
//   - a stored -1 reads as no purge (the control: it proves the other two are
//     about the READ channel and not about the value handling);
//   - a config.json that is THERE and unparseable ALSO reads as no purge, while
//     the default window is a positive number — this is the leg the fix exists
//     for, and it is the one a folded read fails;
//   - a config.json that is ABSENT reads as the default window, which is the leg
//     that catches a split that fails CLOSED and leaves a fresh install with no
//     retention at all.
//
// Legs 1 and 2 together are the whole point: without leg 1, leg 2 passes for any
// implementation that never purges; without leg 2, leg 1 passes for the folding
// read that deletes the user's chats.
func TestChatRetention_ThreeLegs(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, settings.Filename)

	if settings.DefaultChatRetentionDays <= 0 {
		t.Fatalf("DefaultChatRetentionDays = %d; a non-positive default makes leg 2 vacuous",
			settings.DefaultChatRetentionDays)
	}
	wantDefault := time.Duration(settings.DefaultChatRetentionDays) * 24 * time.Hour

	// Leg 1: the Keep-forever checkbox as it is persisted.
	writeConfig(t, path, `{"chat_retention_days":-1}`)
	if got := chatRetention(ctx, dir); got != 0 {
		t.Fatalf("chatRetention with a stored -1 = %v, want 0 (never purge)", got)
	}

	// Leg 2: the same directory, file present and unreadable.
	writeConfig(t, path, `{`)
	if got := chatRetention(ctx, dir); got != 0 {
		t.Errorf("chatRetention with an unparseable config.json = %v, want 0; applying the default (%v) here purges the chats the user asked to keep",
			got, wantDefault)
	}

	// Leg 3: no file at all, which is a fresh install and must still get a window.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	if got := chatRetention(ctx, dir); got != wantDefault {
		t.Errorf("chatRetention with no config.json = %v, want %v (absence is not a failure)", got, wantDefault)
	}
}

// TestChatRetention_StoredWindow pins the ordinary path, so the refusal above
// cannot be satisfied by a function that never purges anything.
func TestChatRetention_StoredWindow(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, settings.Filename), `{"chat_retention_days":7}`)

	if got, want := chatRetention(t.Context(), dir), 7*24*time.Hour; got != want {
		t.Errorf("chatRetention with a stored 7 = %v, want %v", got, want)
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
