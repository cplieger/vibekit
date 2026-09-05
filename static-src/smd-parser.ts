// ---------------------------------------------------------------------------
// Streaming markdown parser for incremental DOM rendering.
//
// Port of streaming-markdown v0.2.15 (MIT, © 2024 Damian Tarnawski)
// https://github.com/thetarnav/streaming-markdown
//
// The fork exists for unilateral fixability: parser bugs get fixed here under
// this repo's invariant tests instead of waiting on an upstream release, at the
// cost of tracking upstream by hand. The PARSER is append-only, so emitted
// content stays stable across flushes; the RENDERER is not, and unwrapping an
// element whose inline token never closed is the sanctioned use of that
// (smd-renderer.ts, `unwrap_unclosed`).
//
// Construct coverage matches the reference implementation: paragraphs, headings
// (1-6), blockquotes, ordered/unordered/task lists, code blocks/fences/
// inline, bold/italic/strike, links/images/raw URLs, tables, line breaks,
// horizontal rules. The equation tokens carry their expression as TEXT, and the
// renderer converts the accumulated source to native MathML when the expression
// closes (smd-renderer.ts + mathml.ts). An expression the converter does not
// support stays as its raw string. Inline is `$x$` or `\(x\)`; a BLOCK needs a
// newline right after its opener (`$$\n…$$`, `\[\n…\]`), because `\[` is also a
// legitimate escaped bracket — so `\[x\]` on one line renders as the literal
// text, which is honest but is not display math.
//
// DIVERGENCE REGISTER. Reasoning, measurements and the append-only dependency
// list live in `.kiro/steering/vibekit-ui.md`; this is the index.
//
// THE STANDING PRINCIPLE. vibekit is a PUBLIC app: anyone can paste anything
// into it, and a corpus of one person's writing style predicts nothing about
// what arrives next. Zero corpus occurrences is therefore NOT a reason to leave
// a markdown fault unfixed — the corpus measures what this installation has
// seen, not what the next paste contains. Every exclusion below rests on one of
// four grounds, and frequency is not among them: an architectural impossibility
// in the append-only model, stated precisely; a security invariant, named; a
// product decision, stated; or a DEFERRAL on effort, which is not a decline and
// says so. "Carried from upstream" is not a ground either: this fork owns the
// parser outright, so there is no release to wait for and every entry below is
// assessed on feasibility here.
//
// Fixed here, still open upstream: emphasis inside a word (#36, both the open
// and the single-`_` close), which is also #29's `[read_md.md](url)` losing its
// label to an intraword `_`; a scheme check before an href is set (#45, in the
// renderer); a bracketed run inside a link label (#39); column alignment from
// the delimiter row (#44); the table section picked by child index (#43); a
// fence closed by a mid-line backtick run (#34/#40); and a trailing space
// breaking a table row (#30). Fixed with no upstream counterpart: unbounded
// recursion once the token stack saturates, `<` `[` `\` and a backtick deleted at
// the end of a line or of the input, an opener deleted at the depth cap — the
// inline delimiters, and with them every block marker: a heading's `#`s, a fence
// and its info string, a list marker, a `>`, `$$`, a URL's scheme (which was
// emitted twice) — an attribute for a refused token landing on the enclosing
// element, buffered text landing inside the block opened after it, an unclosed
// inline opener formatting the rest of its block, a table opening without a
// delimiter row or losing one to a tab or to an escaped pipe, a bare URL
// swallowing the backtick after a full-width comma, and a block prefix reading
// as literal text on the line after a saturated one.
//
// Also fixed here, absent from upstream: entity and numeric character
// references; a link title, an angle-bracketed destination and balanced parens
// in one; `1)` ordered lists and a new list on a delimiter change; an ATX
// closing sequence and a bare `#` run as an empty heading; angle autolinks; a
// table row that ends without a pipe; a pipe inside a code span hidden from the
// cell count; a delimiter row that begins with a bullet marker; and a label that
// closes with no destination rendering as the text that was typed rather than as
// an anchor with no href.
//
// TWO SECURITY RULES the reference decoding establishes, both checkable by test
// name. A reference is decoded BEFORE a URL is validated, never after:
// `javascript&#58;alert(1)` does not start with `javascript:`, so a gate handed
// the encoded spelling passes it and the browser then decodes a live scheme
// ("an encoded colon does not smuggle a javascript scheme"). And a decoded
// character is NEVER re-parsed — it goes straight to the text buffer — so no
// reference can synthesise markup ("an encoded asterisk does not emphasise").
//
// THE AUTOLINK CARVE-OUT is the only exception to escaping `<` wholesale. Only
// CommonMark 6.5's absolute-URI and email forms are recognised, both route
// through the same `isSafeUrl` gate as every other href, and one inside a link
// label stays text because a nested anchor is invalid HTML. `Vec<String>`,
// `<div>`, `<!-- -->` and `<String>` all still render as escaped text.
//
// THE DEPTH-CAP RULE is drain-aware. Past the cap every source character
// survives as text WHILE the blockquote stack is saturated. A line that does not
// carry the full block prefix DRAINS it, and past that point a blank line is a
// block separator and a marker is syntax again — which is what both references
// do and what the below-cap control has always done. A construct the cap still
// refuses stays literal, so a heading on such a line keeps its hashes. What no
// fix can reach at the cap is the enclosing `<p>`: the paragraph push is refused
// there, so the TEXT of a saturated blockquote matches the reference while its
// markup keeps one fewer element. That is the depth limit, not a defect.
//
// A TABLE SHAPE IS MEASURED AGAINST GFM. commonmark.js implements no tables, so
// every table case diverges from it by construction; GFM is the oracle for those,
// and where the two disagree the register says which one is being matched.
//
// GENUINE IMPOSSIBILITIES in the append-only model, not deferrals. Nesting an
// emphasis run inside another of the same character (`a *b *c* d* e`): CommonMark
// resolves emphasis from a delimiter stack at the END of the block, so which of
// two runs opens and which closes is unknowable until then, and a streaming
// parser must emit the opener before that. The closing half of CommonMark 6.2
// (whether a run ever closes at all, which decides the OPENING delimiter's fate)
// is the same shape; the half that IS computable from the lookbehind the parser
// already has is implemented.
//
// SECURITY INVARIANT. Raw HTML is emitted as text rather than parsed. Two
// grep-checkable artefacts hold it: the no-HTML-parser invariant at
// `editor-markdown.ts`, and the fast-check properties over `<script`,
// `javascript:`, `on*`, `data:` and `vbscript:` in `markdown.test.ts`. Parsing
// raw HTML would move those properties onto a sanitiser. The references also LOSE
// text here (`Vec<String>` becomes `Vec`), so this is the better renderer as well
// as the safer one.
//
// PRODUCT DECISION. A soft line break renders as `<br>`. Chat text is written
// expecting the line to break. The lazy-continuation and bold-lead-in rows in
// `probe-classes.ts` are downstream of the same decision, not separate entries,
// and so is the one remaining difference on a pipe line that does not become a
// table: with the `<br>`s normalised to spaces, a header holding a code span
// with a pipe in it, over a two-column delimiter row, is byte-identical to
// CommonMark's paragraph.
//
// DECLINED ON MERIT. A body row keeps every cell it was given; the GFM reference
// truncates to the header's count, which DELETES user text, and this register
// ranks text loss above a ragged row. Checkable premise:
// `| a | b |` over `| - | - |` over `| 1 | 2 | 3 |` renders the `3` here and
// drops it in the reference. Padding a short row to the header's count loses
// nothing and is not built either, because nothing has asked for it.
//
// CHARACTERIZATION, no loss. A space in a link destination
// (`[a](http://e.com/a b)`) sets the href with the space where CommonMark renders
// the whole run literally. An unparseable `](…)` run keeps the whole run as the
// destination, deliberately: routing it to a literal would change every real link
// a model writes with an unencoded space. An empty destination with a title
// (`[a]( "t")`) keeps the title where both references render it literally.
//
// `***x***` NESTS `<strong><em>` WHERE COMMONMARK NESTS `<em><strong>`, and the
// correction is declined on merit rather than deferred on cost. Swapping the two
// `add_inline_token` calls in `handleCommon`'s `_`/`*` arm is one edit and was
// MEASURED: it fixes seven shapes exactly (`***x***`, `___tri___`, `***x**y*`,
// `a***b***c`, `***x***y`, `***a*** b`, `___a___ ___b___`) and breaks one,
// `***x*y**`, which goes from CommonMark's own `<strong><em>x</em>y</strong>` to
// `*<strong>x<em>y</em></strong>*` with two literal asterisks. The cause is
// structural: a single `*` inside `<strong>` has to CLOSE the enclosing `<em>`,
// and only `handleItalic` knows how, because both halves of the two-level close
// live there and depend on ITALIC being innermost. So the trade is visible stray
// asterisks for a nesting order that renders identically — there is no CSS, no
// accessibility difference and no selector in the app that distinguishes
// `<strong><em>` from `<em><strong>`. Making it correct means moving the
// two-level close into `handleStrong` and giving it a delimiter-accumulation
// state it has never had, on the two mutually-recursive handlers the emphasis
// work most recently stabilised. Checkable premise: make the swap and
// `***x*y**` loses its markup.
//
// DEFERRED, each feasible here, none declined. A nested blockquote or table
// escaping a list item (#41) and a block prefix in front of a table row (#42) —
// which is why a pipe line inside a blockquote is text — are one subject: both
// need the close to target the innermost enclosing LIST_ITEM whose indent the
// current line satisfies, which `continue_or_add_list` already computes, plus a
// per-line block-prefix contract the table handlers honour. No lookahead and no
// un-emit; the work is re-deriving `blockquote_idx`, which five readers treat as
// an absolute stack index. `__` cannot refuse a close the way `_` does, because
// it decides on the second character of the run: the one-character deferral that
// would fix it collides with the meaning `handleItalic` already gives `__` in
// `pending`, so it needs a dedicated deferral token. An image label's inline
// markup survives literally in the alt text (`![a *b* c]` gives `alt="a *b* c"`):
// CommonMark's alt is the label's PLAIN TEXT with markup stripped, which the
// renderer already produces for text inside an `<img>`, so what is missing is
// suppressing ELEMENT creation there. Setext headings need one line of held
// lookahead plus a close-time promote in the renderer, and collide with the
// thematic-break reading of `---`. Reference links need a document-scoped
// definition map and a registry of unresolved anchors the renderer retains; the
// cheap half is done, since an undefined reference now renders as the text that
// was typed. `<p>` inside a loose list item, and the two list-content escapes,
// are one missing state and need a renderer wrap-at-close. Three of these
// re-parent already-emitted nodes, so each needs the `data-vk-chunk-settled`
// mitigation the unwrap path already carries.
// ---------------------------------------------------------------------------

