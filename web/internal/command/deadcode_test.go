package command

import "testing"

func TestWithBridge(t *testing.T) {
	opt := WithBridge(nil)
	if opt == nil {
		t.Fatal("expected non-nil option")
	}
}

func TestWithChat(t *testing.T) {
	opt := WithChat(nil)
	if opt == nil {
		t.Fatal("expected non-nil option")
	}
}
