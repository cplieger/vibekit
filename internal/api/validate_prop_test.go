package api

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestValidChatID_RapidPathTraversal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.StringMatching(`[A-Za-z0-9_\-]{0,60}`).Draw(t, "base")
		sep := rapid.SampledFrom([]string{"/", "\\", ".."}).Draw(t, "sep")
		suffix := rapid.StringMatching(`[A-Za-z0-9_\-]{0,60}`).Draw(t, "suffix")
		id := base + sep + suffix
		if !strings.Contains(id, "/") && !strings.Contains(id, "\\") && !strings.Contains(id, "..") {
			return // skip if separator got lost
		}
		if ValidChatID(id) {
			t.Fatalf("ValidChatID(%q) = true, want false (contains path separator)", id)
		}
	})
}

func TestValidChatID_RapidLengthBound(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(129, 300).Draw(t, "len")
		id := strings.Repeat("a", n)
		if ValidChatID(id) {
			t.Fatalf("ValidChatID(len=%d) = true, want false", n)
		}
	})
}

func TestValidChatID_RapidAcceptCharset(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := rapid.StringMatching(`[A-Za-z0-9_\-]{1,128}`).Draw(t, "id")
		if !ValidChatID(id) {
			t.Fatalf("ValidChatID(%q) = false, want true", id)
		}
	})
}

func TestValidMessageID_RapidSuperset(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := rapid.StringMatching(`[A-Za-z0-9_.\-:]{1,128}`).Draw(t, "id")
		if ValidMessageID(id) && !ValidRequestID(id) {
			t.Fatalf("ValidMessageID(%q)=true but ValidRequestID(%q)=false", id, id)
		}
	})
}

func TestValidRequestID_RapidEmptyAlwaysValid(t *testing.T) {
	if !ValidRequestID("") {
		t.Fatal("ValidRequestID(\"\") = false, want true")
	}
}