import {
  handleRootContext,
  handleHeading,
  handleTable,
  handleTableRow,
  handleTableCell,
  handleCodeBlock,
  handleCodeFence,
  handleCodeInline,
  handleMaybeTask,
  handleStrong,
  handleItalic,
  handleMaybeEqBlock,
  handleMaybeURL,
  handleLinkOrImage,
  handleRawURL,
  handleMaybeAngle,
  handleMaybeEntity,
  handleCommon,
} from "./smd-parser-handlers.js";

// Re-export only the constants used by test consumers.
export {
  DOCUMENT,
  HEADING_1,
  HEADING_2,
  HEADING_3,
  HEADING_4,
  HEADING_5,
  HEADING_6,
} from "./smd-parser-types.js";

export type { Renderer, Parser } from "./smd-parser-types.js";

import type { Parser, Token, Renderer } from "./smd-parser-types.js";
import {
  DOCUMENT,
  BLOCKQUOTE,
  HEADING_1,
  HEADING_2,
  HEADING_3,
  HEADING_4,
  HEADING_5,
  HEADING_6,
  LINE_BREAK,
  LIST_ORDERED,
  LIST_UNORDERED,
  NEWLINE,
  TABLE,
  TABLE_ROW,
  TABLE_CELL,
  CODE_BLOCK,
  CODE_FENCE,
  CODE_INLINE,
  STRONG_AST,
  STRONG_UND,
  ITALIC_AST,
  ITALIC_UND,
  STRIKE,
  MAYBE_EQ_BLOCK,
  MAYBE_TASK,
  EQUATION_BLOCK,
  EQUATION_INLINE,
  IMAGE,
  LINK,
  RAW_URL,
  MAYBE_ANGLE,
  MAYBE_ENTITY,
  TOKEN_ARRAY_CAP,
  add_text,
  end_token,
  end_token_unresolved,
  end_tokens_to_indent,
  is_inline_token,
} from "./smd-parser-types.js";

