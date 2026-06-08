package hub

import (
	"testing"

	"github.com/cplieger/vibekit/internal/pending"
)

// FuzzTruncateForStaging exercises the staging truncation helper with
// arbitrary old/new text. Asserts no panic and that output respects cap.
func FuzzTruncateForStaging(f *testing.F) {
	f.Add("old content", "new content")
	f.Add("", "")
	f.Add("x", "y")

	f.Fuzz(func(t *testing.T, oldText, newText string) {
		if len(oldText) > 64*1024 || len(newText) > 64*1024 {
			return
		}
		trimO, trimN, truncated := truncateForStaging(oldText, newText)
		if len(trimO) > pending.Cap {
			t.Errorf("trimmed old len %d exceeds cap %d", len(trimO), pending.Cap)
		}
		if len(trimN) > pending.Cap {
			t.Errorf("trimmed new len %d exceeds cap %d", len(trimN), pending.Cap)
		}
		if len(oldText) <= pending.Cap && len(newText) <= pending.Cap && truncated {
			t.Error("truncated=true but both inputs are within cap")
		}
	})
}
