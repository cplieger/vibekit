// ---------------------------------------------------------------------------
// Shared types, constants, and pure utility functions for the streaming
// markdown parser. Extracted from smd-parser.ts to break the circular
// import between smd-parser.ts and smd-parser-handlers.ts.
//
// Import graph after extraction:
//   smd-parser-handlers.ts → smd-parser-types.ts (types, constants, utils)
//   smd-parser-handlers.ts → smd-parser.ts       (parser_write only)
//   smd-parser.ts          → smd-parser-types.ts (re-exports all)
//   smd-parser.ts          → smd-parser-handlers.ts (handler functions)
//   smd-renderer.ts        → smd-parser-types.ts (types, constants)
// ---------------------------------------------------------------------------

// --- Token enum (matches smd.js constants) ---

/** All valid token values. Derive the Token type from this object. */
const TOKENS = {
  DOCUMENT: 1,
  PARAGRAPH: 2,
  HEADING_1: 3,
  HEADING_2: 4,
  HEADING_3: 5,
  HEADING_4: 6,
  HEADING_5: 7,
  HEADING_6: 8,
  CODE_BLOCK: 9,
  CODE_FENCE: 10,
  CODE_INLINE: 11,
  ITALIC_AST: 12,
  ITALIC_UND: 13,
  STRONG_AST: 14,
  STRONG_UND: 15,
  STRIKE: 16,
  LINK: 17,
  RAW_URL: 18,
  IMAGE: 19,
  BLOCKQUOTE: 20,
  LINE_BREAK: 21,
  RULE: 22,
  LIST_UNORDERED: 23,
  LIST_ORDERED: 24,
  LIST_ITEM: 25,
  CHECKBOX: 26,
  TABLE: 27,
  TABLE_ROW: 28,
  TABLE_CELL: 29,
  EQUATION_BLOCK: 30,
  EQUATION_INLINE: 31,
  NEWLINE: 101,
  MAYBE_BR: 104,
  MAYBE_EQ_BLOCK: 105,
} as const satisfies Record<string, number>;

/** Token type derived from the TOKENS constant object. */
export type Token = (typeof TOKENS)[keyof typeof TOKENS];

// Re-export individual constants via destructuring.
export const {
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
} = TOKENS;

// --- Attr enum ---

/** All valid attribute values. */
const ATTRS = {
  HREF: 1,
  SRC: 2,
  LANG: 4,
  CHECKED: 8,
  START: 16,
  /** The literal that opened an inline token which never closed. Not an HTML
   *  attribute: the renderer unwraps the element and restores this text in its
   *  place. Always emitted immediately before that token's `end_token`. */
  UNCLOSED: 32,
  /** A table's per-column `text-align`, comma-joined, read off the delimiter
   *  row's colons. Not an HTML attribute either: the renderer applies it to each
   *  cell by position. Emitted on the TABLE token before its first row. */
  ALIGN: 64,
} as const satisfies Record<string, number>;

/** Attr type derived from the ATTRS constant object. */
export type Attr = (typeof ATTRS)[keyof typeof ATTRS];

export const { HREF, SRC, LANG, CHECKED, START, UNCLOSED, ALIGN } = ATTRS;

export const TOKEN_ARRAY_CAP = 24;

// --- Renderer interface ---

export interface Renderer<T> {
  data: T;
  add_token: (data: T, type: Token) => void;
  end_token: (data: T) => void;
  add_text: (data: T, text: string) => void;
  set_attr: (data: T, attr: Attr, value: string) => void;
}

// --- Parser state ---

export interface Parser {
  renderer: Renderer<unknown>;
  textBuf: string;
  pending: string;
  tokens: Uint32Array;
  /** The literal that opened `tokens[i]`, for the inline tokens only. An
   *  append-only parser cannot un-open a token, but the renderer can unwrap the
   *  element at close time — and to restore the delimiter it needs to know what
   *  it was, since the characters were consumed when the token opened. */
  delims: string[];
  len: number;
  token: Token;
  spaces: Uint8Array;
  indent: string;
  indent_len: number;
  fence_end: number;
  fence_start: number;
  blockquote_idx: number;
  hr_char: string;
  hr_chars: number;
  table_state: number;
  /** Whether the last COMMITTED character was a word character — the lookbehind
   *  for CommonMark 6.2, where a `_` run preceded by one cannot open emphasis.
   *  Reading `textBuf`'s last character alone is not enough: `parser_write` ends
   *  with `add_text` (smd-parser.ts), which hands the buffer to the renderer and
   *  clears it, so a chunk boundary landing between `run_` and `progress` would
   *  otherwise lose the `n`. */
  prev_is_word: boolean;
  /** Depth of unclosed `[` inside the open LINK/IMAGE label. CommonMark 6.3
   *  allows balanced brackets in a label, so only a `]` at depth 0 ends it. */
  link_depth: number;
  /** Set by `parser_end` before it writes its synthetic newline. That newline is
   *  not input, so a construct needing a character after it cannot be one: a
   *  trailing `\` is a literal backslash rather than a hard line break. */
  at_end: boolean;
  write: (p: Parser, chunk: string) => void;
}

