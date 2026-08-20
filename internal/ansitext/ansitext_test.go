package ansitext

import (
	"strconv"
	"strings"
	"testing"
)

// styleAt returns the style covering offset off, or the zero style when no
// span covers it. Spans never overlap, so the first match is the answer.
func styleAt(spans []Span, off int) (Span, bool) {
	for _, s := range spans {
		if off >= s.Start && off < s.End {
			return s, true
		}
	}
	return Span{}, false
}

func TestParse_PlainTextProducesNoSpans(t *testing.T) {
	const in = "hello world\nsecond line\n"
	text, spans := Parse(in)
	if text != in {
		t.Errorf("text = %q, want %q", text, in)
	}
	if len(spans) != 0 {
		t.Errorf("got %d spans for unstyled text, want none", len(spans))
	}
}

func TestParse_StripsSequencesFromTheText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The shapes measured in real tool output: gitleaks' zerolog console
		// writer and hadolint both colour unconditionally.
		{name: "sgr colour", in: "\x1b[90m1:47AM\x1b[0m \x1b[32mINF\x1b[0m ok", want: "1:47AM INF ok"},
		{name: "bright fg", in: "\x1b[92minfo\x1b[0m", want: "info"},
		{name: "reset shorthand", in: "\x1b[1mbold\x1b[mplain", want: "boldplain"},
		// Grid operations are dropped, not interpreted: this is a pipe, so
		// there is no cursor to move and no screen to erase.
		{name: "cursor move", in: "a\x1b[2Ab", want: "ab"},
		{name: "erase line", in: "a\x1b[2Kb", want: "ab"},
		{name: "erase display", in: "a\x1b[3Jb", want: "ab"},
		{name: "scroll region", in: "a\x1b[1;24rb", want: "ab"},
		{name: "private mode", in: "a\x1b[?25lb", want: "ab"},
		// Other escape families.
		{name: "osc bel terminated", in: "a\x1b]0;title\x07b", want: "ab"},
		{name: "osc st terminated", in: "a\x1b]8;;http://x\x1b\\b", want: "ab"},
		{name: "charset designation", in: "a\x1b(Bb", want: "ab"},
		{name: "dcs", in: "a\x1bPq~~\x1b\\b", want: "ab"},
		{name: "two byte escape", in: "a\x1bcb", want: "ab"},
		{name: "lone escape at end becomes U+FFFD", in: "ab\x1b", want: "ab\ufffd"},
		// The three-byte forms whose FINAL byte leaks into the text when only
		// two bytes are consumed. `ESC % G` (select UTF-8) is the plausible
		// one from a pipe; each of these left a stray capital letter behind.
		{name: "utf8 charset selection", in: "a\x1b%Gb", want: "ab"},
		{name: "96 char set designation", in: "a\x1b-Ab", want: "ab"},
		{name: "line attribute", in: "a\x1b#8b", want: "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, _ := Parse(tc.in)
			if text != tc.want {
				t.Errorf("text = %q, want %q", text, tc.want)
			}
			if strings.ContainsRune(text, 0x1b) {
				t.Errorf("text still contains ESC: %q", text)
			}
		})
	}
}

func TestParse_SpansAddressTheRightRanges(t *testing.T) {
	// "plain" unstyled, "red" in red, "tail" unstyled.
	text, spans := Parse("plain\x1b[31mred\x1b[0mtail")
	if text != "plainredtail" {
		t.Fatalf("text = %q", text)
	}
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1: %+v", len(spans), spans)
	}
	got := spans[0]
	if got.Start != 5 || got.End != 8 {
		t.Errorf("span range = [%d,%d), want [5,8) covering %q", got.Start, got.End, "red")
	}
	if text[got.Start:got.End] != "red" {
		t.Errorf("span covers %q, want %q", text[got.Start:got.End], "red")
	}
	if got.FG != 1 {
		t.Errorf("FG = %d, want 1 (red)", got.FG)
	}
	if _, ok := styleAt(spans, 0); ok {
		t.Error("offset 0 is styled, want the leading text unstyled")
	}
	if _, ok := styleAt(spans, 8); ok {
		t.Error("offset 8 is styled, want the trailing text unstyled")
	}
}