const MAYBE_URL = 102 as Token; // local-only token, not in TOKENS

// --- Parser constructor ---

export function parser<T>(renderer: Renderer<T>): Parser {
  const tokens = new Uint32Array(TOKEN_ARRAY_CAP);
  tokens[0] = DOCUMENT;
  return {
    renderer: renderer as Renderer<unknown>,
    textBuf: "",
    pending: "",
    tokens,
    delims: new Array<string>(TOKEN_ARRAY_CAP).fill(""),
    len: 0,
    token: DOCUMENT,
    fence_end: 0,
    blockquote_idx: 0,
    hr_char: "",
    hr_chars: 0,
    fence_start: 0,
    spaces: new Uint8Array(TOKEN_ARRAY_CAP),
    indent: "",
    indent_len: 0,
    table_state: 0,
    prev_is_word: false,
    link_depth: 0,
    at_end: false,
    atx_close: false,
    write: parser_write,
  };
}

export function parser_end(p: Parser): void {
  if (p.pending.length > 0) {
    p.at_end = true;
    parser_write(p, "\n");
    // Whatever a handler is still holding was held for a character that never
    // arrived, so it is literal text — `<` held as a possible `<br>` is the case
    // this recovers. Drop out of any MAYBE_* token first, and strip the
    // synthetic newline above, which is not input.
    const held = p.pending.endsWith("\n") ? p.pending.slice(0, -1) : p.pending;
    if (held !== "") {
      p.token = p.tokens[p.len] as Token;
      p.pending = "";
      p.textBuf += held;
      add_text(p);
    }
  }
  // Trailing inline tokens never closed. Stop at the first block token: an open
  // paragraph or fence is the streaming tail, and the code-block decoration
  // sweeps rely on a fence staying open.
  while (is_inline_token(p.tokens[p.len] as Token)) {
    end_token_unresolved(p);
  }
}

