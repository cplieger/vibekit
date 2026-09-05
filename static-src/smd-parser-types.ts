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
} as const satisfies Record<string, number>;

/** Attr type derived from the ATTRS constant object. */
export type Attr = (typeof ATTRS)[keyof typeof ATTRS];

export const { HREF, SRC, LANG, CHECKED, START } = ATTRS;

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
  write: (p: Parser, chunk: string) => void;
}

// --- Pure utility functions ---

/** CommonMark's word character: neither Unicode whitespace nor Unicode
 *  punctuation, which makes every letter and digit in every script one. */
const NOT_WORD = /[\s\p{P}\p{S}]/u;

function is_word_char(ch: string): boolean {
  return ch !== "" && !NOT_WORD.test(ch);
}

export function prev_is_word_char(p: Parser): boolean {
  return p.textBuf === "" ? p.prev_is_word : is_word_char(p.textBuf.charAt(p.textBuf.length - 1));
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
  p.prev_is_word = is_word_char(p.textBuf.charAt(p.textBuf.length - 1));
  p.renderer.add_text(p.renderer.data, p.textBuf);
  p.textBuf = "";
}

export function end_token(p: Parser): void {
  p.prev_is_word = false;
  p.len -= 1;
  p.token = p.tokens[p.len] as Token;
  p.renderer.end_token(p.renderer.data);
}

export function add_token(p: Parser, token: Token): void {
  p.prev_is_word = false;
  if (
    (p.tokens[p.len] === LIST_ORDERED || p.tokens[p.len] === LIST_UNORDERED) &&
    token !== LIST_ITEM
  ) {
    end_token(p);
  }
  if (p.len >= TOKEN_ARRAY_CAP - 1) {
    return;
  } // saturate at max depth
  p.len += 1;
  p.tokens[p.len] = token;
  p.token = token;
  p.renderer.add_token(p.renderer.data, token);
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
    end_token(p);
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
    end_token(p);
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
