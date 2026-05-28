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
export const TOKENS = {
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

// Re-export individual constants for backward compatibility.
export const DOCUMENT: Token = TOKENS.DOCUMENT;
export const PARAGRAPH: Token = TOKENS.PARAGRAPH;
export const HEADING_1: Token = TOKENS.HEADING_1;
export const HEADING_2: Token = TOKENS.HEADING_2;
export const HEADING_3: Token = TOKENS.HEADING_3;
export const HEADING_4: Token = TOKENS.HEADING_4;
export const HEADING_5: Token = TOKENS.HEADING_5;
export const HEADING_6: Token = TOKENS.HEADING_6;
export const CODE_BLOCK: Token = TOKENS.CODE_BLOCK;
export const CODE_FENCE: Token = TOKENS.CODE_FENCE;
export const CODE_INLINE: Token = TOKENS.CODE_INLINE;
export const ITALIC_AST: Token = TOKENS.ITALIC_AST;
export const ITALIC_UND: Token = TOKENS.ITALIC_UND;
export const STRONG_AST: Token = TOKENS.STRONG_AST;
export const STRONG_UND: Token = TOKENS.STRONG_UND;
export const STRIKE: Token = TOKENS.STRIKE;
export const LINK: Token = TOKENS.LINK;
export const RAW_URL: Token = TOKENS.RAW_URL;
export const IMAGE: Token = TOKENS.IMAGE;
export const BLOCKQUOTE: Token = TOKENS.BLOCKQUOTE;
export const LINE_BREAK: Token = TOKENS.LINE_BREAK;
export const RULE: Token = TOKENS.RULE;
export const LIST_UNORDERED: Token = TOKENS.LIST_UNORDERED;
export const LIST_ORDERED: Token = TOKENS.LIST_ORDERED;
export const LIST_ITEM: Token = TOKENS.LIST_ITEM;
export const CHECKBOX: Token = TOKENS.CHECKBOX;
export const TABLE: Token = TOKENS.TABLE;
export const TABLE_ROW: Token = TOKENS.TABLE_ROW;
export const TABLE_CELL: Token = TOKENS.TABLE_CELL;
export const EQUATION_BLOCK: Token = TOKENS.EQUATION_BLOCK;
export const EQUATION_INLINE: Token = TOKENS.EQUATION_INLINE;
export const NEWLINE: Token = TOKENS.NEWLINE;
export const MAYBE_BR: Token = TOKENS.MAYBE_BR;
export const MAYBE_EQ_BLOCK: Token = TOKENS.MAYBE_EQ_BLOCK;

// --- Attr enum ---

/** All valid attribute values. */
export const ATTRS = {
  HREF: 1,
  SRC: 2,
  LANG: 4,
  CHECKED: 8,
  START: 16,
} as const satisfies Record<string, number>;

/** Attr type derived from the ATTRS constant object. */
export type Attr = (typeof ATTRS)[keyof typeof ATTRS];

export const HREF: Attr = ATTRS.HREF;
export const SRC: Attr = ATTRS.SRC;
export const LANG: Attr = ATTRS.LANG;
export const CHECKED: Attr = ATTRS.CHECKED;
export const START: Attr = ATTRS.START;

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
  textBuf: string[];
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
  write: (p: Parser, chunk: string) => void;
}

// --- Pure utility functions ---

export function add_text(p: Parser): void {
  if (p.textBuf.length === 0) {
    return;
  }
  p.renderer.add_text(p.renderer.data, p.textBuf.join(""));
  p.textBuf.length = 0;
}

export function end_token(p: Parser): void {
  p.len -= 1;
  p.token = p.tokens[p.len] as Token;
  p.renderer.end_token(p.renderer.data);
}

export function add_token(p: Parser, token: Token): void {
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
