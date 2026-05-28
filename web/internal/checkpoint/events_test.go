package checkpoint

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

func TestEventLogAppendRead(t *testing.T) {
	l := newEventLog(t.TempDir(), "chat1")
	evs := []event{
		{Kind: kindTurnStart, Turn: 1, MessageCount: 2},
		{Kind: kindSnapshot, Turn: 1, Tool: 0, Tag: "1", Path: "main.go", BeforeSHA: "aaa", AfterSHA: "bbb", MessageCount: 3},
		{Kind: kindSnapshot, Turn: 1, Tool: 1, Tag: "1.1", Path: "util.go", BeforeSHA: "", AfterSHA: "ccc", MessageCount: 4},
	}
	for i := range evs {
		if err := l.Append(context.Background(), &evs[i]); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := l.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != len(evs) {
		t.Fatalf("got %d events, want %d", len(got), len(evs))
	}
	for i, e := range evs {
		if got[i].Kind != e.Kind || got[i].Tag != e.Tag || got[i].Path != e.Path {
			t.Errorf("event[%d] = %+v, want shape %+v", i, got[i], e)
		}
		if got[i].TS == 0 {
			t.Errorf("event[%d].TS = 0, expected auto-populated", i)
		}
		if got[i].V != currentEventVersion {
			t.Errorf("event[%d].V = %d, want %d", i, got[i].V, currentEventVersion)
		}
	}
}

func TestEventLogStampsCurrentVersion(t *testing.T) {
	// Append leaves V==0 untouched on input events that already
	// carry a future version number, so a forward-compat upgrade
	// path can write events with the new schema without being
	// silently downgraded by the auto-stamp.
	l := newEventLog(t.TempDir(), "chat-version")
	cases := []struct {
		ev    event
		wantV int
	}{
		{event{Kind: kindTurnStart, Turn: 1}, currentEventVersion},
		{event{Kind: kindTurnStart, Turn: 2, V: 99}, 99},
	}
	for i := range cases {
		if err := l.Append(context.Background(), &cases[i].ev); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}
	got, err := l.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != len(cases) {
		t.Fatalf("got %d events, want %d", len(got), len(cases))
	}
	for i, c := range cases {
		if got[i].V != c.wantV {
			t.Errorf("event[%d].V = %d, want %d", i, got[i].V, c.wantV)
		}
	}
}

func TestEventLogReadsLegacyEventsWithoutV(t *testing.T) {
	// Pre-versioning logs have lines without a "v" field; readers
	// must continue to parse them and surface V=0 so callers can
	// distinguish legacy data without needing a migration step.
	dir := t.TempDir()
	l := newEventLog(dir, "chat-legacy")
	// Hand-craft a legacy line (no v field) and write it directly
	// to the on-disk path that newEventLog computes.
	legacy := []byte(`{"type":"turn_start","ts":1700000000000,"turn":1}` + "\n")
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := l.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].V != 0 {
		t.Errorf("legacy event V = %d, want 0", got[0].V)
	}
	if got[0].Kind != kindTurnStart {
		t.Errorf("legacy event Kind = %q, want turn_start", got[0].Kind)
	}
}

func TestEventLogReadMissingFile(t *testing.T) {
	// A fresh chat with no snapshots yet should read as an empty
	// slice without error. The HTTP handlers rely on this to avoid
	// 500s on "chat has never been touched".
	l := newEventLog(t.TempDir(), "never-used")
	evs, err := l.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("missing file Read = %d events, want 0", len(evs))
	}
}

func TestEventLogTolerantOfPartialLines(t *testing.T) {
	// A crash mid-append could leave a truncated JSON line. Replay
	// must skip that line and return valid events, not abort.
	dir := t.TempDir()
	l := newEventLog(dir, "c")
	if err := l.Append(context.Background(), &event{Kind: kindSnapshot, Tag: "1", Path: "a.go"}); err != nil {
		t.Fatal(err)
	}
	// Manually scribble a broken line. Both WriteString and
	// Close return errors; _-discard keeps the errcheck-clean
	// intent explicit without failing the test on a tmpfs
	// hiccup.
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"snapshot","tag":"bro`)
	_ = f.Close()
	if err := l.Append(context.Background(), &event{Kind: kindSnapshot, Tag: "2", Path: "b.go"}); err != nil {
		t.Fatal(err)
	}
	got, err := l.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Two good events survive the bad middle line.
	if len(got) != 2 {
		t.Errorf("Read = %d events, want 2 (bad line skipped)", len(got))
	}
}

