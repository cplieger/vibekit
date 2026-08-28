package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/slogx/capture"
)

// TestFieldStrict_SeparatesAbsenceFromUnreadable walks one config.json through
// the three states a retention read has to tell apart, in order, against the
// same directory — so the cache is exercised the way a running process would
// exercise it rather than from a fresh cache per case.
//
// All three legs are required. Under Field's two-value signature the middle leg
// is indistinguishable from the last, and a caller substituting a default for
// both silently overrides a stored -1 ("Keep forever") with the 1-day window and
// purges the chats the user asked to keep.
//
// How each leg fails if FieldStrict is reverted to Field's folding: leg 1 is the
// control and passes either way (it is what proves the other two are about the
// channel and not about the read being broken); leg 2 goes red, because a folded
// read answers absence for a file that is present and unparseable; leg 3 goes red
// if the split ever fails CLOSED instead, reporting an error for a fresh install
// that has no config.json at all.
func TestFieldStrict_SeparatesAbsenceFromUnreadable(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	resetCache(t, dir)
	path := filepath.Join(dir, Filename)

	// Leg 1: a stored value the caller must see as PRESENT. -1 is the value the
	// Keep-forever checkbox persists, and the one a default would destroy.
	writeSettings(t, dir, []byte(`{"chat_retention_days":-1}`))
	days, ok, err := FieldStrict[int](ctx, dir, KeyChatRetentionDays)
	if err != nil {
		t.Fatalf("FieldStrict(%s) on a stored value: err = %v, want nil", KeyChatRetentionDays, err)
	}
	if !ok || days != -1 {
		t.Fatalf("FieldStrict(%s) on a stored value = (%d, %v), want (-1, true)", KeyChatRetentionDays, days, ok)
	}

	// Leg 2: the file is THERE and unparseable. Absence would be a lie.
	writeSettings(t, dir, []byte(`{`))
	days, ok, err = FieldStrict[int](ctx, dir, KeyChatRetentionDays)
	if err == nil {
		t.Errorf("FieldStrict(%s) on an unparseable config.json = (%d, %v, nil), want an error", KeyChatRetentionDays, days, ok)
	}
	if ok || days != 0 {
		t.Errorf("FieldStrict(%s) on an unparseable config.json = (%d, %v), want (0, false)", KeyChatRetentionDays, days, ok)
	}

	// Leg 3: no file at all. A fresh volume is a legitimate absent case and must
	// stay one, or every new install refuses to purge anything forever.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	days, ok, err = FieldStrict[int](ctx, dir, KeyChatRetentionDays)
	if err != nil {
		t.Errorf("FieldStrict(%s) with no config.json: err = %v, want nil (absence is not a failure)", KeyChatRetentionDays, err)
	}
	if ok || days != 0 {
		t.Errorf("FieldStrict(%s) with no config.json = (%d, %v), want (0, false)", KeyChatRetentionDays, days, ok)
	}
}

// TestFieldStrict_AbsentKeyInAReadableFileIsNotAnError separates the two absent
// rows from each other: leg 3 above removes the file, this one keeps a perfectly
// good file that simply predates the key. Both must answer absence with a nil
// error, and a split that keyed on "the file parsed" rather than on "the key was
// there" would pass the test above and fail here.
func TestFieldStrict_AbsentKeyInAReadableFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	resetCache(t, dir)
	writeSettings(t, dir, []byte(`{"theme":"dark"}`))

	days, ok, err := FieldStrict[int](t.Context(), dir, KeyChatRetentionDays)
	if err != nil || ok || days != 0 {
		t.Errorf("FieldStrict(%s) on a file without the key = (%d, %v, %v), want (0, false, nil)",
			KeyChatRetentionDays, days, ok, err)
	}
}

// TestField_KeepsBothWarnLines pins the diagnostic trail internal/push documents
// itself as relying on: config.json silently reverting a user's toggle leaves one
// Warn naming the key, and that is the whole signal an operator gets. Field is a
// wrapper over FieldStrict now, so the logging lives at exactly one place and a
// refactor that drops it is invisible to every other test in this package —
// each of them asserts (zero, false), which the silent version also returns.
//
// The two messages stay distinct because they name different faults: a document
// that would not parse at all, versus one key whose value is the wrong type.
//
// Serial by necessity: slog's default logger is process-global, so a parallel
// sibling's lines would land in this recorder.
func TestField_KeepsBothWarnLines(t *testing.T) {
	tests := []struct {
		desc    string
		content string
		wantMsg string
	}{
		{
			desc:    "a document that does not parse",
			content: `{`,
			wantMsg: "settings: read config.json for " + KeyChatRetentionDays,
		},
		{
			desc:    "a key whose value is the wrong type",
			content: `{"chat_retention_days":"forever"}`,
			wantMsg: "settings: parse " + KeyChatRetentionDays,
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			dir := t.TempDir()
			resetCache(t, dir)
			writeSettings(t, dir, []byte(tc.content))

			rec := capture.Default(t)
			if _, ok := Field[int](t.Context(), dir, KeyChatRetentionDays); ok {
				t.Fatalf("Field(%s) on %s returned ok=true", KeyChatRetentionDays, tc.desc)
			}
			if n := rec.CountExact(tc.wantMsg); n != 1 {
				t.Errorf("Field(%s) on %s logged %q %d times, want 1; messages: %v",
					KeyChatRetentionDays, tc.desc, tc.wantMsg, n, rec.Messages())
			}
		})
	}
}
