package api

import (
	"strings"
	"testing"
)

// TestModelServed pins the entitlement predicate, including both fail-open cases.
// They are the ones a naive implementation gets wrong, and each wrong answer is
// worse than the defect the check exists to prevent: refusing every model when a
// backend advertises no catalog, or refusing an inherited empty value that means
// "use the backend default".
func TestModelServed(t *testing.T) {
	t.Parallel()
	served := []string{"claude-sonnet-4", "claude-haiku-4", "auto"}
	cases := []struct {
		name   string
		id     string
		served []string
		want   bool
	}{
		{"a served id", "claude-sonnet-4", served, true},
		{"an unserved id", "claude-opus-9", served, false},
		{"an empty id means inherit the default", "", served, true},
		{"the auto sentinel is never validated", ModelAuto, []string{"only-this"}, true},
		{"an empty served set means entitlement is unknowable", "anything", nil, true},
		{"an empty served set plus an empty id", "", nil, true},
		{
			// The case that decides whether the check is safe to add at all. The
			// display list drops [Deprecated]/[Legacy] entries, so a check against
			// it would refuse a model the account can still run. Callers must pass
			// the UNFILTERED set, and this pins that a present-but-different id is
			// refused so the distinction is not silently lost.
			"an id absent from the set is refused even when the set looks complete",
			"claude-legacy-1", served, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := ModelServed(c.id, c.served); got != c.want {
				t.Errorf("ModelServed(%q, %v) = %v, want %v", c.id, c.served, got, c.want)
			}
		})
	}
}

// FuzzModelServed pins that the predicate agrees with a trivial oracle for every
// input, so no id can pass by a normalisation or comparison quirk. An entitlement
// check that answers yes for an id the account does not have is the whole bug.
func FuzzModelServed(f *testing.F) {
	f.Add("claude-sonnet-4", "claude-sonnet-4,claude-haiku-4")
	f.Add("", "a,b")
	f.Add("x", "")
	f.Add("AUTO", "auto")
	f.Fuzz(func(t *testing.T, id, csv string) {
		var served []string
		if csv != "" {
			served = strings.Split(csv, ",")
		}
		// The oracle is a MAP rather than slices.Contains, deliberately: production
		// uses slices.Contains, and an oracle built from the same primitive would
		// agree with a bug in it.
		set := make(map[string]struct{}, len(served))
		for _, sv := range served {
			set[sv] = struct{}{}
		}
		_, member := set[id]
		want := id == "" || id == ModelAuto || len(served) == 0 || member
		got := ModelServed(id, served)
		if got != want {
			t.Fatalf("ModelServed(%q, %v) = %v, want %v", id, served, got, want)
		}
		// Exact comparison only: a case-folded or trimmed match would let an id the
		// backend never advertised through.
		if got && id != "" && id != ModelAuto && len(served) > 0 && !member {
			t.Fatalf("ModelServed accepted %q which is not exactly in %v", id, served)
		}
	})
}