// TestEventLogReadRejectsDirectoryAtPath covers the non-IsNotExist
// branch of Read: if the events.jsonl path is itself a directory,
// Read must return an error — swallowing it as "fresh chat" would
// silently lose history.
func TestEventLogReadRejectsDirectoryAtPath(t *testing.T) {
	dir := t.TempDir()
	logPath := chatLogPath(dir, "c")
	// Create events.jsonl AS A DIRECTORY. On POSIX, os.Open on
	// a directory succeeds; the scanner's first Read surfaces
	// EISDIR. On some filesystems Open itself fails — either
	// path results in a non-nil error out of Read.
	if err := os.MkdirAll(logPath, 0o700); err != nil {
		t.Fatal(err)
	}
	l := newEventLog(dir, "c")
	_, err := l.Read(context.Background())
	if err == nil {
		t.Fatal("Read on a directory returned nil error, want non-nil")
	}
	if os.IsNotExist(err) {
		t.Errorf("Read err = %v, want non-IsNotExist (dir exists)", err)
	}
}

// TestEventLogAppendReadAllEventKinds round-trips one event of
// each kind through Append+Read. Catches any json:"..." tag drift
// (omitempty on a field production relies on, wrong tag name, new
// field forgotten in the struct literal).
func TestEventLogAppendReadAllEventKinds(t *testing.T) {
	l := newEventLog(t.TempDir(), "c")
	evs := []event{
		{Kind: kindTurnStart, Turn: 3, MessageCount: 10},
		{Kind: kindSnapshot, Tag: "3", Path: "a", BeforeSHA: "b0", AfterSHA: "a0", Tool: 0, Turn: 3, MessageCount: 11},
		{Kind: kindRestore, Tag: "1", MessageCount: 5},
		{Kind: kindRestoreStarted, Tag: "2", MessageCount: 6},
		{Kind: kindRestoreCommitted, Tag: "2", MessageCount: 6},
		{Kind: kindConflict, Path: "a", OtherChat: "X", ExpectedSHA: "e", BeforeSHA: "actual", Tag: "3"},
	}
	for i := range evs {
		if err := l.Append(context.Background(), &evs[i]); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}
	got, err := l.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(evs) {
		t.Fatalf("Read len = %d, want %d", len(got), len(evs))
	}
	// kindConflict must preserve OtherChat + ExpectedSHA +
	// BeforeSHA — those feed Manager.Conflicts which surfaces
	// them to the UI.
	conf := got[len(got)-1]
	if conf.Kind != kindConflict || conf.OtherChat != "X" ||
		conf.ExpectedSHA != "e" || conf.BeforeSHA != "actual" {
		t.Errorf("kindConflict round-trip lost fields: %+v", conf)
	}
	if got[2].Kind != kindRestore || got[2].Tag != "1" {
		t.Errorf("kindRestore round-trip: %+v", got[2])
	}
	if got[3].Kind != kindRestoreStarted || got[3].Tag != "2" {
		t.Errorf("kindRestoreStarted round-trip: %+v", got[3])
	}
	if got[4].Kind != kindRestoreCommitted || got[4].Tag != "2" {
		t.Errorf("kindRestoreCommitted round-trip: %+v", got[4])
	}
}

func TestEventLogWipe(t *testing.T) {
	dir := t.TempDir()
	l := newEventLog(dir, "c")
	_ = l.Append(context.Background(), &event{Kind: kindSnapshot, Tag: "1"})
	if err := l.Wipe(); err != nil {
		t.Fatalf("Wipe: %v", err)
	}
	chatDir := filepath.Join(chatsRoot(dir), "c")
	if _, err := os.Stat(chatDir); !os.IsNotExist(err) {
		t.Errorf("chat dir should be gone, err = %v", err)
	}
	// Wipe on already-wiped is a no-op, not an error.
	if err := l.Wipe(); err != nil {
		t.Errorf("second Wipe = %v, want nil", err)
	}
}

