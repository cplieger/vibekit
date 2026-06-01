package github

import "testing"

func TestNew(t *testing.T) {
	_, err := New("github.com")
	if err != nil {
		t.Fatal(err)
	}
}