// Every SGR attribute is covered. `ESC[7m` appears in real measured output and
// rendered unstyled before the parse moved here.
func TestApplySGR_AllAttributes(t *testing.T) {
	cases := []struct {
		name string
		seq  string
		want uint16
	}{
		{name: "bold", seq: "1", want: AttrBold},
		{name: "dim", seq: "2", want: AttrDim},
		{name: "italic", seq: "3", want: AttrItalic},
		{name: "underline", seq: "4", want: AttrUnderline},
		{name: "blink", seq: "5", want: AttrBlink},
		{name: "rapid blink", seq: "6", want: AttrBlink},
		{name: "inverse", seq: "7", want: AttrInverse},
		{name: "hidden", seq: "8", want: AttrHidden},
		{name: "strike", seq: "9", want: AttrStrike},
		{name: "double underline", seq: "21", want: AttrDoubleUnderline},
		{name: "overline", seq: "53", want: AttrOverline},
		{name: "bold and italic", seq: "1;3", want: AttrBold | AttrItalic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spans := Parse("\x1b[" + tc.seq + "mx")
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want 1", len(spans))
			}
			if spans[0].Attrs != tc.want {
				t.Errorf("attrs = %#b, want %#b", spans[0].Attrs, tc.want)
			}
		})
	}
}

// The attribute bit VALUES are a cross-language contract, not an internal
// detail: web-terminal-engine's vt.WireRun.A uses these exact bits, and the
// client's one constant table (output-render.ts) serves both renderers. A
// reordering of the iota block would keep every other test in this file green
// while silently repainting bold as italic in the browser.
func TestAttrBits_MatchTheWireRunContract(t *testing.T) {
	want := map[string]uint16{
		"bold": 1, "italic": 2, "underline": 4, "inverse": 8, "strike": 16,
		"dim": 32, "hidden": 64, "blink": 128, "overline": 256, "doubleUnderline": 512,
	}
	got := map[string]uint16{
		"bold": AttrBold, "italic": AttrItalic, "underline": AttrUnderline,
		"inverse": AttrInverse, "strike": AttrStrike, "dim": AttrDim,
		"hidden": AttrHidden, "blink": AttrBlink, "overline": AttrOverline,
		"doubleUnderline": AttrDoubleUnderline,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("Attr%s = %d, want %d (vt.WireRun.A contract)", name, got[name], w)
		}
	}
	// Bit 1024 is WireRun's AttrAutolink. Nothing here may claim it.
	var all uint16
	for _, v := range got {
		all |= v
	}
	if all&1024 != 0 {
		t.Errorf("attribute bits = %#b, want none at 1024 (vt.AttrAutolink)", all)
	}
}

func TestApplySGR_AttributeOffSwitches(t *testing.T) {
	cases := []struct {
		name string
		seq  string
		want uint16
	}{
		{name: "22 clears bold and dim", seq: "1;2;22", want: 0},
		{name: "23 clears italic", seq: "3;23", want: 0},
		{name: "24 clears both underlines", seq: "4;21;24", want: 0},
		{name: "25 clears blink", seq: "5;25", want: 0},
		{name: "27 clears inverse", seq: "7;27", want: 0},
		{name: "28 clears hidden", seq: "8;28", want: 0},
		{name: "29 clears strike", seq: "9;29", want: 0},
		{name: "55 clears overline", seq: "53;55", want: 0},
		{name: "22 leaves italic alone", seq: "1;3;22", want: AttrItalic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spans := Parse("\x1b[" + tc.seq + "mx")
			var got uint16
			if len(spans) == 1 {
				got = spans[0].Attrs
			}
			if got != tc.want {
				t.Errorf("attrs = %#b, want %#b", got, tc.want)
			}
		})
	}
}

func TestApplySGR_Colours(t *testing.T) {
	cases := []struct {
		name   string
		seq    string
		wantFG int32
		wantBG int32
	}{
		{name: "basic fg", seq: "31", wantFG: 1, wantBG: ColorDefault},
		{name: "basic bg", seq: "41", wantFG: ColorDefault, wantBG: 1},
		{name: "bright fg maps above 8", seq: "91", wantFG: 9, wantBG: ColorDefault},
		{name: "bright bg maps above 8", seq: "101", wantFG: ColorDefault, wantBG: 9},
		{name: "256 palette fg", seq: "38;5;208", wantFG: 208, wantBG: ColorDefault},
		{name: "256 palette bg", seq: "48;5;17", wantFG: ColorDefault, wantBG: 17},
		{name: "truecolour fg", seq: "38;2;10;20;30", wantFG: RGB(10, 20, 30), wantBG: ColorDefault},
		{name: "truecolour bg", seq: "48;2;1;2;3", wantFG: ColorDefault, wantBG: RGB(1, 2, 3)},
		{name: "39 resets fg only", seq: "31;41;39", wantFG: ColorDefault, wantBG: 1},
		{name: "49 resets bg only", seq: "31;41;49", wantFG: 1, wantBG: ColorDefault},
		{name: "fg and bg together", seq: "32;44", wantFG: 2, wantBG: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spans := Parse("\x1b[" + tc.seq + "mx")
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want 1", len(spans))
			}
			if spans[0].FG != tc.wantFG {
				t.Errorf("FG = %d, want %d", spans[0].FG, tc.wantFG)
			}
			if spans[0].BG != tc.wantBG {
				t.Errorf("BG = %d, want %d", spans[0].BG, tc.wantBG)
			}
		})
	}
}