// --- Pure utility functions ---

/** CommonMark's word character: neither Unicode whitespace nor Unicode
 *  punctuation, which makes every letter and digit in every script one. */
const NOT_WORD = /[\s\p{P}\p{S}]/u;

/** Whether `s` ends in a word character. Reads the last CODE POINT: an astral
 *  character's trailing low surrogate is category Cs on its own, which matches
 *  none of NOT_WORD's classes, so a single-unit read calls `🎉` a word character
 *  and blocks the emphasis in `🎉_yay_` that CommonMark opens. */
function ends_with_word_char(s: string): boolean {
  const len = s.length;
  if (len === 0) {
    return false;
  }
  const start = len > 1 && (s.codePointAt(len - 2) ?? 0) > 0xffff ? len - 2 : len - 1;
  return !NOT_WORD.test(s.slice(start));
}

export function prev_is_word_char(p: Parser): boolean {
  return p.textBuf === "" ? p.prev_is_word : ends_with_word_char(p.textBuf);
}

/** Whether a single code point is a word character. The right neighbour of a
 *  delimiter run, where `prev_is_word_char` gives the left one. */
export function is_word_char(ch: string): boolean {
  return ch !== "" && !NOT_WORD.test(ch);
}

/** Emit a leaf token (rule, line break, checkbox) straight to the renderer.
 *  Deliberately does NOT touch the token stack — a leaf has no content, so
 *  nothing may nest inside it. */
export function emit_leaf(p: Parser, token: Token, attr?: Attr): void {
  p.prev_is_word = false;
  p.renderer.add_token(p.renderer.data, token);
  if (attr !== undefined) {
    p.renderer.set_attr(p.renderer.data, attr, "");
  }
  p.renderer.end_token(p.renderer.data);
}

export function add_text(p: Parser): void {
  if (p.textBuf === "") {
    return;
  }
  p.prev_is_word = ends_with_word_char(p.textBuf);
  p.renderer.add_text(p.renderer.data, p.textBuf);
  p.textBuf = "";
}

export function end_token(p: Parser): void {
  p.prev_is_word = false;
  p.len -= 1;
  p.token = p.tokens[p.len] as Token;
  p.renderer.end_token(p.renderer.data);
}

/** The inline tokens: those opened by a delimiter that has a matching closer.
 *  A BLOCK token still open is the streaming tail, which is not a defect and
 *  which the code-block decoration sweeps depend on; an inline one still open
 *  when its block ends never closed at all, and CommonMark reads its delimiter
 *  as literal text. RAW_URL is absent deliberately — it has no delimiter, and
 *  it always terminates on the newline `parser_end` writes. */
const INLINE_TOKENS: ReadonlySet<Token> = new Set<Token>([
  ITALIC_AST,
  ITALIC_UND,
  STRONG_AST,
  STRONG_UND,
  STRIKE,
  CODE_INLINE,
  LINK,
  IMAGE,
  EQUATION_INLINE,
]);

export function is_inline_token(token: Token): boolean {
  return INLINE_TOKENS.has(token);
}

/** Open an inline token, remembering the delimiter that opened it. At the depth
 *  cap nothing is pushed, and every caller then overwrites `pending` with the
 *  character it was handed, so the delimiter is kept as literal text here — the
 *  same reading it gets when the token opens and never closes. */
export function add_inline_token(p: Parser, token: Token, delim: string): void {
  if (add_token(p, token)) {
    p.delims[p.len] = delim;
    return;
  }
  p.textBuf += delim;
}

/** Close an inline token that never met its closer, telling the renderer which
 *  literal to restore in the element's place. */
export function end_token_unresolved(p: Parser): void {
  p.renderer.set_attr(p.renderer.data, UNCLOSED, p.delims[p.len] ?? "");
  end_token(p);
}

/** Close the current token, annotating it when it is an inline token the input
 *  never closed. Every legitimate inline close goes through its own handler, so
 *  reaching an inline token from a BULK close means it never closed. */
function end_token_or_unresolved(p: Parser): void {
  if (is_inline_token(p.tokens[p.len] as Token)) {
    end_token_unresolved(p);
    return;
  }
  end_token(p);
}

