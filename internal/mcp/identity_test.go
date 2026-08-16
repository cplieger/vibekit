package mcp

// D81: one identity grammar, one writer.
//
// Three doors reach a server identity — Validate (a name), ParseServerID (a URL
// path segment) and paste.go's sanitizeName (a repairer) — and each used to state
// the charset itself, with three comments claiming they matched. These tests are
// what make the claim mechanical: the shared half is asserted per row against
// EVERY door, and the two deliberate differences (the id's bound, the id's
// tolerance of a leading digit) are asserted as differences rather than left to
// be rediscovered as drift.

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"
)

// nameDoorCase is one candidate identity and what each door must say about it.
type nameDoorCase struct {
	name string
	in   string
	// okName is ValidateName's verdict, okID is ParseServerID's. They differ on
	// exactly two axes and each row that uses one says which.
	okName bool
	okID   bool
	why    string
}

func nameDoorTable() []nameDoorCase {
	return []nameDoorCase{
		{name: "Empty", in: "", okName: false, okID: false, why: "no identity at all"},
		{name: "OneLetter", in: "a", okName: true, okID: true, why: "the minimum legal identity"},
		{name: "Ordinary", in: "github", okName: true, okID: true},
		{name: "MixedCase", in: "GitHub", okName: true, okID: true, why: "case is preserved, not folded"},
		{name: "UnderscoreAndDash", in: "my_server-2", okName: true, okID: true},
		{
			name: "LeadingDigit", in: "1github", okName: false, okID: true,
			why: "the lead rule is the tool prefix's; a base32 id may open with a digit",
		},
		{name: "LeadingDash", in: "-github", okName: false, okID: true, why: "same lead rule"},
		{name: "LeadingUnderscore", in: "_github", okName: false, okID: true, why: "same lead rule"},
		{name: "DashOnly", in: "-", okName: false, okID: true, why: "same lead rule"},
		{name: "InteriorSpace", in: "my server", okName: false, okID: false, why: "shared charset"},
		{name: "Dot", in: "my.server", okName: false, okID: false, why: "shared charset"},
		{name: "Slash", in: "my/server", okName: false, okID: false, why: "shared charset"},
		{
			name: "Traversal", in: "../../etc/passwd", okName: false, okID: false,
			why: "the shared charset is what closes this on the id door",
		},
		{name: "DotDot", in: "..", okName: false, okID: false, why: "shared charset"},
		{name: "NonASCIICJK", in: "\u65e5\u672c\u8a9e", okName: false, okID: false, why: "ASCII only"},
		{name: "NonASCIILatin1", in: "caf\u00e9", okName: false, okID: false, why: "ASCII only"},
		{name: "EmbeddedNUL", in: "gi\x00thub", okName: false, okID: false, why: "shared charset"},
		{name: "TrailingSpace", in: "github ", okName: false, okID: false, why: "neither door trims"},
		{
			name: "AtNameBound", in: "a" + strings.Repeat("b", NameMaxLen-1), okName: true, okID: false,
			why: "64 is the name's bound; the id's is 32",
		},
		{
			name: "OverNameBound", in: "a" + strings.Repeat("b", NameMaxLen), okName: false, okID: false,
			why: "one past the name bound",
		},
		{
			name: "AtIDBound", in: "a" + strings.Repeat("b", IDMaxLen-1), okName: true, okID: true,
			why: "32 is legal for both",
		},
		{
			name: "OverIDBound", in: "a" + strings.Repeat("b", IDMaxLen), okName: true, okID: false,
			why: "one past the id bound, still inside the name bound",
		},
	}
}

// TestNameDoorsAgree is D81's mechanical claim: the two rejecting doors agree on
// the shared charset for every row, and disagree ONLY where a difference is
// stated in the table.
func TestNameDoorsAgree(t *testing.T) {
	for _, tc := range nameDoorTable() {
		t.Run(tc.name, func(t *testing.T) {
			gotName := ValidateName(tc.in) == nil
			if gotName != tc.okName {
				t.Errorf("ValidateName(%q) ok=%v, want %v (%s)", tc.in, gotName, tc.okName, tc.why)
			}
			_, idErr := ParseServerID(tc.in)
			gotID := idErr == nil
			if gotID != tc.okID {
				t.Errorf("ParseServerID(%q) ok=%v, want %v (%s)", tc.in, gotID, tc.okID, tc.why)
			}
		})
	}
}