func TestApplySGR_MalformedExtendedColourDoesNotCorruptLaterParams(t *testing.T) {
	// `38;5` with no index is truncated. The parser must not swallow the
	// following bold, or a malformed colour would eat unrelated styling.
	_, spans := Parse("\x1b[38;5m\x1b[1mx")
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Attrs&AttrBold == 0 {
		t.Errorf("attrs = %#b, want bold set", spans[0].Attrs)
	}
}

// A parameter this parser cannot honour must be IGNORED, never folded in as 0 —
// 0 is a full reset, so the "unknown means zero" reading turns every one of
// these into a silent style wipe. Each case here is a real emitter.
func TestApplySGR_UnhonourableParametersDoNotReset(t *testing.T) {
	cases := []struct {
		name      string
		seq       string
		wantAttrs uint16
		wantFG    int32
		reason    string
	}{{
		name: "colon subparameter underline", seq: "\x1b[4:3mx",
		wantAttrs: AttrUnderline, wantFG: ColorDefault,
		reason: "gcc and clang emit ESC[4:3m for a curly diagnostic underline;" +
			" read whole the field is non-numeric, and non-numeric read as 0 resets",
	}, {
		name: "colon subparameter keeps earlier styling", seq: "\x1b[31m\x1b[4:3mx",
		wantAttrs: AttrUnderline, wantFG: 1,
		reason: "the red opened by the previous sequence must survive the underline",
	}, {
		name: "private parameter marker is ignored whole", seq: "\x1b[31m\x1b[>4;2mx",
		wantAttrs: 0, wantFG: 1,
		reason: "xterm's modifyOtherKeys has an SGR final byte but is not SGR;" +
			" terminals ignore it, and reading `>4` as 0 wiped the red",
	}, {
		name: "intermediate byte is skipped not zeroed", seq: "\x1b[1m\x1b[2 mx",
		wantAttrs: AttrBold, wantFG: ColorDefault,
		reason: "an intermediate byte means the sequence is not SGR at all",
	}, {
		name: "underline colour is dropped without resetting", seq: "\x1b[1m\x1b[58:2::1:2:3mx",
		wantAttrs: AttrBold, wantFG: ColorDefault,
		reason: "a Span carries no underline colour, so 58 is dropped — not read as a reset",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, spans := Parse(tc.seq)
			var gotAttrs uint16
			gotFG := ColorDefault
			if len(spans) > 0 {
				last := spans[len(spans)-1]
				gotAttrs, gotFG = last.Attrs, last.FG
			}
			if gotAttrs != tc.wantAttrs || gotFG != tc.wantFG {
				t.Errorf("attrs=%#b fg=%d, want attrs=%#b fg=%d (%s)",
					gotAttrs, gotFG, tc.wantAttrs, tc.wantFG, tc.reason)
			}
		})
	}
}

func TestParse_ResetClosesTheSpan(t *testing.T) {
	text, spans := Parse("\x1b[31mred\x1b[0mplain\x1b[32mgreen\x1b[0m")
	if text != "redplaingreen" {
		t.Fatalf("text = %q", text)
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: %+v", len(spans), spans)
	}
	if text[spans[0].Start:spans[0].End] != "red" {
		t.Errorf("first span covers %q, want %q", text[spans[0].Start:spans[0].End], "red")
	}
	if text[spans[1].Start:spans[1].End] != "green" {
		t.Errorf("second span covers %q, want %q", text[spans[1].Start:spans[1].End], "green")
	}
}

