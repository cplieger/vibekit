// Package ansitext separates ANSI escape sequences from the text they style.
//
// It exists because agent command output is rendered in the CHAT TRANSCRIPT, in
// a tool card, and a transcript wants three things at once that a browser-side
// ANSI-to-HTML converter cannot give: the text has to stay searchable and
// exportable as text, it has to pass through redaction as text, and it must
// reach the DOM without anybody building an HTML string out of agent-controlled
// bytes. So the parse happens once here, on the server, and produces two
// values: the plain text, and style SPANS that address ranges of it.
//
// Spans carry OFFSETS rather than copies, so styling a 64 KB command costs a
// handful of small structs instead of a second copy of the output, and the
// common case (measured: 99.75% of real tool outputs contain no escape at all)
// costs an empty slice.
//
// # Scope
//
// This is a LINEAR parser, not a terminal. Agent terminals are pipes with no
// PTY (see hub/agent_terminal.go), so the stream has no cursor addressing, no
// carriage-return redraw and no alternate screen, and modelling a grid would
// buy nothing while forcing a fixed column width onto a card whose width is
// whatever the window is. Sequences that only make sense against a grid are
// therefore DROPPED rather than interpreted: cursor movement, erases, scroll
// regions, and every other non-SGR CSI final byte.
//
// SGR support is a strict superset of the ansi_up library this replaced: all
// twelve attributes including inverse, strikethrough, blink, conceal, overline
// and double-underline (ansi_up silently ignored five of those, so real
// `ESC[7m` output rendered unstyled), plus the 16 basic colours, the 8 bright
// aliases, the 256-colour palette and 24-bit truecolour.
//
// The attribute bitflags and the colour encoding deliberately match
// web-terminal-engine's vt.WireRun, so the two rendering paths in this app
// speak one attribute language rather than two.
package ansitext

import (
	"strings"
	"unicode/utf8"
)

// Style attribute bits. Values match web-terminal-engine's WireRun.a so the
// terminal renderer and the transcript renderer share one vocabulary.
const (
	AttrBold uint16 = 1 << iota
	AttrItalic
	AttrUnderline
	AttrInverse
	AttrStrike
	AttrDim
	AttrHidden
	AttrBlink
	AttrOverline
	AttrDoubleUnderline
)

// ColorDefault marks "no colour set", distinct from black (index 0).
const ColorDefault int32 = -1

// Span styles the half-open range [Start,End) of the plain text.
//
// Offsets are UTF-16 CODE UNITS, not bytes, because the only consumer indexes
// the text with JavaScript string offsets. For ASCII the two are identical; a
// byte offset would silently point into the middle of a character the moment a
// command printed a box-drawing glyph or an accented name.
//
// A span is emitted only when at least one of its fields is non-default, so
// unstyled output produces no spans at all.
type Span struct {
	// Start is the inclusive UTF-16 offset into the plain text.
	Start int `json:"start"`
	// End is the exclusive UTF-16 offset into the plain text.
	End int `json:"end"`
	// FG is the foreground colour: ColorDefault, a 0-255 palette index, or
	// 0x1000000|RGB for 24-bit colour.
	FG int32 `json:"fg"`
	// BG is the background colour, encoded like FG.
	BG int32 `json:"bg"`
	// Attrs is the OR of the Attr* bits.
	Attrs uint16 `json:"attrs"`
}

// utf16Len counts the UTF-16 code units a string occupies: one per rune below
// U+10000, two for anything above (a surrogate pair). An invalid byte decodes
// to U+FFFD, which is one unit, matching what a browser receives after the
// JSON round trip.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

// neutralizeESC replaces every ESC byte with U+FFFD.
//
// Applied on the two paths that release bytes the parser could not parse as a
// sequence: an unterminated run past maxPendingBytes, and a stream that ended
// mid-sequence. Without it those paths would put a raw ESC into the plain text,
// and that text is persisted to a JSON chat file and re-served.
//
// This is what lets the parser REPLACE api.StripANSI rather than run after it.
// The caller sanitizes hidden Unicode first (so nothing can hide a sequence
// behind a zero-width character), the parser then consumes SGR and drops every
// other escape family StripANSI matched — CSI, OSC, charset designation,
// DCS/SOS/PM/APC and the two-byte forms — and this covers the remainder. The
// result is an escape-free output text by construction, which FuzzParse asserts
// directly. One U+FFFD per ESC keeps UTF-16 length additive over fragments.
func neutralizeESC(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	return strings.ReplaceAll(s, "\x1b", "\uFFFD")
}