// TestNameDoorsShareTheCharset states the shared half on its own, keyed on the
// property rather than on the row list: for any candidate holding a rune outside
// NameAllowedRune, BOTH doors refuse. This is the half that has to hold whatever
// the bounds do, because it is what refuses a traversal-shaped path segment.
func TestNameDoorsShareTheCharset(t *testing.T) {
	for _, tc := range nameDoorTable() {
		if tc.in == "" {
			continue
		}
		if !strings.ContainsFunc(tc.in, func(r rune) bool { return !NameAllowedRune(r) }) {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			if ValidateName(tc.in) == nil {
				t.Errorf("ValidateName(%q) accepted a value outside the shared charset", tc.in)
			}
			if _, err := ParseServerID(tc.in); err == nil {
				t.Errorf("ParseServerID(%q) accepted a value outside the shared charset", tc.in)
			}
		})
	}
}

// TestSanitizeNameAlwaysValid is the third door's contract. It REPAIRS rather than
// rejects, so its agreement with the grammar cannot be "same verdict" — it is
// "every non-empty output is a value ValidateName accepts". That postcondition is
// what folding it into the one writer bought.
func TestSanitizeNameAlwaysValid(t *testing.T) {
	for _, tc := range nameDoorTable() {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeName(tc.in)
			if got == "" {
				return // nothing salvageable; importName turns this into an error
			}
			if err := ValidateName(got); err != nil {
				t.Errorf("sanitizeName(%q) = %q, which ValidateName rejects: %v", tc.in, got, err)
			}
		})
	}
}

// TestSanitizeNameRespectsSharedBound pins that the repairer's cap comes from
// NameMaxLen, not from a literal beside it, and that truncating at a byte index
// cannot split a rune (every kept rune is single-byte ASCII by construction).
func TestSanitizeNameRespectsSharedBound(t *testing.T) {
	long := strings.Repeat("a", NameMaxLen*3)
	got := sanitizeName(long)
	if len(got) != NameMaxLen {
		t.Fatalf("sanitizeName capped at %d bytes, want NameMaxLen=%d", len(got), NameMaxLen)
	}
	if !utf8.ValidString(got) {
		t.Errorf("sanitizeName produced invalid UTF-8: %q", got)
	}
	// A multi-byte input folds to single-byte separators, so the cap still lands
	// on a rune boundary.
	wide := sanitizeName("a" + strings.Repeat("\u65e5", NameMaxLen*2))
	if !utf8.ValidString(wide) {
		t.Errorf("sanitizeName produced invalid UTF-8 from a multi-byte input: %q", wide)
	}
	if err := ValidateName(wide); err != nil {
		t.Errorf("sanitizeName(multi-byte) = %q, which ValidateName rejects: %v", wide, err)
	}
}

// TestGeneratedIDsPassTheirOwnDoor is why ParseServerID does not borrow the name's
// lead rule: newID mints base32 lowercase (a-z 2-7), so roughly a fifth of all
// generated ids open with a digit and would be refused by ValidateName.
func TestGeneratedIDsPassTheirOwnDoor(t *testing.T) {
	digitLeading := 0
	const draws = 200
	for range draws {
		id := newID()
		if _, err := ParseServerID(string(id)); err != nil {
			t.Fatalf("ParseServerID refused a generated id %q: %v", id, err)
		}
		if id[0] >= '2' && id[0] <= '7' {
			digitLeading++
		}
	}
	// Six of base32's 32 symbols are digits, so ~19% of draws open with one and
	// seeing none in 200 has probability ~1e-18. A zero here means the encoding
	// changed, and the comment above ParseServerID about why it cannot borrow the
	// name's lead rule would have gone stale with it.
	if digitLeading == 0 {
		t.Errorf("no digit-leading id in %d draws: newID's encoding changed, "+
			"so re-check whether ParseServerID still needs its lead-rule exemption", draws)
	}
	// The exemption is load-bearing precisely because ValidateName refuses that
	// shape; a digit-leading value is legal as an id and illegal as a name.
	if err := ValidateName("2abcdefghi"); err == nil {
		t.Error("ValidateName accepted a digit-leading value; the id door's exemption is now moot")
	}
}