// Streaming: a chunk boundary anywhere inside an escape sequence must not leak
// escape bytes into the text, and an open colour must still apply afterwards.
// This is what the pump does on every 4 KB read.
func TestParser_SplitSequenceAcrossWrites(t *testing.T) {
	const full = "a\x1b[31mred\x1b[0mb"
	for cut := 1; cut < len(full); cut++ {
		t.Run("cut_"+strconv.Itoa(cut), func(t *testing.T) {
			p := NewParser()
			text1, spans1 := p.Write(full[:cut])
			text2, spans2 := p.Write(full[cut:])
			tailText, tailSpans := p.Flush()

			text := text1 + text2 + tailText
			spans := append(append(append([]Span{}, spans1...), spans2...), tailSpans...)

			if text != "aredb" {
				t.Fatalf("cut at %d: text = %q, want %q", cut, text, "aredb")
			}
			// "red" occupies [1,4) of the reassembled text however it was cut.
			covered := 0
			for _, s := range spans {
				for off := s.Start; off < s.End; off++ {
					if off >= 1 && off < 4 {
						covered++
					}
					if off == 0 || off == 4 {
						t.Errorf("cut at %d: offset %d styled, want unstyled", cut, off)
					}
				}
			}
			if covered != 3 {
				t.Errorf("cut at %d: %d of 3 styled bytes covered, spans=%+v", cut, covered, spans)
			}
		})
	}
}

// Open style carries across writes: a colour set in one chunk styles text that
// arrives in the next, which is why each stream needs its own Parser.
func TestParser_OpenStyleCarriesAcrossWrites(t *testing.T) {
	p := NewParser()
	t1, s1 := p.Write("\x1b[31mfirst")
	t2, s2 := p.Write("second\x1b[0m")

	if t1 != "first" || t2 != "second" {
		t.Fatalf("text = %q + %q, want %q + %q", t1, t2, "first", "second")
	}
	if len(s1) != 1 || s1[0].FG != 1 {
		t.Fatalf("first write spans = %+v, want one red span", s1)
	}
	if len(s2) != 1 || s2[0].FG != 1 {
		t.Fatalf("second write spans = %+v, want the colour to carry", s2)
	}
	// Offsets are absolute across the parser's life, so the second write's
	// span starts where the first left off.
	if s2[0].Start != len(t1) {
		t.Errorf("second span starts at %d, want %d (absolute offsets)", s2[0].Start, len(t1))
	}
}

// Offset is the parser's own count of emitted UTF-16 units, and the runtime reports
// it on the wire as the base of the chunk it is broadcasting. The property that
// makes that correct: Offset read BEFORE a Write equals the absolute Start the
// first span of that Write will carry. A second counter kept in step by hand is
// exactly what this accessor exists to remove, so this test is the contract.
func TestParser_OffsetIsTheBaseOfTheNextWrite(t *testing.T) {
	p := NewParser()
	if got := p.Offset(); got != 0 {
		t.Fatalf("fresh parser Offset = %d, want 0", got)
	}
	// A lead-in with a surrogate pair, so a byte-based counter would disagree.
	lead, _ := p.Write("ok\U0001F600")
	wantAfterLead := 4 // "ok" + 2 units for the emoji
	if got := p.Offset(); got != wantAfterLead {
		t.Fatalf("Offset after %q = %d, want %d", lead, got, wantAfterLead)
	}
	base := p.Offset()
	_, spans := p.Write("\x1b[31mred\x1b[0m")
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Start != base {
		t.Errorf("span starts at %d, want the pre-write Offset %d", spans[0].Start, base)
	}
	// And the flush path reports the same way.
	_, _ = p.Write("tail\x1b[3")
	flushBase := p.Offset()
	tail, _ := p.Flush()
	if flushBase+utf16Len(tail) != p.Offset() {
		t.Errorf("Offset advanced by %d over a %d-unit flush",
			p.Offset()-flushBase, utf16Len(tail))
	}
}

// An escape that never terminates must not swallow output forever.
func TestParser_UnterminatedSequenceIsReleasedAsText(t *testing.T) {
	p := NewParser()
	long := "\x1b[" + strings.Repeat("1;", maxPendingBytes)
	text, _ := p.Write(long)
	if text == "" {
		t.Error("an over-long unterminated sequence held every byte back, want it released as text")
	}
	if strings.ContainsRune(text, 0x1b) {
		t.Errorf("released text carries a raw ESC: %q", text[:min(len(text), 32)])
	}
}

