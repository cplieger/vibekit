package buffer

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// bigFile builds an n-line file, one "line <i>\n" per line.
func bigFile(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("line ")
		b.WriteString(itoa(i))
		b.WriteString("\n")
	}
	return b.String()
}

// itoa is a tiny decimal formatter, kept local so bigFile stays allocation-cheap
// and the test file needs no strconv import for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// replaceLine returns text with its 0-based idx'th line replaced by want.
func replaceLine(text string, idx int, want string) string {
	lines := strings.Split(text, "\n")
	lines[idx] = want
	return strings.Join(lines, "\n")
}

// TestLineDelta covers the counts a turn footer shows.
//
// The headline case is the reported bug: one line rewritten inside a 300-line
// file used to report 300 added / 300 removed, because the counts were the
// newline count of each whole-file side rather than a diff.
func TestLineDelta(t *testing.T) {
	big := bigFile(300)

	// The 150th line, rewritten. 300 lines in, 300 lines out, one changed.
	oneEdited := replaceLine(big, 149, "line 149 EDITED")

	inserted := strings.Replace(big, "line 40\n", "line 40\n"+bigFile(10), 1)

	deletedLines := strings.Split(big, "\n")
	withoutFive := strings.Join(append(append([]string{}, deletedLines[:100]...), deletedLines[105:]...), "\n")

	var other strings.Builder
	for i := range 300 {
		other.WriteString("other " + itoa(i) + "\n")
	}

	tests := []struct {
		name     string
		old, new string
		added    int
		removed  int
	}{
		{"single-line edit in a large file", big, oneEdited, 1, 1},
		{"pure insertion", big, inserted, 10, 0},
		{"pure deletion", big, withoutFive, 0, 5},
		{"whole-file replacement", big, other.String(), 300, 300},
		{"no-op write", big, big, 0, 0},
		{"new file creation", "", "line1\nline2\n", 2, 0},
		{"file deletion", "x\ny\n", "", 0, 2},
		{"no trailing newline", "a\nb\nc", "x\ny\nz\nw", 4, 3},
		{"CRLF vs LF only", "a\r\nb\r\n", "a\nb\n", 0, 0},
		{"trailing-newline-only change", "a\nb", "a\nb\n", 0, 0},
		{"both sides empty", "", "", 0, 0},
		{"single empty line added", "", "\n", 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := lineDelta(tt.old, tt.new)
			if added != tt.added || removed != tt.removed {
				t.Errorf("lineDelta(old, new) = +%d/-%d, want +%d/-%d", added, removed, tt.added, tt.removed)
			}
		})
	}
}

// TestLineDelta_BudgetFallback pins the coarse answer past maxDiffCells: a
// bounded delete-all / add-all, never a panic and never a quadratic pass.
func TestLineDelta_BudgetFallback(t *testing.T) {
	const n = 6000 // 6000*6000 = 36M cells, past maxDiffCells
	var a, b strings.Builder
	for i := range n {
		a.WriteString("a" + itoa(i) + "\n")
		b.WriteString("b" + itoa(i) + "\n")
	}
	added, removed := lineDelta(a.String(), b.String())
	if added != n || removed != n {
		t.Errorf("lineDelta over the budget = +%d/-%d, want +%d/-%d", added, removed, n, n)
	}
}