// validUTF8PerByte replaces each invalid byte with U+FFFD individually, so the
// UTF-16 length of two fragments equals the length of their concatenation.
// strings.ToValidUTF8 cannot be used for this: it collapses a RUN of invalid
// bytes into a single replacement, which is friendlier output and breaks
// additivity. Returns s unchanged when it is already valid, so the common path
// allocates nothing.
func validUTF8PerByte(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			sb.WriteRune(utf8.RuneError)
			i++
			continue
		}
		sb.WriteString(s[i : i+size])
		i += size
	}
	return sb.String()
}

// incompleteRuneTail returns how many trailing bytes of b begin a multi-byte
// rune whose continuation bytes have not arrived. 0 when b ends on a complete
// rune, is empty, or ends in a byte that can never complete one. At most 3
// bytes are ever held.
func incompleteRuneTail(b string) int {
	for back := 1; back <= utf8.UTFMax-1 && back <= len(b); back++ {
		c := b[len(b)-back]
		if utf8.RuneStart(c) {
			if size := expectedRuneLen(c); size > back {
				return back
			}
			return 0
		}
	}
	return 0
}

// expectedRuneLen returns how many bytes the rune starting with lead occupies,
// or 0 when lead does not start a valid sequence.
func expectedRuneLen(lead byte) int {
	switch {
	case lead < 0x80:
		return 1
	case lead&0xe0 == 0xc0:
		return 2
	case lead&0xf0 == 0xe0:
		return 3
	case lead&0xf8 == 0xf0:
		return 4
	default:
		return 0
	}
}

// rgbFlag marks an FG/BG value as packed 24-bit colour rather than a palette
// index. 0x1000000 is one past the largest 24-bit value, so the two spaces
// cannot collide.
const rgbFlag int32 = 0x1000000

// RGB packs three components into an FG/BG value.
func RGB(r, g, b uint8) int32 {
	return rgbFlag | int32(r)<<16 | int32(g)<<8 | int32(b)
}

// style is the parser's current SGR state.
type style struct {
	fg    int32
	bg    int32
	attrs uint16
}

func (s style) isDefault() bool {
	return s.fg == ColorDefault && s.bg == ColorDefault && s.attrs == 0
}

// Parser holds the SGR state and the incomplete-sequence remainder between
// Write calls, so a chunk boundary landing mid-escape does not leak escape
// bytes into the text and a colour opened in one chunk still applies in the
// next. One Parser per logical output stream; never share one between two
// concurrent streams or their styles bleed together.
//
// A Parser is not safe for concurrent use.
type Parser struct {
	// pending holds a trailing byte run that starts an escape sequence but is
	// not yet complete. It is always short: a sequence longer than
	// maxPendingLen is treated as garbage and released as text.
	pending []byte
	cur     style
	// offset is the total plain-text length emitted so far, so spans from
	// successive Write calls address one continuous document.
	offset int
}

// maxPendingBytes bounds how many bytes an unterminated escape sequence may
// hold back, so a command that emits `ESC ]` and then gigabytes cannot grow
// this buffer without limit. It is one read chunk (the pump reads 4 KB at a
// time), which is far above any legitimate sequence: a CSI is a handful of
// bytes, and even an OSC 8 hyperlink carrying a long URI is hundreds.
//
// Past the bound the run is released as literal text rather than held, because
// a 4 KB "escape sequence" is not one. That is also the one input class where
// chunked parsing cannot agree with one-shot parsing, and the disagreement is
// inherent rather than a bug: a bounded-memory streaming parser has to decide
// before it can know, while a one-shot parser sees the terminator. FuzzParse
// scopes its streaming-agreement invariant to inputs within the bound for
// exactly this reason.
const maxPendingBytes = 4096

// NewParser returns a Parser with default style and no pending bytes.
func NewParser() *Parser { return &Parser{cur: style{fg: ColorDefault, bg: ColorDefault}} }