func TestParser_FlushReleasesHeldBytes(t *testing.T) {
	p := NewParser()
	text, _ := p.Write("ok\x1b[3")
	if text != "ok" {
		t.Fatalf("text = %q, want %q (the partial sequence is held)", text, "ok")
	}
	tail, _ := p.Flush()
	if tail != "\ufffd[3" {
		t.Errorf("Flush = %q, want the held bytes back with ESC neutralized", tail)
	}
}

func TestRGB_RoundTripsAndIsDistinctFromPaletteIndices(t *testing.T) {
	c := RGB(10, 20, 30)
	if c < rgbFlag {
		t.Errorf("RGB(...) = %d, want it above rgbFlag (%d)", c, rgbFlag)
	}
	// The two encodings share one int32, so a palette index must never land in
	// the truecolour range or a 256-colour span would render as an RGB triple.
	for i := range 256 {
		if idx := int32(i); idx >= rgbFlag {
			t.Fatalf("palette index %d collides with the RGB range", idx)
		}
	}
	if got := (c >> 16) & 0xff; got != 10 {
		t.Errorf("red component = %d, want 10", got)
	}
	if got := (c >> 8) & 0xff; got != 20 {
		t.Errorf("green component = %d, want 20", got)
	}
	if got := c & 0xff; got != 30 {
		t.Errorf("blue component = %d, want 30", got)
	}
}

// Agent command output is untrusted: the agent chooses the command and the
// command chooses the bytes. Four invariants hold for every input.
func FuzzParse(f *testing.F) {
	f.Add("plain text")
	f.Add("\x1b[31mred\x1b[0m")
	f.Add("\x1b[38;2;1;2;3mtrue\x1b[0m")
	f.Add("\x1b[38;5;208m256\x1b[0m")
	f.Add("\x1b[")
	f.Add("\x1b")
	f.Add("\x1b]8;;http://example.com\x07link\x1b]8;;\x07")
	f.Add("\x1b[999999999999999999999m")
	f.Add("\x1b[1;;;;;;mx")
	f.Add("a\x1b[2Kb\x1b[3Jc")
	f.Add("\x1b[7minverse\x1b[27m")
	f.Add("\x1bPq\x1b\\")
	f.Add("\x1b[4:3munderline\x1b[m")
	f.Add("\x1b[>4;2mx")
	f.Add("a\x1b%Gb")
	// The fuzzer's own finds, kept as seeds because each cost a real defect.
	// A long string-terminated sequence, where the pending bound and the
	// chunked/one-shot agreement meet:
	f.Add("\x1bX" + strings.Repeat("0", 63) + "\x07")
	// An escape sequence sitting between a rune's lead byte and its
	// continuation bytes, which made piecewise UTF-16 offsets address past the
	// end of the string the client holds:
	f.Add("\xe6\x1b[1m\xbd\xbd")
	// A run of invalid bytes, which strings.ToValidUTF8 collapses into ONE
	// replacement and so breaks the additivity offsets depend on:
	f.Add("\xbd\xbd\xbd")

	f.Fuzz(func(t *testing.T, in string) {
		text, spans := Parse(in)

		// 1. The text never grows, counted in the unit offsets use. Bytes would
		//    be the wrong measure: an invalid byte is replaced by U+FFFD, which
		//    is three bytes but one code unit. Every input byte yields at most
		//    one unit, and escapes are removed, so this bounds the result.
		if utf16Len(text) > len(in) {
			t.Fatalf("text grew: %d UTF-16 units out of %d input bytes", utf16Len(text), len(in))
		}

		// 2. Every span addresses a real range of the text, in order, without
		//    overlapping. Bounds are checked in UTF-16 space because that is
		//    the offset unit; a bad offset would slice garbage in the client.
		limit := utf16Len(text)
		prevEnd := 0
		for i, s := range spans {
			if s.Start < 0 || s.End > limit || s.Start >= s.End {
				t.Fatalf("span %d = [%d,%d) is out of range for %d UTF-16 units of text", i, s.Start, s.End, limit)
			}
			if s.Start < prevEnd {
				t.Fatalf("span %d = [%d,%d) overlaps the previous span ending at %d", i, s.Start, s.End, prevEnd)
			}
			prevEnd = s.End
		}

		// 3. A span is never emitted for default styling, or unstyled output
		//    would carry spans that paint nothing.
		for i, s := range spans {
			if s.FG == ColorDefault && s.BG == ColorDefault && s.Attrs == 0 {
				t.Fatalf("span %d styles nothing: %+v", i, s)
			}
		}

		// 4. The plain text never carries an escape byte. This is the invariant
		//    that lets the parser stand in for sanitize.StripANSI: the text is
		//    persisted to a JSON chat file and re-served, so a surviving ESC
		//    would be a residual escape in stored output.
		if strings.ContainsRune(text, 0x1b) {
			t.Fatalf("plain text still contains ESC: %q", text)
		}

		// 5. Streaming agrees with one-shot. The pump feeds chunks, so a split
		//    must produce the same text as parsing the whole string. Scoped to
		//    inputs within maxPendingBytes: past that bound the parser releases
		//    an unterminated run as text rather than holding it, and a
		//    bounded-memory streaming parser cannot agree with an unbounded
		//    one-shot parser there (the fuzzer found exactly that case with a
		//    4 KB `ESC X ... BEL` string sequence). Invariants 1-4 still hold
		//    for every input.
		if len(in) > maxPendingBytes {
			return
		}
		for cut := 0; cut <= len(in); cut++ {
			p := NewParser()
			a, _ := p.Write(in[:cut])
			b, _ := p.Write(in[cut:])
			tail, _ := p.Flush()
			if a+b+tail != text {
				t.Fatalf("split at %d gave %q, one-shot gave %q", cut, a+b+tail, text)
			}
			// 6. Offset agrees with the text actually emitted, at every split.
			//    The runtime reports Offset as a chunk's base, so a drift here
			//    would rebase every live span onto the wrong character.
			if got, want := p.Offset(), utf16Len(text); got != want {
				t.Fatalf("split at %d: Offset = %d, want %d units of emitted text", cut, got, want)
			}
			if cut > 64 {
				break // bound the loop; the interesting cuts are early
			}
		}
	})
}

