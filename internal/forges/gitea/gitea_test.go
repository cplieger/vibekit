package gitea

import "testing"

func TestNew(t *testing.T) {
	_, err := New("codeberg.org")
	if err != nil {
		t.Fatal(err)
	}
}
