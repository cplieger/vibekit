package pending

import "testing"

// --- removeID characterization tests ---
//
// removeID is unexported and defensively handles the not-found and
// empty cases, both of which are unreachable via the public API
// today (callers always pass an id they just located in byChat).
// These tests pin the invariants so a future refactor — e.g.
// switching to slices.DeleteFunc — cannot silently regress the
// empty-slice or not-found semantics.

// TestRemoveID_NotFound returns the slice unchanged (length + values
// preserved) when target is absent.
func TestRemoveID_NotFound(t *testing.T) {
	t.Parallel()

	in := []string{"a", "b", "c"}
	out := removeID(in, "missing")

	if len(out) != 3 {
		t.Fatalf("removeID not-found len = %d, want 3", len(out))
	}
	for i, v := range []string{"a", "b", "c"} {
		if out[i] != v {
			t.Errorf("removeID not-found [%d] = %q, want %q", i, out[i], v)
		}
	}
}

// TestRemoveID_Empty handles nil/empty input without panic.
func TestRemoveID_Empty(t *testing.T) {
	t.Parallel()

	out := removeID(nil, "anything")

	if len(out) != 0 {
		t.Errorf("removeID(nil) len = %d, want 0", len(out))
	}
}

// TestRemoveID_FirstMiddleLast covers every positional case through
// a table-driven subtest.
func TestRemoveID_FirstMiddleLast(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		target string
		in     []string
		want   []string
	}{
		{name: "first", in: []string{"a", "b", "c"}, target: "a", want: []string{"b", "c"}},
		{name: "middle", in: []string{"a", "b", "c"}, target: "b", want: []string{"a", "c"}},
		{name: "last", in: []string{"a", "b", "c"}, target: "c", want: []string{"a", "b"}},
		{name: "only", in: []string{"a"}, target: "a", want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := removeID(append([]string(nil), tc.in...), tc.target)
			if len(got) != len(tc.want) {
				t.Fatalf("removeID(%v, %q) len = %d, want %d", tc.in, tc.target, len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("removeID(%v, %q)[%d] = %q, want %q", tc.in, tc.target, i, got[i], tc.want[i])
				}
			}
		})
	}
}