// Span offsets are UTF-16 code units because the consumer indexes the text with
// JavaScript string offsets. A byte offset would point into the middle of a
// character the moment a command printed a box-drawing glyph, an accented name
// or an emoji, and the client would slice garbage.
func TestParse_OffsetsAreUTF16CodeUnits(t *testing.T) {
	cases := []struct {
		name string
		lead string // unstyled text before the styled word
		want int    // expected UTF-16 offset of the styled word
	}{
		{name: "ascii", lead: "abc", want: 3},
		// U+00E9 is 2 bytes in UTF-8, 1 UTF-16 unit.
		{name: "latin1 supplement", lead: "caf\u00e9", want: 4},
		// U+2502 (box drawing) is 3 bytes, 1 UTF-16 unit.
		{name: "box drawing", lead: "\u2502\u2502", want: 2},
		// U+1F600 is 4 bytes and a SURROGATE PAIR: 2 UTF-16 units.
		{name: "emoji is a surrogate pair", lead: "\U0001F600", want: 2},
		{name: "mixed", lead: "a\u00e9\u2502\U0001F600", want: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, spans := Parse(tc.lead + "\x1b[31mred\x1b[0m")
			if len(spans) != 1 {
				t.Fatalf("got %d spans, want 1", len(spans))
			}
			if spans[0].Start != tc.want {
				t.Errorf("span starts at %d, want %d (UTF-16 units in %q)", spans[0].Start, tc.want, tc.lead)
			}
			if spans[0].End != tc.want+3 {
				t.Errorf("span ends at %d, want %d", spans[0].End, tc.want+3)
			}
			// The invariant the client relies on: slicing the text by the
			// span's offsets in UTF-16 space yields exactly the styled word.
			if got := utf16Slice(text, spans[0].Start, spans[0].End); got != "red" {
				t.Errorf("utf16 slice = %q, want %q", got, "red")
			}
		})
	}
}

// utf16Slice indexes s the way a browser would, so a test can assert what the
// client will actually paint.
func utf16Slice(s string, start, end int) string {
	units := 0
	var out []rune
	for _, r := range s {
		w := 1
		if r > 0xffff {
			w = 2
		}
		if units >= start && units+w <= end {
			out = append(out, r)
		}
		units += w
	}
	return string(out)
}

func TestUTF16Len(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{in: "", want: 0},
		{in: "abc", want: 3},
		{in: "caf\u00e9", want: 4},
		{in: "\u2502", want: 1},
		{in: "\U0001F600", want: 2},
		{in: "a\U0001F600b", want: 4},
	}
	for _, tc := range cases {
		if got := utf16Len(tc.in); got != tc.want {
			t.Errorf("utf16Len(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