func TestReplayBuildsState(t *testing.T) {
	evs := []event{
		{Kind: kindTurnStart, Turn: 1, MessageCount: 2},
		{Kind: kindSnapshot, Turn: 1, Tool: 0, Tag: "1", Path: "a", BeforeSHA: "a0", AfterSHA: "a1", MessageCount: 3},
		{Kind: kindSnapshot, Turn: 1, Tool: 1, Tag: "1.1", Path: "b", BeforeSHA: "", AfterSHA: "b1", MessageCount: 4},
		{Kind: kindTurnStart, Turn: 2, MessageCount: 5},
		{Kind: kindSnapshot, Turn: 2, Tool: 0, Tag: "2", Path: "a", BeforeSHA: "a1", AfterSHA: "a2", MessageCount: 6},
	}
	s := replay(evs)
	if s.turn != 2 {
		t.Errorf("turn = %d, want 2", s.turn)
	}
	if s.toolsInTurn != 1 {
		t.Errorf("toolsInTurn = %d, want 1", s.toolsInTurn)
	}
	if s.tags["1"] != 3 || s.tags["1.1"] != 4 || s.tags["2"] != 6 {
		t.Errorf("tags = %+v, want {1:3, 1.1:4, 2:6}", s.tags)
	}
	if got := s.oldestTag(); got != "1" {
		t.Errorf("oldestTag = %q, want 1", got)
	}
	if sha, ok := s.contentAtTag("a", "2"); !ok || sha != "a1" {
		t.Errorf("contentAtTag(a, 2) = (%q, %v), want (a1, true)", sha, ok)
	}
	// File "b" has no beforeSHA at tag 1.1 (created fresh by the
	// write). contentAtTag must return (_, false) so Restore knows
	// to delete the file rather than try to rewrite empty content.
	if sha, ok := s.contentAtTag("b", "1.1"); ok {
		t.Errorf("contentAtTag(b, 1.1) = (%q, true), want (_, false) for freshly-created file", sha)
	}
}

// TestContentAtTagReturnsFalseWhenBeforeSHAEmpty pins the
// "freshly-created file" branch explicitly. A snapshot recorded
// for a file that didn't exist before the write has an empty
// beforeSHA; Restore needs to distinguish that from a real
// "had content X" case to decide between delete and rewrite.
func TestContentAtTagReturnsFalseWhenBeforeSHAEmpty(t *testing.T) {
	s := replay([]event{
		{Kind: kindSnapshot, Tag: "1", Path: "fresh.go", BeforeSHA: "", AfterSHA: "a1"},
	})
	if sha, ok := s.contentAtTag("fresh.go", "1"); ok {
		t.Errorf("contentAtTag(fresh-file tag) = (%q, true), want (_, false)", sha)
	}
}

// TestContentAtTagUnknownPath guards the nil-history branch: a
// path the chat never snapshotted must return false without
// scanning into nil history.
func TestContentAtTagUnknownPath(t *testing.T) {
	s := replay([]event{{Kind: kindSnapshot, Tag: "1", Path: "a"}})
	if sha, ok := s.contentAtTag("never-touched.go", "1"); ok {
		t.Errorf("contentAtTag(unknown) = (%q, true), want (_, false)", sha)
	}
}

// TestStateReferencesBlobEmptyShaRejected guards the fast path.
// The HTTP blob-read handler validates the requested SHA but the
// empty-sha guard is a belt-and-braces fence so a caller passing
// "" can never be told "yes we reference it".
func TestStateReferencesBlobEmptyShaRejected(t *testing.T) {
	s := replay([]event{
		{Kind: kindSnapshot, Tag: "1", Path: "a", AfterSHA: "deadbeef"},
	})
	if s.referencesBlob("") {
		t.Error(`referencesBlob("") = true, want false`)
	}
	if !s.referencesBlob("deadbeef") {
		t.Error("referencesBlob(known sha) = false, want true")
	}
}