// Write consumes one chunk and returns its plain text plus the spans styling
// it. Span offsets are absolute across the Parser's lifetime, so a caller
// appending text and spans to one accumulating document needs no adjustment.
//
// Text may be empty (a chunk holding only escapes), and spans may be empty
// (unstyled text), independently.
func (p *Parser) Write(chunk string) (text string, spans []Span) {
	buf := chunk
	if len(p.pending) > 0 {
		buf = string(p.pending) + chunk
		p.pending = p.pending[:0]
	}
	w := &writer{p: p, openStart: p.offset, openStyle: p.cur}
	w.sb.Grow(len(buf))
	w.scan(buf)

	text = w.sb.String()
	p.offset += w.emitted
	w.closeRun(p.offset)
	return text, w.spans
}

// writer carries one Write call's accumulating state: the text built so far, how
// many UTF-16 units that is, and the open style run. Extracted from Write so the
// scan loop reads as a sequence of decisions rather than a knot of index
// arithmetic; the previous inline form tripped gocognit and carried two
// ineffectual `i = len(buf)` assignments whose only job was to reach a break.
type writer struct {
	p         *Parser
	sb        strings.Builder
	spans     []Span
	emitted   int
	openStart int
	openStyle style
}

// write appends text, coercing it to valid UTF-8 first.
//
// It is the OFFSETS that require the coercion rather than tidiness. Two ways a
// rune arrives broken here, and both would otherwise make offsets disagree with
// the string the client holds. An escape sequence can sit between a rune's lead
// byte and its continuation bytes ("\xe6" + ESC[1m + "\xbd\xbd"), and a chunk
// boundary can fall mid-rune. Either way the fragments recombine in the
// concatenated result, so a piecewise count addresses past the end.
//
// Replacement is PER BYTE, not per run: strings.ToValidUTF8 collapses a run of
// invalid bytes into one U+FFFD, which makes the length of two fragments differ
// from the length of their concatenation and breaks the one property every span
// offset rests on. (Both found by FuzzParse.)
func (w *writer) write(s string) {
	valid := validUTF8PerByte(s)
	w.sb.WriteString(valid)
	w.emitted += utf16Len(valid)
}

// closeRun emits the open style run, ending at the given absolute offset.
func (w *writer) closeRun(absEnd int) {
	if absEnd > w.openStart && !w.openStyle.isDefault() {
		w.spans = append(w.spans, Span{
			Start: w.openStart, End: absEnd,
			FG: w.openStyle.fg, BG: w.openStyle.bg, Attrs: w.openStyle.attrs,
		})
	}
}

// scan consumes buf, writing its text and recording style runs.
func (w *writer) scan(buf string) {
	for i := 0; i < len(buf); {
		e := strings.IndexByte(buf[i:], 0x1b)
		if e < 0 {
			w.writeTail(buf[i:])
			return
		}
		if e > 0 {
			w.write(buf[i : i+e])
		}
		i += e
		// buf[i] is ESC.
		consumed, newStyle, changed, incomplete := w.p.scanEscape(buf[i:])
		if incomplete {
			w.hold(buf[i:])
			return
		}
		if changed {
			w.restyle(newStyle)
		}
		i += consumed
	}
}

// writeTail writes the final run of plain text, holding back a trailing
// incomplete rune for the next Write so a chunk boundary landing mid-character
// does not turn one character into two replacements. The pump carries this too,
// one layer up; owning it here as well is what makes the parser correct under
// ANY split, which is the property FuzzParse asserts.
func (w *writer) writeTail(tail string) {
	if hold := incompleteRuneTail(tail); hold > 0 {
		w.p.pending = append(w.p.pending[:0], tail[len(tail)-hold:]...)
		tail = tail[:len(tail)-hold]
	}
	w.write(tail)
}

// hold keeps an unterminated escape sequence for the next Write, unless it has
// grown past maxPendingBytes, in which case it is released as literal text
// rather than swallowed.
func (w *writer) hold(rest string) {
	if len(rest) > maxPendingBytes {
		w.write(neutralizeESC(rest))
		return
	}
	w.p.pending = append(w.p.pending[:0], rest...)
}

// restyle closes the open run at the current position and opens a new one.
func (w *writer) restyle(s style) {
	absEnd := w.p.offset + w.emitted
	w.closeRun(absEnd)
	w.openStart = absEnd
	w.openStyle = s
	w.p.cur = s
}