// TestValidateNameIsTheRunePredicates is D81's real claim, stated as a PROPERTY over
// generated input rather than as a table of hand-written expectations.
//
// The table above catches a change to a character it happens to list. It cannot
// catch a grammar EXTENSION that updates one writer and not another, which is the
// drift that mattered while a regexp, the rune predicates and a hard-coded grammar
// string each spelled the charset independently. This asserts the only thing that
// makes them one writer: ValidateName's accepted language IS what the predicates
// generate, for every candidate rapid can build.
func TestValidateNameIsTheRunePredicates(t *testing.T) {
	// The in-charset alphabet includes runes that are legal mid-name and illegal as a
	// LEAD (a digit, an underscore, a hyphen), so the lead rule is probed by an
	// otherwise-clean draw.
	inCharset := []rune{'a', 'B', 'z', 'Q', '0', '9', '_', '-'}
	outside := []rune{'.', '/', ':', ' ', '\n', 0x00, 0x7F, '\u00e9', '\u65e5', '\u202e'}

	rapid.Check(t, func(t *rapid.T) {
		// Lengths straddle the bound half the time, so an off-by-N there is reachable
		// rather than a lottery on rapid's bias toward short values.
		n := rapid.OneOf(
			rapid.IntRange(0, 6),
			rapid.IntRange(NameMaxLen-2, NameMaxLen+4),
		).Draw(t, "length")
		runes := rapid.SliceOfN(rapid.SampledFrom(inCharset), n, n).Draw(t, "runes")
		// Inject exactly ONE rune from outside the charset, sometimes. One rather
		// than drawing from a mixed alphabet: a name peppered with illegal runes is
		// refused whatever the charset rule says, so it cannot tell a validator that
		// wrongly accepts `.` from one that refuses everything — the discriminating
		// input is legal-except-for-this-character.
		if len(runes) > 0 && rapid.Bool().Draw(t, "injectOutsider") {
			at := rapid.IntRange(0, len(runes)-1).Draw(t, "at")
			runes[at] = rapid.SampledFrom(outside).Draw(t, "outsider")
		}
		name := string(runes)

		// The predicates, applied here and nowhere else in this function, are the
		// oracle. Deliberately spelled as the rule rather than by calling the
		// production helper, or this would assert a function against itself.
		wantOK := name != "" && len(name) <= NameMaxLen
		if wantOK {
			for i, r := range name {
				allowed := NameAllowedRune(r)
				if i == 0 {
					allowed = NameLeadRune(r)
				}
				if !allowed {
					wantOK = false
					break
				}
			}
		}

		gotOK := ValidateName(name) == nil
		if gotOK != wantOK {
			t.Fatalf("ValidateName(%q) ok=%v, the rune predicates say %v: "+
				"the validator and the predicates are no longer one grammar", name, gotOK, wantOK)
		}
	})
}

// TestNameGrammarSaysWhatTheBoundIs keeps the human-readable half honest. It used to
// carry its own hard-coded 63, so raising NameMaxLen told the user an old rule; the
// message is built from the constant now.
func TestNameGrammarSaysWhatTheBoundIs(t *testing.T) {
	got := nameGrammar()
	if !strings.Contains(got, strconv.Itoa(NameMaxLen)) {
		t.Errorf("nameGrammar() = %q, which does not state NameMaxLen=%d", got, NameMaxLen)
	}
	err := ValidateName("bad name")
	if err == nil {
		t.Fatal("ValidateName accepted a spaced name")
	}
	if !strings.Contains(err.Error(), got) {
		t.Errorf("the rejection %q does not tell the user the grammar %q", err, got)
	}
}

// FuzzValidateNameMatchesPredicates is the byte-level companion to the property
// above: rapid draws from a rune alphabet, this reaches invalid UTF-8 and every
// other byte sequence an HTTP body can carry.
func FuzzValidateNameMatchesPredicates(f *testing.F) {
	for _, tc := range nameDoorTable() {
		f.Add(tc.in)
	}
	f.Add("\xff\xfe")
	f.Add("a\xc3")
	f.Fuzz(func(t *testing.T, name string) {
		wantOK := name != "" && len(name) <= NameMaxLen
		if wantOK {
			for i, r := range name {
				allowed := NameAllowedRune(r)
				if i == 0 {
					allowed = NameLeadRune(r)
				}
				if !allowed {
					wantOK = false
					break
				}
			}
		}
		if gotOK := ValidateName(name) == nil; gotOK != wantOK {
			t.Fatalf("ValidateName(%q) ok=%v, the rune predicates say %v", name, gotOK, wantOK)
		}
	})
}

// FuzzSanitizeNameProducesValidName is the invariant fuzz target the package's
// other parsers already carry: whatever bytes a README hands the paste path,
// sanitizeName's output is either empty or something the one grammar accepts.
func FuzzSanitizeNameProducesValidName(f *testing.F) {
	for _, tc := range nameDoorTable() {
		f.Add(tc.in)
	}
	f.Add("@modelcontextprotocol/server-everything")
	f.Add("---")
	f.Add("\x00\x01\x02")
	f.Fuzz(func(t *testing.T, raw string) {
		got := sanitizeName(raw)
		if got == "" {
			return
		}
		if err := ValidateName(got); err != nil {
			t.Fatalf("sanitizeName(%q) = %q, rejected by ValidateName: %v", raw, got, err)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("sanitizeName(%q) = %q, not valid UTF-8", raw, got)
		}
	})
}