// ---------------------------------------------------------------------------
// parser_write — the state machine. Consumes a chunk of markdown text
// one codepoint at a time, emitting add_token/end_token/add_text calls
// to the renderer as syntax is recognised. Faithful port of the
// reference implementation; inline `case` labels match character
// branches in the original so bug reports tagged to smd v0.2.15 map
// directly.
// ---------------------------------------------------------------------------

// tokenAction is the result of a token-specific handler.
const actionContinue = 0;
const actionBreak = 1;
const actionAlwaysContinue = 2;
type TokenAction = typeof actionContinue | typeof actionBreak | typeof actionAlwaysContinue;

// tokenHandler is a per-token dispatch function. Returns the action to take.
type TokenHandler = (p: Parser, char: string, pending: string) => TokenAction;

// TOKEN_HANDLERS maps token types to their specific handler logic.
// Handlers that always consume (code blocks, raw URLs, equation blocks)
// return actionAlwaysContinue. Handlers that conditionally consume return
// actionContinue on match, actionBreak on fall-through.
const TOKEN_HANDLERS: Partial<Record<Token, TokenHandler>> = {
  [LINE_BREAK]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [DOCUMENT]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [BLOCKQUOTE]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [LIST_ORDERED]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [LIST_UNORDERED]: (p, char, pending) =>
    handleRootContext(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_1]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_2]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_3]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_4]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_5]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [HEADING_6]: (p, char, pending) =>
    handleHeading(p, char, pending) ? actionContinue : actionBreak,
  [TABLE]: (p, char, pending) => (handleTable(p, char, pending) ? actionContinue : actionBreak),
  [TABLE_ROW]: (p, char, pending) =>
    handleTableRow(p, char, pending) ? actionContinue : actionBreak,
  [TABLE_CELL]: (p, char, pending) =>
    handleTableCell(p, char, pending) ? actionContinue : actionBreak,
  [CODE_BLOCK]: (p, char, pending) => {
    handleCodeBlock(p, char, pending);
    return actionAlwaysContinue;
  },
  [CODE_FENCE]: (p, char, pending) => {
    handleCodeFence(p, char, pending);
    return actionAlwaysContinue;
  },
  [CODE_INLINE]: (p, char, pending) => {
    handleCodeInline(p, char, pending);
    return actionAlwaysContinue;
  },
  [MAYBE_TASK]: (p: Parser, char: string, pending: string) =>
    handleMaybeTask(p, char, pending) ? actionContinue : actionBreak,
  [STRONG_AST]: (p: Parser, char: string, _pending: string) =>
    handleStrong(p, char) ? actionContinue : actionBreak,
  [STRONG_UND]: (p: Parser, char: string, _pending: string) =>
    handleStrong(p, char) ? actionContinue : actionBreak,
  [ITALIC_AST]: (p, char, pending) =>
    handleItalic(p, char, pending) ? actionContinue : actionBreak,
  [ITALIC_UND]: (p, char, pending) =>
    handleItalic(p, char, pending) ? actionContinue : actionBreak,
  [STRIKE]: (p, _char, pending) => {
    if (pending === "~~") {
      add_text(p);
      end_token(p);
      p.pending = "";
      return actionContinue;
    }
    return actionBreak;
  },
  [MAYBE_EQ_BLOCK]: (p, char, _pending) => {
    handleMaybeEqBlock(p, char);
    return actionAlwaysContinue;
  },
  [EQUATION_BLOCK]: (p, _char, pending) => {
    // `$$` as well as `$`: a block opened with `$$\n` is closed with `$$`, and
    // the bare `$` only matched when the previous character left `pending`
    // empty — which the newline before a closing fence never does. See the
    // equation guard in handleCommon's `$` case for the other half.
    if (pending === "\\]" || pending === "$" || pending === "$$") {
      add_text(p);
      end_token(p);
      p.pending = "";
      return actionContinue;
    }
    return actionBreak;
  },
  [EQUATION_INLINE]: (p, char, pending) => {
    if (pending === "\\)" || p.pending.startsWith("$")) {
      add_text(p);
      end_token(p);
      p.pending = char === ")" ? "" : char;
      return actionContinue;
    }
    return actionBreak;
  },
  [MAYBE_URL]: (p: Parser, char: string, pending: string) => {
    handleMaybeURL(p, char, pending);
    return actionAlwaysContinue;
  },
  [LINK]: (p, char, pending) =>
    handleLinkOrImage(p, char, pending) ? actionContinue : actionBreak,
  [IMAGE]: (p, char, pending) =>
    handleLinkOrImage(p, char, pending) ? actionContinue : actionBreak,
  [RAW_URL]: (p, char, pending) => {
    handleRawURL(p, char, pending);
    return actionAlwaysContinue;
  },
  [MAYBE_ANGLE]: (p, char, pending) =>
    handleMaybeAngle(p, char, pending) ? actionContinue : actionBreak,
  [MAYBE_ENTITY]: (p: Parser, char: string, pending: string) =>
    handleMaybeEntity(p, char, pending) ? actionContinue : actionBreak,
};