// Flush releases any held incomplete sequence as literal text. Call it once at
// end of stream so a truncated escape is not silently dropped; a stream that
// ends mid-sequence is malformed, and showing the bytes beats losing them.
func (p *Parser) Flush() (text string, spans []Span) {
	if len(p.pending) == 0 {
		return "", nil
	}
	text = validUTF8PerByte(neutralizeESC(string(p.pending)))
	p.pending = p.pending[:0]
	start := p.offset
	p.offset += utf16Len(text)
	if !p.cur.isDefault() {
		spans = []Span{{Start: start, End: p.offset, FG: p.cur.fg, BG: p.cur.bg, Attrs: p.cur.attrs}}
	}
	return text, spans
}

// Parse is the one-shot form for a complete string.
func Parse(s string) (text string, spans []Span) {
	p := NewParser()
	text, spans = p.Write(s)
	tailText, tailSpans := p.Flush()
	if tailText != "" {
		text += tailText
		spans = append(spans, tailSpans...)
	}
	return text, spans
}

// scanEscape examines an escape sequence starting at b[0]==ESC.
//
// consumed is how many bytes the sequence occupies (0 when incomplete).
// changed reports whether the style changed, in which case newStyle is it.
// incomplete reports that b holds a prefix of a sequence and the caller must
// wait for more bytes.
func (p *Parser) scanEscape(b string) (consumed int, newStyle style, changed, incomplete bool) {
	if len(b) < 2 {
		return 0, p.cur, false, true
	}
	switch b[1] {
	case '[':
		return p.scanCSI(b)
	case ']':
		n := scanOSC(b)
		return n, p.cur, false, n == 0
	case '(', ')', '*', '+':
		// Charset designation: ESC ( <final>. Meaningless without a grid.
		if len(b) < 3 {
			return 0, p.cur, false, true
		}
		return 3, p.cur, false, false
	case 'P', 'X', '^', '_':
		// DCS / SOS / PM / APC, terminated by ST. Dropped whole.
		n := scanStringTerminated(b)
		return n, p.cur, false, n == 0
	default:
		// A lone ESC or a two-byte sequence (ESC c, ESC 7, ...). Dropped.
		return 2, p.cur, false, false
	}
}

// scanCSI parses ESC [ ... <final>. Only an SGR final ('m') changes style;
// every other final byte is a grid operation this parser deliberately drops.
func (p *Parser) scanCSI(b string) (consumed int, newStyle style, changed, incomplete bool) {
	i := 2
	for i < len(b) {
		c := b[i]
		// Parameter bytes 0x30-0x3f, intermediate bytes 0x20-0x2f.
		if (c >= 0x30 && c <= 0x3f) || (c >= 0x20 && c <= 0x2f) {
			i++
			continue
		}
		if c >= 0x40 && c <= 0x7e {
			if c != 'm' {
				return i + 1, p.cur, false, false
			}
			st := applySGR(p.cur, b[2:i])
			return i + 1, st, st != p.cur, false
		}
		// An illegal byte inside a CSI: the sequence is malformed. Consume
		// what we have so the stream resynchronizes rather than stalling.
		return i, p.cur, false, false
	}
	return 0, p.cur, false, true
}

// scanOSC parses ESC ] ... (BEL | ESC \). Returns 0 when incomplete.
//
// OSC 8 hyperlinks are dropped rather than turned into links: a transcript
// linkifies paths and URLs itself (linkifyPaths), so honouring an
// agent-supplied link target would add an anchor whose href nothing here
// validated.
func scanOSC(b string) int {
	for i := 2; i < len(b); i++ {
		if b[i] == 0x07 {
			return i + 1
		}
		if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
			return i + 2
		}
	}
	return 0
}

// scanStringTerminated parses a DCS/SOS/PM/APC string ending in ST or BEL.
func scanStringTerminated(b string) int {
	for i := 2; i < len(b); i++ {
		if b[i] == 0x07 {
			return i + 1
		}
		if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
			return i + 2
		}
	}
	return 0
}

// applySGR folds one SGR parameter list into a style. An empty list is a
// reset, matching `ESC[m`.
func applySGR(cur style, params string) style {
	if params == "" {
		return style{fg: ColorDefault, bg: ColorDefault}
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		n, ok := atoiSGR(fields[i])
		if !ok {
			// A non-numeric parameter (a private-mode marker, an empty field)
			// counts as 0 the way terminals treat it.
			n = 0
		}
		// 38 and 48 are the only parameters that CONSUME the ones after them,
		// so they are handled here rather than in a pure fold.
		if n == 38 || n == 48 {
			c, adv, valid := extendedColor(fields[i+1:])
			if valid {
				if n == 38 {
					cur.fg = c
				} else {
					cur.bg = c
				}
			}
			i += adv
			continue
		}
		cur = applySGRParam(cur, n)
	}
	return cur
}

