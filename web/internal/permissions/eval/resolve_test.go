package eval

import "testing"

func TestResolveExplicitAllow_nil(t *testing.T) {
	got := ResolveExplicitAllow("ls", nil)
	if got != ShellAsk {
		t.Fatalf("expected ShellAsk, got %q", got)
	}
}