export function parser_write(p: Parser, chunk: string): void {
  for (const char of chunk) {
    // Handle newlines — once a newline was pending, consume leading
    // whitespace so we can decide whether to extend the previous block
    // or start a new one.
    if (p.token === NEWLINE) {
      switch (char) {
        case " ":
          p.indent_len += 1;
          continue;
        case "\t":
          p.indent_len += 4;
          continue;
      }

      const indent = end_tokens_to_indent(p, p.indent_len);
      p.indent_len = 0;
      p.token = p.tokens[p.len] as Token;

      if (indent > 0) {
        parser_write(p, " ".repeat(indent));
      }
    }

    const pending_with_char = p.pending + char;

    // Token-specific dispatch via lookup table.
    const handler = TOKEN_HANDLERS[p.token];
    if (handler !== undefined) {
      const action = handler(p, char, pending_with_char);
      if (action === actionContinue || action === actionAlwaysContinue) {
        continue;
      }
      // actionBreak: fall through to common checks below.
    }

    // Common inline checks — apply regardless of block context unless
    // the guards above kicked in.
    if (handleCommon(p, char, pending_with_char)) {
      continue;
    }

    // Raw URL detection: "foo http://..." can start anywhere a space
    // or line boundary ends a word.
    if (
      p.token !== IMAGE &&
      p.token !== LINK &&
      p.token !== EQUATION_BLOCK &&
      p.token !== EQUATION_INLINE &&
      char === "h" &&
      (p.pending === " " || p.pending === "")
    ) {
      p.textBuf += p.pending;
      p.pending = char;
      p.token = MAYBE_URL;
      continue;
    }

    // No check hit — shift pending forward and keep going.
    p.textBuf += p.pending;
    p.pending = char;
  }

  add_text(p);
}