// applySGRParam folds one self-contained SGR parameter into a style. Split from
// applySGR so the fold is a flat table and the two parameters that consume their
// successors stay in the loop that can advance the index.
func applySGRParam(cur style, n int) style {
	// Attribute set and clear bits, keyed by parameter. A table rather than a
	// branch per case: the parameters are unrelated to each other, so a switch
	// only says "these are all SGR" at the price of thirty lines.
	if set, ok := sgrAttrSet[n]; ok {
		cur.attrs |= set
		return cur
	}
	if off, ok := sgrAttrClear[n]; ok {
		cur.attrs &^= off
		return cur
	}
	switch {
	case n == 0:
		return style{fg: ColorDefault, bg: ColorDefault}
	case n == 39:
		cur.fg = ColorDefault
	case n == 49:
		cur.bg = ColorDefault
	case n >= 30 && n <= 37:
		cur.fg = int32(n - 30)
	case n >= 40 && n <= 47:
		cur.bg = int32(n - 40)
	case n >= 90 && n <= 97:
		cur.fg = int32(n - 90 + 8)
	case n >= 100 && n <= 107:
		cur.bg = int32(n - 100 + 8)
	}
	return cur
}

// sgrAttrSet maps an SGR parameter to the attribute bits it turns ON. 6 is
// "rapid blink", which terminals render the same as 5.
var sgrAttrSet = map[int]uint16{
	1:  AttrBold,
	2:  AttrDim,
	3:  AttrItalic,
	4:  AttrUnderline,
	5:  AttrBlink,
	6:  AttrBlink,
	7:  AttrInverse,
	8:  AttrHidden,
	9:  AttrStrike,
	21: AttrDoubleUnderline,
	53: AttrOverline,
}

// sgrAttrClear maps an SGR parameter to the attribute bits it turns OFF. 22
// clears bold AND dim, and 24 clears both underline kinds, which is why this is
// a bitmask per entry rather than the inverse of sgrAttrSet.
var sgrAttrClear = map[int]uint16{
	22: AttrBold | AttrDim,
	23: AttrItalic,
	24: AttrUnderline | AttrDoubleUnderline,
	25: AttrBlink,
	27: AttrInverse,
	28: AttrHidden,
	29: AttrStrike,
	55: AttrOverline,
}

// extendedColor reads the parameters after a 38/48 selector: `5;<idx>` for the
// 256-colour palette or `2;<r>;<g>;<b>` for truecolour. adv is how many
// parameters it consumed, which the caller adds to its index so a malformed
// colour cannot swallow the styling that follows it.
func extendedColor(rest []string) (c int32, adv int, ok bool) {
	if len(rest) == 0 {
		return 0, 0, false
	}
	mode, valid := atoiSGR(rest[0])
	if !valid {
		return 0, 1, false
	}
	switch mode {
	case 5:
		return palette256(rest)
	case 2:
		return trueColor(rest)
	default:
		return 0, 1, false
	}
}

// palette256 reads `5;<idx>`.
func palette256(rest []string) (c int32, adv int, ok bool) {
	if len(rest) < 2 {
		return 0, 1, false
	}
	idx, valid := atoiSGR(rest[1])
	if !valid || idx < 0 || idx > 255 {
		return 0, 2, false
	}
	return int32(idx), 2, true
}

// trueColor reads `2;<r>;<g>;<b>`.
func trueColor(rest []string) (c int32, adv int, ok bool) {
	if len(rest) < 4 {
		return 0, len(rest), false
	}
	var comp [3]uint8
	for i := range comp {
		v, valid := atoiSGR(rest[i+1])
		if !valid || v < 0 || v > 255 {
			return 0, 4, false
		}
		// #nosec G115 -- bounded to 0-255 on the line above.
		comp[i] = uint8(v)
	}
	return RGB(comp[0], comp[1], comp[2]), 4, true
}

// atoiSGR parses a decimal SGR parameter. It rejects anything non-numeric
// rather than partially parsing, and bounds the value so a long digit run
// cannot overflow. An empty field is 0, which is how terminals read it.
func atoiSGR(s string) (int, bool) {
	if s == "" {
		return 0, true
	}
	n := 0
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 0xffff {
			return 0, false
		}
	}
	return n, true
}
