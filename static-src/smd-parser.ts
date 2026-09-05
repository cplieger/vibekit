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
// delimiter row or losing one to a tab or to an escaped pipe, and a bare URL
// swallowing the backtick after a full-width comma.
//
// Deliberately not fixed. `__` cannot refuse a close the way `_` does: it
// decides on the second character of the run, before the character after it has
// arrived, so `__x_y_` keeps its literal reading. An image label's inline markup
// survives literally in the alt text (`![a *b* c]` gives `alt="a *b* c"` where
// CommonMark gives `a b c`): nothing is parsed inside an IMAGE, and an attribute
// cannot hold the elements a parsed label would produce. Raw HTML is emitted as
// text rather than parsed, which is the security property the XSS tests pin. A
// soft line break renders as `<br>` by product decision. Carried from upstream:
// a nested blockquote or table escaping a list item (#41), and a block prefix in
// front of a table row (#42), which is why a pipe line inside a blockquote is
// text. Two table rows read looser than GFM: a delimiter row starting with a
// bullet marker (`| a | b |` over `- | -`) opens a table where the reference
// opens a list, and a body row keeps its own cell count rather than the header's.
// `***x***` nests `<strong><em>` where CommonMark nests `<em><strong>`. Absent
// from both: setext headings, reference links, angle autolinks, link titles,
// entity references, ATX closing sequences, `1)` ordered lists, `<p>` inside a
// loose list item, and the nesting of an emphasis run inside another of the same
// character (`a *b *c* d* e`).
// ---------------------------------------------------------------------------

import {
  handleRootContext,
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
  handleMaybeBR,
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
  MAYBE_BR,
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
  [MAYBE_BR]: (p, char, pending) =>
    handleMaybeBR(p, char, pending) ? actionContinue : actionBreak,
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