func TestAllocateTag(t *testing.T) {
	cases := []struct {
		name   string
		want   string
		events []event
	}{
		{
			name:   "fresh chat",
			events: nil,
			want:   "0.0",
		},
		{
			name:   "after turn 1 start",
			events: []event{{Kind: kindTurnStart, Turn: 1}},
			want:   "1",
		},
		{
			name: "second tool in turn 1",
			events: []event{
				{Kind: kindTurnStart, Turn: 1},
				{Kind: kindSnapshot, Turn: 1, Tool: 0, Tag: "1"},
			},
			want: "1.1",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := replay(tt.events).allocateTag()
			if got != tt.want {
				t.Errorf("allocateTag = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilesTouchedBetween(t *testing.T) {
	evs := []event{
		{Kind: kindSnapshot, Tag: "1", Path: "a"},
		{Kind: kindSnapshot, Tag: "1.1", Path: "b"},
		{Kind: kindSnapshot, Tag: "2", Path: "a"}, // a touched again
		{Kind: kindSnapshot, Tag: "2", Path: "c"},
	}
	s := replay(evs)
	// from="1" exclusive, to="2" inclusive → 1.1 (b) and 2 (a, c).
	got := s.filesTouchedBetween("1", "2")
	want := map[string]bool{"a": true, "b": true, "c": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want keys %v", got, want)
	}
	for _, f := range got {
		if !want[f] {
			t.Errorf("unexpected file %q", f)
		}
	}
}

// TestFilesInTagRangeBoundaries covers the two branches existing
// tests miss: an empty state returning nil, and a `to` tag that
// doesn't exist in orderedTags (the "walk past the tail" path).
func TestFilesInTagRangeBoundaries(t *testing.T) {
	// Empty state: every range is empty, no panic.
	s := newState()
	if got := s.filesTouchedBetween("0", "1"); got != nil {
		t.Errorf("filesTouchedBetween(empty state) = %v, want nil", got)
	}
	if got := s.filesTouchedAtOrAfter("0"); got != nil {
		t.Errorf("filesTouchedAtOrAfter(empty state) = %v, want nil", got)
	}

	// Populate a small range and probe boundary cases.
	evs := []event{
		{Kind: kindSnapshot, Tag: "1", Path: "a"},
		{Kind: kindSnapshot, Tag: "2", Path: "b"},
	}
	s = replay(evs)

	// `to` tag not present in orderedTags: exclusive upper bound
	// semantics — walk from "1" exclusive to "5" exclusive, which
	// catches tag "2" (path b) but not tag "1" (path a).
	got := s.filesTouchedBetween("1", "5")
	if len(got) != 1 || got[0] != "b" {
		t.Errorf("filesTouchedBetween(1, nonexistent 5) = %v, want [b]", got)
	}

	// `from` tag not present, before the first real tag. Insertion
	// index is 0; `!inclusive && found=false` means no ++; we walk
	// from 0 to endIdx.
	got = s.filesTouchedBetween("0.5", "2")
	want := map[string]bool{"a": true, "b": true}
	if len(got) != 2 {
		t.Fatalf("filesTouchedBetween(0.5, 2) = %v, want 2 entries", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path %q", p)
		}
	}
}

// TestStateApplyConflictsCapAndTruncationFlag pins the
// maxInMemoryConflicts cap: the ring buffer stays at N entries on
// overflow, drops oldest, appends newcomer.
func TestStateApplyConflictsCapAndTruncationFlag(t *testing.T) {
	s := newState()
	// Fill to the cap with distinguishable payloads.
	for i := range maxInMemoryConflicts {
		s.apply(&event{
			Kind:      kindConflict,
			Path:      "p",
			OtherChat: "A",
			Tag:       "t",
			TS:        int64(i),
		})
	}
	conflicts := s.conflicts.slice()
	if len(conflicts) != maxInMemoryConflicts {
		t.Fatalf("len(conflicts) at cap = %d, want %d",
			len(conflicts), maxInMemoryConflicts)
	}
	if conflicts[0].TS != 0 {
		t.Errorf("pre-overflow oldest TS = %d, want 0", conflicts[0].TS)
	}

	// First overflow: drops oldest (TS=0), appends TS=500.
	s.apply(&event{Kind: kindConflict, Path: "p", Tag: "t", TS: int64(maxInMemoryConflicts)})
	conflicts = s.conflicts.slice()
	if len(conflicts) != maxInMemoryConflicts {
		t.Errorf("len(conflicts) after 1 overflow = %d, want %d",
			len(conflicts), maxInMemoryConflicts)
	}
	if conflicts[0].TS != 1 {
		t.Errorf("oldest TS after 1 overflow = %d, want 1", conflicts[0].TS)
	}
	got := conflicts[len(conflicts)-1]
	if got.TS != int64(maxInMemoryConflicts) {
		t.Errorf("newest TS after 1 overflow = %d, want %d",
			got.TS, maxInMemoryConflicts)
	}

	// Second overflow: ring still sheds oldest and admits newcomer.
	s.apply(&event{Kind: kindConflict, Path: "p", Tag: "t", TS: int64(maxInMemoryConflicts + 1)})
	conflicts = s.conflicts.slice()
	if conflicts[0].TS != 2 {
		t.Errorf("oldest TS after 2 overflows = %d, want 2", conflicts[0].TS)
	}
	if conflicts[len(conflicts)-1].TS != int64(maxInMemoryConflicts+1) {
		t.Errorf("newest TS after 2 overflows = %d, want %d",
			conflicts[len(conflicts)-1].TS, maxInMemoryConflicts+1)
	}
}

func TestCompareTags(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0", "1", -1},
		{"1", "1", 0},
		{"1.2", "1", 1},
		{"1.2", "2", -1},
		{"10", "2", 1},
		{"1.10", "1.2", 1},
		{"0.0", "0", 0},
	}
	for _, c := range cases {
		if got := compareTags(c.a, c.b); got != c.want {
			t.Errorf("compareTags(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestCompareTagsInvariants uses pgregory.net/rapid to check the
// ordering invariants (reflexive, antisymmetric, transitive) over
// arbitrary tag strings including edge cases the curated sample
// cannot reach (leading zeros, very large turn numbers, malformed
// separators, empty strings, multi-dot).
func TestCompareTagsInvariants(t *testing.T) {
	tagGen := rapid.OneOf(
		// Well-formed tags: "N" or "N.M"
		rapid.Custom(func(t *rapid.T) string {
			turn := rapid.IntRange(0, 1000).Draw(t, "turn")
			if rapid.Bool().Draw(t, "hasTool") {
				tool := rapid.IntRange(0, 100).Draw(t, "tool")
				return strconv.Itoa(turn) + "." + strconv.Itoa(tool)
			}
			return strconv.Itoa(turn)
		}),
		// Edge-case tags: empty, non-numeric, multi-dot
		rapid.SampledFrom([]string{"", "0", "0.0", "abc", "1.2.3", "999"}),
	)

	rapid.Check(t, func(t *rapid.T) {
		a := tagGen.Draw(t, "a")
		b := tagGen.Draw(t, "b")
		c := tagGen.Draw(t, "c")

		// Reflexive: cmp(x, x) == 0.
		if got := compareTags(a, a); got != 0 {
			t.Fatalf("reflexivity: cmp(%q,%q) = %d, want 0", a, a, got)
		}

		// Antisymmetric: sign(cmp(a,b)) == -sign(cmp(b,a)).
		ab := compareTags(a, b)
		ba := compareTags(b, a)
		if (ab < 0) != (ba > 0) || (ab > 0) != (ba < 0) {
			t.Fatalf("antisymmetry: cmp(%q,%q)=%d, reverse cmp(%q,%q)=%d",
				a, b, ab, b, a, ba)
		}

		// Transitive: a<=b && b<=c implies a<=c.
		bc := compareTags(b, c)
		ac := compareTags(a, c)
		if ab <= 0 && bc <= 0 && ac > 0 {
			t.Fatalf("transitivity: %q<=%q<=%q but cmp(%q,%q)=%d",
				a, b, c, a, c, ac)
		}
	})
}

// TestCompareTagsAgreesWithNumericOrder pins the semantic:
// compareTags orders by (turn, tool) numerically, not
// lexicographically. The "10"/"2" and "1.10"/"1.2" rows are the
// load-bearing ones — they pass compareTags but would fail a
// strings.Compare-based refactor.
func TestCompareTagsAgreesWithNumericOrder(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"10", "2", 1},     // 10 > 2 numerically; "10" < "2" lexicographically
		{"1.10", "1.2", 1}, // same intra-turn
		{"2.9", "2.10", -1},
		{"1", "1.0", 0}, // tool-0 equivalence
		{"0", "0.1", -1},
	}
	for _, c := range cases {
		if got := compareTags(c.a, c.b); got != c.want {
			t.Errorf("compareTags(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestAtoiSafeRejectsNonDigits covers the non-digit branch. A
// corrupted events.jsonl with garbage in a tag's numeric segment
// must not abort replay; atoiSafe is the safety valve.
func TestAtoiSafeRejectsNonDigits(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"abc", 0},
		{"12x", 0},
		{"-5", 0},  // leading minus
		{"1.5", 0}, // decimal
		{" 7", 0},  // leading whitespace
		{"", 0},    // empty (zero-loop path)
	}
	for _, tc := range cases {
		if got := atoiSafe(tc.in); got != tc.want {
			t.Errorf("atoiSafe(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestAtoiSafeOverflowSaturatesToZero pins the `n < 0` overflow
// guard: a string long enough to overflow int64 on any platform
// must return 0, not a truncated value that would silently
// reorder tags.
func TestAtoiSafeOverflowSaturatesToZero(t *testing.T) {
	if got := atoiSafe("999999999999999999999"); got != 0 {
		t.Errorf("atoiSafe(overflow) = %d, want 0", got)
	}
}

// TestAtoiSafeAcceptsValidNonNegativeIntegers pins the positive
// path: every representative non-negative int round-trips through
// strconv.Itoa → atoiSafe. A rapid-based PBT would span a wider
// input space but vibekit doesn't carry that dep.
func TestAtoiSafeAcceptsValidNonNegativeIntegers(t *testing.T) {
	cases := []int{0, 1, 9, 10, 99, 100, 12345, 1_000_000, 1_000_000_000}
	for _, n := range cases {
		s := strconv.Itoa(n)
		if got := atoiSafe(s); got != n {
			t.Errorf("atoiSafe(%q) = %d, want %d", s, got, n)
		}
	}
}

// TestEventLogReadSkipsOversizedLine pins the CYCLE 1 fix for the
// bufio scanner's ErrTooLong path. A chat's events.jsonl can
// accumulate a pathological line (corrupt write, future event-
// field bloat) larger than the scanner's default 64 KiB buffer.
// Pre-fix, bufio.Scanner's default limit returned ErrTooLong from
// sc.Err and Read bubbled that up, making the chat unreplayable
// forever. Post-fix, Read installs a 32 MiB ceiling via
// sc.Buffer and explicitly folds ErrTooLong into a warn + partial
// success — every event BEFORE the bad line still replays.
//
// This test writes one valid event, one oversized line (> 32 MiB,
// well past eventLogMaxLine), then another valid event. Expected:
// at least the first valid event returned, no error surfaced.
// Without the fix the test fails with an error from Read AND
// len(got) == 0.
func TestEventLogReadSkipsOversizedLine(t *testing.T) {
	dir := t.TempDir()
	l := newEventLog(dir, "c")
	if err := l.Append(context.Background(), &event{Kind: kindSnapshot, Tag: "1", Path: "a.go"}); err != nil {
		t.Fatal(err)
	}

	// Inject a single oversized JSON-shaped line that exceeds the
	// 32 MiB cap. Use a long description field so the line is
	// still valid UTF-8 (the scanner reads bytes and would hit
	// ErrTooLong before Unmarshal could complain about shape).
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// 33 MiB of 'x' padding inside a description field, then a
	// trailing newline so the scanner hits the cap before it
	// finds the line terminator.
	const oversized = (32 << 20) + (1 << 20) // 33 MiB
	if _, err := f.WriteString(`{"type":"snapshot","tag":"bad","description":"`); err != nil {
		t.Fatal(err)
	}
	pad := bytes.Repeat([]byte{'x'}, oversized)
	if _, err := f.Write(pad); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Append one more valid event AFTER the oversized line. The
	// scanner should recover past the bad token and still scan
	// this one (though the regression contract below only
	// requires the pre-oversized event to survive).
	if err := l.Append(context.Background(), &event{Kind: kindSnapshot, Tag: "2", Path: "b.go"}); err != nil {
		t.Fatal(err)
	}

	got, err := l.Read(context.Background())
	if err != nil {
		t.Fatalf("Read after oversized line = %v, want nil (fix folds ErrTooLong into partial success)", err)
	}
	// At MINIMUM the first valid event before the oversized line
	// must survive. The post-oversized event may or may not be
	// recoverable depending on how bufio.Scanner advances past
	// the truncated token; both outcomes are acceptable for the
	// regression contract (the fix is "don't return error", not
	// "recover every valid line perfectly").
	if len(got) < 1 {
		t.Fatalf("Read = %d events, want >=1 (at least the pre-oversized event must survive)", len(got))
	}
	if got[0].Tag != "1" {
		t.Errorf("first event.Tag = %q, want %q", got[0].Tag, "1")
	}
}

// TestContentAtTagBinarySearchMidpointMiss exercises the binary
// search iteration where compareTags(history[mid].tag, tag) < 0 and
// the search must advance lo = mid + 1. Existing tests only cover
// single-entry histories (mid=0 hit) so a mutation that replaced
// the c<0 branch with c>0-style (hi = mid) would pass them but
// return !ok for any tag in the upper half of a multi-entry
// history. Restore and Diff both walk contentAtTag for every file
// touched in the relevant range, so this branch is load-bearing
// at scale.
func TestContentAtTagBinarySearchMidpointMiss(t *testing.T) {
	evs := []event{
		{Kind: kindSnapshot, Tag: "1", Path: "f.go", BeforeSHA: "s1"},
		{Kind: kindSnapshot, Tag: "2", Path: "f.go", BeforeSHA: "s2"},
		{Kind: kindSnapshot, Tag: "3", Path: "f.go", BeforeSHA: "s3"},
		{Kind: kindSnapshot, Tag: "4", Path: "f.go", BeforeSHA: "s4"},
		{Kind: kindSnapshot, Tag: "5", Path: "f.go", BeforeSHA: "s5"},
	}
	s := replay(evs)
	// Every tag in the history must resolve to its own beforeSHA.
	// A binary-search regression surfaces as ok=false on tags in
	// whichever half the broken branch mishandles.
	for _, tag := range []string{"1", "2", "3", "4", "5"} {
		want := "s" + tag
		got, ok := s.contentAtTag("f.go", tag)
		if !ok {
			t.Errorf("contentAtTag(f.go, %q) ok = false, want content %q", tag, want)
			continue
		}
		if got != want {
			t.Errorf("contentAtTag(f.go, %q) = %q, want %q", tag, got, want)
		}
	}
}

// FuzzEventLogRead exercises the JSONL event replay path against
// arbitrary byte strings. Seeded with known-good event shapes from
// TestEventLogAppendReadAllEventKinds. The function must never panic
// or infinite-loop on malformed input.
func FuzzEventLogRead(f *testing.F) {
	// Seed corpus: valid JSONL lines from known event kinds.
	seeds := []string{
		`{"type":"turn_start","turn":3,"message_count":10,"ts":1}`,
		`{"type":"snapshot","tag":"3","path":"a","before_sha":"b0","after_sha":"a0","tool":0,"turn":3,"message_count":11,"ts":2}`,
		`{"type":"restore","tag":"1","message_count":5,"ts":3}`,
		`{"type":"restore_started","tag":"2","message_count":6,"ts":4}`,
		`{"type":"restore_committed","tag":"2","message_count":6,"ts":5}`,
		`{"type":"conflict_detected","path":"a","other_chat":"X","expected_sha":"e","before_sha":"actual","tag":"3","ts":6}`,
		// Malformed seeds to guide the fuzzer.
		`{"type":"snapshot","tag":"`,
		``,
		`{`,
		"\x00\x01\x02",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		l := newEventLog(dir, "fuzz")
		// Write raw bytes as the event log content.
		logPath := l.path
		if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(logPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		// Read must not panic. Errors are acceptable.
		_, _ = l.Read(context.Background())
	})
}

// BenchmarkReplay measures event-log replay cost at scale to detect
// regressions in replay throughput. Tests 100, 1000, and 10000
// events (mix of kindTurnStart and kindSnapshot).
func BenchmarkReplay(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		evs := makeSyntheticEvents(size)
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				replay(evs)
			}
		})
	}
}

// makeSyntheticEvents generates n events: alternating turn_start and
// snapshot events to simulate realistic replay workloads.
func makeSyntheticEvents(n int) []event {
	evs := make([]event, 0, n)
	turn := 0
	tool := 0
	for i := range n {
		if i%5 == 0 {
			turn++
			tool = 0
			evs = append(evs, event{
				Kind:         kindTurnStart,
				Turn:         turn,
				MessageCount: i,
				TS:           int64(i),
			})
		} else {
			tag := strconv.Itoa(turn)
			if tool > 0 {
				tag += "." + strconv.Itoa(tool)
			}
			evs = append(evs, event{
				Kind:         kindSnapshot,
				Turn:         turn,
				Tool:         tool,
				Tag:          tag,
				Path:         "file" + strconv.Itoa(i%20) + ".go",
				BeforeSHA:    "sha" + strconv.Itoa(i-1),
				AfterSHA:     "sha" + strconv.Itoa(i),
				MessageCount: i + 1,
				TS:           int64(i),
			})
			tool++
		}
	}
	return evs
}

// FuzzAtoiSafe exercises the integer parser against arbitrary byte
// strings. Asserts: never panics, never returns negative, and agrees
// with strconv.Atoi on valid non-negative inputs.
func FuzzAtoiSafe(f *testing.F) {
	f.Add("0")
	f.Add("1")
	f.Add("123")
	f.Add("999999999")
	f.Add("")
	f.Add("abc")
	f.Add("-5")
	f.Add("12x")
	f.Add("999999999999999999999")

	f.Fuzz(func(t *testing.T, s string) {
		got := atoiSafe(s)
		if got < 0 {
			t.Errorf("atoiSafe(%q) = %d, must never be negative", s, got)
		}
		// Oracle: if strconv.Atoi succeeds and result >= 0,
		// atoiSafe must agree.
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			if got != n {
				t.Errorf("atoiSafe(%q) = %d, strconv.Atoi = %d", s, got, n)
			}
		}
	})
}

// FuzzReplay exercises the state-machine replay with arbitrary event
// sequences. Asserts replay never panics and produces consistent state
// (orderedTags sorted, non-negative turn).
func FuzzReplay(f *testing.F) {
	// Seed with a minimal valid sequence.
	f.Add(uint8(0), uint8(1), uint8(0), uint8(2))
	f.Fuzz(func(t *testing.T, b1, b2, b3, b4 uint8) {
		kinds := []eventKind{kindTurnStart, kindSnapshot, kindRestore,
			kindRestoreStarted, kindRestoreCommitted, kindConflict}
		pick := func(b uint8) eventKind { return kinds[int(b)%len(kinds)] }

		events := []event{
			{Kind: pick(b1), Turn: int(b1 % 10), Tag: strconv.Itoa(int(b1 % 10)), Path: "a.go", AfterSHA: "sha1", TS: 1},
			{Kind: pick(b2), Turn: int(b2 % 10), Tag: strconv.Itoa(int(b2%10)) + ".1", Path: "b.go", AfterSHA: "sha2", TS: 2},
			{Kind: pick(b3), Turn: int(b3 % 10), Tag: strconv.Itoa(int(b3 % 10)), Path: "c.go", AfterSHA: "sha3", TS: 3},
			{Kind: pick(b4), Turn: int(b4 % 10), Tag: strconv.Itoa(int(b4%10)) + ".2", Path: "d.go", AfterSHA: "sha4", TS: 4},
		}

		// Must not panic.
		s := replay(events)

		// Basic consistency: turn is non-negative.
		if s.turn < 0 {
			t.Fatalf("replay produced negative turn: %d", s.turn)
		}

		// orderedTags should be sorted by compareTags.
		for i := 1; i < len(s.orderedTags); i++ {
			if compareTags(s.orderedTags[i-1], s.orderedTags[i]) > 0 {
				t.Fatalf("orderedTags not sorted: %v", s.orderedTags)
			}
		}
	})
}
