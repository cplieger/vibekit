// ---------------------------------------------------------------------------
// Streaming markdown parser for incremental DOM rendering.
//
// Port of streaming-markdown v0.2.15 (MIT, © 2024 Damian Tarnawski)
// https://github.com/thetarnav/streaming-markdown
//
// Rewritten in TypeScript with one substantive change: `add_text` appends
// text via the renderer's callback instead of the fixed `appendChild(text
// node)` form, so callers can wrap newly-appended text in an animated
// span without patching the parser. That's the whole point of porting
// it — our renderer wraps each new text chunk in `<span data-vk-stream>`
// so CSS can fade it in as it mounts. The parser is append-only, so old
// content stays stable across flushes.
//
// Behaviour matches the reference implementation: paragraphs, headings
// (1-6), blockquotes, ordered/unordered/task lists, code blocks/fences/
// inline, bold/italic/strike, links/images/raw URLs, tables, line breaks,
// horizontal rules. Equation/MathML tokens are accepted for parser
// completeness but render as empty wrappers (vibekit has no MathML).
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

// Re-export everything from smd-parser-types for backward compatibility.
// Consumers that import from "./smd-parser.js" continue to work unchanged.
export {
  DOCUMENT,
  PARAGRAPH,
  HEADING_1,
  HEADING_2,
  HEADING_3,
  HEADING_4,
  HEADING_5,
  HEADING_6,
  CODE_BLOCK,
  CODE_FENCE,
  CODE_INLINE,
  ITALIC_AST,
  ITALIC_UND,
  STRONG_AST,
  STRONG_UND,
  STRIKE,
  LINK,
  RAW_URL,
  IMAGE,
  BLOCKQUOTE,
  LINE_BREAK,
  RULE,
  LIST_UNORDERED,
  LIST_ORDERED,
  LIST_ITEM,
  CHECKBOX,
  TABLE,
  TABLE_ROW,
  TABLE_CELL,
  EQUATION_BLOCK,
  EQUATION_INLINE,
  NEWLINE,
  MAYBE_BR,
  MAYBE_EQ_BLOCK,
  MAYBE_TASK,
  HREF,
  SRC,
  LANG,
  CHECKED,
  START,
  TOKEN_ARRAY_CAP,
  add_text,
  end_token,
  add_token,
  idx_of_token,
  end_tokens_to_len,
  end_tokens_to_indent,
  continue_or_add_list,
  add_list_item,
  clear_root_pending,
  is_digit,
  is_delimiter_or_number,
  heading_from_level,
  attr_to_html_attr,
} from "./smd-parser-types.js";

export type { Token, Attr, Renderer, Parser } from "./smd-parser-types.js";

import type { Parser, Token, Renderer } from "./smd-parser-types.js";
import {
  DOCUMENT,
  BLOCKQUOTE,
  LINE_BREAK,
  LIST_ORDERED,
  LIST_UNORDERED,
  PARAGRAPH,
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
  add_token,
  end_tokens_to_indent,
} from "./smd-parser-types.js";

const MAYBE_URL = 102 as Token;

// --- Parser constructor ---

export function parser<T>(renderer: Renderer<T>): Parser {
  const tokens = new Uint32Array(TOKEN_ARRAY_CAP);
  tokens[0] = DOCUMENT;
  return {
    renderer: renderer as Renderer<unknown>,
    textBuf: [],
    pending: "",
    tokens,
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
    write: parser_write,
  };
}

export function parser_end(p: Parser): void {
  if (p.pending.length > 0) {
    parser_write(p, "\n");
  }
}

function ensure_paragraph(p: Parser): void {
  switch (p.token) {
    case LINE_BREAK:
    case DOCUMENT:
    case BLOCKQUOTE:
    case LIST_ORDERED:
    case LIST_UNORDERED:
      add_token(p, PARAGRAPH);
  }
}

// The reference parser exposes `push_text` that wraps ensure_paragraph
// + text concat. We don't call it directly because parser_write inlines
// the logic for performance, but ensure_paragraph is still referenced
// indirectly to keep the symbol live and document the control flow.
void ensure_paragraph;

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
  [MAYBE_TASK]: (p, char, pending) =>
    handleMaybeTask(p, char, pending) ? actionContinue : actionBreak,
  [STRONG_AST]: (p, char, _pending) => (handleStrong(p, char) ? actionContinue : actionBreak),
  [STRONG_UND]: (p, char, _pending) => (handleStrong(p, char) ? actionContinue : actionBreak),
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
    if (pending === "\\]" || pending === "$") {
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
  [MAYBE_URL]: (p, char, pending) => {
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
      p.textBuf.push(p.pending);
      p.pending = char;
      p.token = MAYBE_URL;
      continue;
    }

    // No check hit — shift pending forward and keep going.
    p.textBuf.push(p.pending);
    p.pending = char;
  }

  add_text(p);
}
