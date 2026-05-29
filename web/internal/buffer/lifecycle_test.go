package buffer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverPartials_EmptyConfigDir(t *testing.T) {
	l := &Lifecycle{ConfigDir: ""}
	if got := l.RecoverPartials(); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestRecoverPartials_NoChatsDir(t *testing.T) {
	l := &Lifecycle{ConfigDir: t.TempDir()}
	if got := l.RecoverPartials(); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestRecoverPartials_ValidPartial(t *testing.T) {
	dir := t.TempDir()
	chatsDir := filepath.Join(dir, "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"content":"hello","reasoning":""}`
	if err := os.WriteFile(filepath.Join(chatsDir, "chat-1.partial"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	l := &Lifecycle{ConfigDir: dir}
	got := l.RecoverPartials()
	if len(got) != 1 {
		t.Fatalf("expected 1 recovered, got %d", len(got))
	}
	if string(got[0].ChatID) != "chat-1" {
		t.Errorf("ChatID = %q, want chat-1", got[0].ChatID)
	}
}

func TestRecoverPartials_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	chatsDir := filepath.Join(dir, "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chatsDir, "chat-2.partial")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	l := &Lifecycle{ConfigDir: dir}
	got := l.RecoverPartials()
	if len(got) != 0 {
		t.Errorf("expected 0 recovered, got %d", len(got))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("empty partial file was not removed")
	}
}

func TestRecoverPartials_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	chatsDir := filepath.Join(dir, "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chatsDir, "chat-3.partial")
	if err := os.WriteFile(path, []byte("{invalid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := &Lifecycle{ConfigDir: dir}
	got := l.RecoverPartials()
	if len(got) != 0 {
		t.Errorf("expected 0 recovered, got %d", len(got))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("corrupt partial file was not removed")
	}
}

func TestRecoverPartials_DirectoryEntry(t *testing.T) {
	dir := t.TempDir()
	chatsDir := filepath.Join(dir, "chats")
	if err := os.MkdirAll(filepath.Join(chatsDir, "chat-4.partial"), 0o755); err != nil {
		t.Fatal(err)
	}

	l := &Lifecycle{ConfigDir: dir}
	got := l.RecoverPartials()
	if len(got) != 0 {
		t.Errorf("expected 0 recovered for directory entry, got %d", len(got))
	}
}

func TestRecoverPartials_MultiplePartials(t *testing.T) {
	dir := t.TempDir()
	chatsDir := filepath.Join(dir, "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"content":"text","reasoning":""}`
	for _, id := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(chatsDir, id+".partial"), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	l := &Lifecycle{ConfigDir: dir}
	got := l.RecoverPartials()
	if len(got) != 3 {
		t.Errorf("expected 3 recovered, got %d", len(got))
	}
}

func TestRecoverPartials_NonPartialFiles(t *testing.T) {
	dir := t.TempDir()
	chatsDir := filepath.Join(dir, "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chatsDir, "chat.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := &Lifecycle{ConfigDir: dir}
	got := l.RecoverPartials()
	if len(got) != 0 {
		t.Errorf("expected 0 recovered, got %d", len(got))
	}
}