// TestLineDelta_Fixture reads the cross-language fixture. Its TypeScript twin is
// static-src/line-delta.node.test.ts; both must agree on every case.
func TestLineDelta_Fixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/line_delta.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture struct {
		Cases []struct {
			Name    string `json:"name"`
			Old     string `json:"old"`
			New     string `json:"new"`
			Added   int    `json:"added"`
			Removed int    `json:"removed"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			added, removed := lineDelta(c.Old, c.New)
			if added != c.Added || removed != c.Removed {
				t.Errorf("lineDelta(%q, %q) = +%d/-%d, want +%d/-%d", c.Old, c.New, added, removed, c.Added, c.Removed)
			}
		})
	}
}

// TestSplitDiffLines pins the canonical line vocabulary the counts rest on:
// a final newline adds no line, and a trailing CR is not part of the line.
func TestSplitDiffLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"one line no newline", "a", []string{"a"}},
		{"one line trailing newline", "a\n", []string{"a"}},
		{"two lines", "a\nb", []string{"a", "b"}},
		{"two lines trailing newline", "a\nb\n", []string{"a", "b"}},
		{"crlf stripped", "a\r\nb\r\n", []string{"a", "b"}},
		{"lone newline is one empty line", "\n", []string{""}},
		{"blank line kept in the middle", "a\n\nb\n", []string{"a", "", "b"}},
		{"only the last empty element is dropped", "a\n\n", []string{"a", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitDiffLines(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("splitDiffLines(%q) = %q, want %q", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("splitDiffLines(%q) = %q, want %q", tt.in, got, tt.want)
				}
			}
		})
	}
}

// TestLcsLen pins the primitive the counts derive from, including the symmetry
// the row-width swap depends on.
func TestLcsLen(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want int
	}{
		{"both empty", nil, nil, 0},
		{"one empty", []string{"a"}, nil, 0},
		{"identical", []string{"a", "b"}, []string{"a", "b"}, 2},
		{"disjoint", []string{"a", "b"}, []string{"c", "d"}, 0},
		{"interleaved", []string{"a", "b", "c"}, []string{"x", "b", "y", "c"}, 2},
		{"repeats", []string{"x", "x", "x"}, []string{"x"}, 1},
		{"longer first", []string{"a", "b", "c", "d"}, []string{"b", "d"}, 2},
		{"longer second", []string{"b", "d"}, []string{"a", "b", "c", "d"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lcsLen(tt.a, tt.b); got != tt.want {
				t.Errorf("lcsLen(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestLineHunks covers the editor gutter's input: which NEW-text lines a diff
// touched. The headline case is the sibling of the footer bug — a one-line edit
// used to record 1..300 for a 300-line file, painting the whole gutter.
func TestLineHunks(t *testing.T) {
	big := bigFile(300)
	tests := []struct {
		name     string
		old, new string
		want     []lineHunk
	}{
		{
			"the 150th line of a 300-line file, edited",
			big, replaceLine(big, 149, "line 149 EDITED"),
			[]lineHunk{{StartLine: 150, EndLine: 150}},
		},
		{"no-op write records nothing", big, big, nil},
		{"new file spans every line", "", "a\nb\nc\n", []lineHunk{{StartLine: 1, EndLine: 3}}},
		{
			"insertion covers only the inserted lines",
			"a\nb\nc\n", "a\nx\ny\nb\nc\n",
			[]lineHunk{{StartLine: 2, EndLine: 3}},
		},
		{
			"deletion marks the junction line",
			"a\nb\nc\nd\n", "a\nd\n",
			[]lineHunk{{StartLine: 2, EndLine: 2}},
		},
		{
			"deletion at end of file clamps to the last line",
			"a\nb\nc\n", "a\n",
			[]lineHunk{{StartLine: 1, EndLine: 1}},
		},
		{
			"two separate edits record two hunks",
			"a\nb\nc\nd\ne\n", "a\nB\nc\nD\ne\n",
			[]lineHunk{{StartLine: 2, EndLine: 2}, {StartLine: 4, EndLine: 4}},
		},
		{
			"whole-file rewrite still spans the file",
			"a\nb\nc\n", "x\ny\nz\n",
			[]lineHunk{{StartLine: 1, EndLine: 3}},
		},
		{"file emptied marks line 1", "a\nb\n", "", []lineHunk{{StartLine: 1, EndLine: 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lineHunks(tt.old, tt.new)
			if len(got) != len(tt.want) {
				t.Fatalf("lineHunks() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("lineHunks() = %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

// TestLineHunks_BudgetFallback pins maxHunkCells, the traceback's own budget: the
// dense table is 8 bytes a cell, so the hunks path degrades to one coarse hunk far
// earlier than the counts path does. A trimmed middle of 2099 lines each side is
// 4.4M cells — past maxHunkCells, well under maxDiffCells — and every odd line
// differs, so a dense traceback would return ~1050 separate hunks.
func TestLineHunks_BudgetFallback(t *testing.T) {
	const n = 2100
	old := bigFile(n)
	lines := strings.Split(old, "\n")
	for i := 1; i < n; i += 2 {
		lines[i] += " EDITED"
	}
	got := lineHunks(old, strings.Join(lines, "\n"))
	want := lineHunk{StartLine: 2, EndLine: n}
	if len(got) != 1 {
		// Print the count, not the slice: the dense path returns ~1050 hunks here.
		t.Fatalf("lineHunks over the traceback budget returned %d hunks, want 1 coarse hunk %+v", len(got), want)
	}
	if got[0] != want {
		t.Errorf("lineHunks over the traceback budget = %+v, want %+v", got[0], want)
	}
}

// TestLineHunks_StayInsideNewText pins the invariant the gutter depends on:
// every recorded range sits inside the new text, ordered and non-inverted.
func TestLineHunks_StayInsideNewText(t *testing.T) {
	cases := [][2]string{
		{"a\nb\nc\n", "a\nB\nc\n"},
		{"a\nb\nc\nd\ne\n", "a\ne\n"},
		{"", "x\n"},
		{"x\n", ""},
		{"a\nb", "a\nb\n"},
		{bigFile(50), replaceLine(bigFile(50), 49, "tail EDITED")},
	}
	for _, c := range cases {
		newLines := len(splitDiffLines(c[1]))
		prevEnd := 0
		for _, h := range lineHunks(c[0], c[1]) {
			if h.StartLine < 1 || h.EndLine < h.StartLine {
				t.Errorf("lineHunks(%q, %q) produced inverted range %+v", c[0], c[1], h)
			}
			if newLines > 0 && h.EndLine > newLines {
				t.Errorf("lineHunks(%q, %q) range %+v past %d new lines", c[0], c[1], h, newLines)
			}
			if h.StartLine < prevEnd {
				t.Errorf("lineHunks(%q, %q) ranges out of order at %+v", c[0], c[1], h)
			}
			prevEnd = h.EndLine
		}
	}
}