/** Whether `n` more pushes fit above depth `len`. `add_token` saturates at
 *  TOKEN_ARRAY_CAP - 1, so a construct that needs several nested tokens to be
 *  well formed has to ask before it opens the outermost one. */
export function has_token_room(len: number, n: number): boolean {
  return len + n <= TOKEN_ARRAY_CAP - 1;
}

/** Push `token` and make it current. Returns false when the stack is already at
 *  TOKEN_ARRAY_CAP, in which case NOTHING changed — `p.token` and `p.pending`
 *  still describe the caller's state. A caller that then re-feeds the character
 *  it was handed re-enters itself with identical state and recurses without
 *  bound, so a caller that re-feeds must consume the text itself instead. */
export function add_token(p: Parser, token: Token): boolean {
  if (
    (p.tokens[p.len] === LIST_ORDERED || p.tokens[p.len] === LIST_UNORDERED) &&
    token !== LIST_ITEM
  ) {
    end_token(p);
  }
  if (p.len >= TOKEN_ARRAY_CAP - 1) {
    return false;
  } // saturate at max depth
  p.prev_is_word = false;
  p.len += 1;
  p.tokens[p.len] = token;
  p.token = token;
  p.renderer.add_token(p.renderer.data, token);
  return true;
}

export function idx_of_token(p: Parser, token: Token, start_idx: number): number {
  while (start_idx <= p.len) {
    if (p.tokens[start_idx] === token) {
      return start_idx;
    }
    start_idx += 1;
  }
  return -1;
}

export function end_tokens_to_len(p: Parser, len: number): void {
  p.fence_start = 0;
  while (p.len > len) {
    end_token_or_unresolved(p);
  }
}

export function end_tokens_to_indent(p: Parser, indent: number): number {
  let idx = 0;
  for (let i = 0; i <= p.len; i += 1) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    indent -= p.spaces[i]!;
    if (indent < 0) {
      break;
    }
    switch (p.tokens[i]) {
      case CODE_BLOCK:
      case CODE_FENCE:
      case BLOCKQUOTE:
      case LIST_ITEM:
        idx = i;
        break;
    }
  }
  while (p.len > idx) {
    end_token_or_unresolved(p);
  }
  return indent;
}

export function continue_or_add_list(p: Parser, list_token: Token): boolean {
  let list_idx = -1;
  let item_idx = -1;
  for (let i = p.blockquote_idx + 1; i <= p.len; i += 1) {
    if (p.tokens[i] === LIST_ITEM) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      if (p.indent_len < p.spaces[i]!) {
        item_idx = -1;
        break;
      }
      item_idx = i;
    } else if (p.tokens[i] === list_token) {
      list_idx = i;
    }
  }
  if (item_idx === -1) {
    if (list_idx === -1) {
      end_tokens_to_len(p, p.blockquote_idx);
      add_token(p, list_token);
      return true;
    }
    end_tokens_to_len(p, list_idx);
    return false;
  }
  end_tokens_to_len(p, item_idx);
  add_token(p, list_token);
  return true;
}

export function add_list_item(p: Parser, prefix_length: number): void {
  add_token(p, LIST_ITEM);
  p.spaces[p.len] = p.indent_len + prefix_length;
  clear_root_pending(p);
  p.token = MAYBE_TASK;
}

const MAYBE_TASK = 103 as Token;
export { MAYBE_TASK };

export function clear_root_pending(p: Parser): void {
  p.indent = "";
  p.indent_len = 0;
  p.pending = "";
  p.prev_is_word = false;
  p.link_depth = 0;
}

export function is_digit(cc: number): boolean {
  return cc >= 48 && cc <= 57;
}

export function is_delimiter_or_number(cc: number): boolean {
  return is_digit(cc) || is_delimiter(cc);
}

function is_delimiter(cc: number): boolean {
  switch (cc) {
    case 32:
    case 58:
    case 59:
    case 41:
    case 44:
    case 33:
    case 46:
    case 63:
    case 93:
    case 10:
      return true;
    default:
      return false;
  }
}

export function heading_from_level(level: number): Token {
  switch (level) {
    case 1:
      return HEADING_1;
    case 2:
      return HEADING_2;
    case 3:
      return HEADING_3;
    case 4:
      return HEADING_4;
    case 5:
      return HEADING_5;
    default:
      return HEADING_6;
  }
}

const ATTR_HTML_MAP: Readonly<Record<number, string>> = {
  [HREF as number]: "href",
  [SRC as number]: "src",
  [LANG as number]: "class",
  [CHECKED as number]: "checked",
  [START as number]: "start",
};

export function attr_to_html_attr(type: Attr): string {
  return ATTR_HTML_MAP[type as number] ?? "";
}
