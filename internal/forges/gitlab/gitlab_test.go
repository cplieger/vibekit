package gitlab

import "testing"

func TestNew(t *testing.T) {
	_, err := New("gitlab.com")
	if err != nil {
		t.Fatal(err)
	}
}
