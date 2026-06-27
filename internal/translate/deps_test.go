package translate

import "testing"

// TestNew_InstallsDefaultIDWhenUnset pins that constructing a Translator
// without WithIDGenerator installs the default message-ID generator,
// which produces non-empty IDs.
func TestNew_InstallsDefaultIDWhenUnset(t *testing.T) {
	tr := New(newBaseDeps(), "/tmp")
	if tr.newMsgID == nil {
		t.Fatal("New (no WithIDGenerator): newMsgID is nil, want default generator installed")
	}
	if id := tr.newMsgID(); id == "" {
		t.Errorf("default newMsgID() = %q, want non-empty", id)
	}
}

// TestNew_KeepsCustomIDGenerator pins that a WithIDGenerator override is
// preserved and never replaced by the default generator.
func TestNew_KeepsCustomIDGenerator(t *testing.T) {
	tr := New(newBaseDeps(), "/tmp", WithIDGenerator(func() string { return "custom-id" }))
	if got := tr.newMsgID(); got != "custom-id" {
		t.Errorf("newMsgID() = %q, want %q (custom generator must not be overwritten)", got, "custom-id")
	}
}
