package checkpoint

import (
	"fmt"
	"testing"
)

// FuzzStateQueryContentAtTag builds state by applying snapshot events
// with fuzz-generated tags, then verifies contentAtTag and
// contentAtOrBeforeTag never panic and respect their documented
// invariants (contentAtOrBeforeTag is a superset of contentAtTag).
func FuzzStateQueryContentAtTag(f *testing.F) {
	f.Add([]byte{3, 0, 1, 0, 5, 1, 2, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		s := newState()
		var tags []string
		path := "test.go"
		// Apply a sequence of snapshot events with generated tags.
		for len(data) >= 2 {
			turn := int(data[0])
			tool := int(data[1])
			data = data[2:]
			tag := fmt.Sprintf("%d.%d", turn, tool)
			beforeSHA := fmt.Sprintf("b%s", tag)
			afterSHA := fmt.Sprintf("a%s", tag)
			ev := &event{
				Kind:      kindSnapshot,
				Tag:       tag,
				Path:      path,
				BeforeSHA: beforeSHA,
				AfterSHA:  afterSHA,
				TS:        int64(turn*1000 + tool),
			}
			s.apply(ev)
			tags = append(tags, tag)
		}
		// Query with each generated tag and verify invariant:
		// if contentAtTag returns a result, contentAtOrBeforeTag
		// must return the same result for the same tag.
		for _, tag := range tags {
			sha1, ok1 := s.contentAtTag(path, tag)
			sha2, ok2 := s.contentAtOrBeforeTag(path, tag)
			if ok1 && !ok2 {
				t.Fatalf("contentAtTag found %q at %q but contentAtOrBeforeTag did not", sha1, tag)
			}
			if ok1 && ok2 && sha1 != sha2 {
				t.Fatalf("contentAtTag=%q but contentAtOrBeforeTag=%q at tag %q", sha1, sha2, tag)
			}
		}
	})
}
