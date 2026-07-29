package translate

import "testing"

// TestNew_InstallsDefaultIDWhenUnset pins that constructing a Translator
// without withIDGenerator installs the default message-ID generator,
// which produces non-empty IDs.
func TestNew_InstallsDefaultIDWhenUnset(t *testing.T) {
	tr := New(newBaseDeps())
	if tr.newMsgID == nil {
		t.Fatal("New (no withIDGenerator): newMsgID is nil, want default generator installed")
	}
	if id := tr.newMsgID(); id == "" {
		t.Errorf("default newMsgID() = %q, want non-empty", id)
	}
}

// TestNew_KeepsCustomIDGenerator pins that a withIDGenerator override is
// preserved and never replaced by the default generator.
func TestNew_KeepsCustomIDGenerator(t *testing.T) {
	tr := New(newBaseDeps(), withIDGenerator(func() string { return "custom-id" }))
	if got := tr.newMsgID(); got != "custom-id" {
		t.Errorf("newMsgID() = %q, want %q (custom generator must not be overwritten)", got, "custom-id")
	}
}
