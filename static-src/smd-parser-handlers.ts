// ---------------------------------------------------------------------------
// Per-context handlers for the streaming markdown parser.
//
// Extracted from smd-parser.ts — each handler returns `true` when it fully
// consumed the character and the outer loop should `continue`; `false` when
// the character should fall through to the common-check section.
// ---------------------------------------------------------------------------

import type { Parser, Token } from "./smd-parser-types.js";
import {
  BLOCKQUOTE,
  CHECKBOX,
  CODE_BLOCK,
  CODE_FENCE,
  CODE_INLINE,
  EQUATION_BLOCK,
  EQUATION_INLINE,
  HEADING_1,
  HEADING_2,
  HEADING_3,
  HEADING_4,
  HEADING_5,
  HEADING_6,
  IMAGE,
  ITALIC_AST,
  ITALIC_UND,
  LINE_BREAK,
  LINK,
  LIST_ITEM,
  LIST_ORDERED,
  LIST_UNORDERED,
  PARAGRAPH,
  RAW_URL,
  RULE,
  STRIKE,
  STRONG_AST,
  STRONG_UND,
  TABLE,
  TABLE_CELL,
  TABLE_ROW,
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
  NEWLINE,
  MAYBE_BR,
  MAYBE_EQ_BLOCK,
  HREF,
  SRC,
  LANG,
  CHECKED,
  START,
} from "./smd-parser-types.js";

export function handleRootContext(p: Parser, char: string, pending_with_char: string): boolean {
  switch (p.pending[0]) {
    case undefined:
      p.pending = char;
      return true;
    case " ":
      p.pending = char;
      p.indent += " ";
      p.indent_len += 1;
      return true;
    case "\t":
      p.pending = char;
      p.indent += "\t";
      p.indent_len += 4;
      return true;
    case "\n":
      // Lists can have an empty line between items.
      if (p.tokens[p.len] === LIST_ITEM && p.token === LINE_BREAK) {
        end_token(p);
        clear_root_pending(p);
        p.pending = char;
        return true;
      }
      end_tokens_to_len(p, p.blockquote_idx);
      clear_root_pending(p);
      p.blockquote_idx = 0;
      p.fence_start = 0;
      p.pending = char;
      return true;
    case "#":
      // Heading: #..###### + space
      if (char === "#") {
        if (p.pending.length < 6) {
          p.pending = pending_with_char;
          return true;
        }
      } else if (char === " ") {
        end_tokens_to_indent(p, p.indent_len);
        add_token(p, heading_from_level(p.pending.length));
        clear_root_pending(p);
        return true;
      }
      break;
    case ">": {
      const next_blockquote_idx = idx_of_token(p, BLOCKQUOTE, p.blockquote_idx + 1);
      if (next_blockquote_idx === -1) {
        end_tokens_to_len(p, p.blockquote_idx);
        p.blockquote_idx += 1;
        p.fence_start = 0;
        add_token(p, BLOCKQUOTE);
      } else {
        p.blockquote_idx = next_blockquote_idx;
      }
      clear_root_pending(p);
      p.pending = char;
      return true;
    }
    case "-":
    case "*":
    case "_": {
      // Horizontal rule: "--- * *** --" variants
      if (p.hr_chars === 0) {
        p.hr_chars = 1;
        p.hr_char = p.pending;
      }
      if (p.hr_chars > 0) {
        if (char === p.hr_char) {
          p.hr_chars += 1;
          p.pending = pending_with_char;
          return true;
        }
        if (char === " ") {
          p.pending = pending_with_char;
          return true;
        }
        if (char === "\n") {
          if (p.hr_chars < 3) {
            // Fall through to unordered-list check below
          } else {
            end_tokens_to_indent(p, p.indent_len);
            p.renderer.add_token(p.renderer.data, RULE);
            p.renderer.end_token(p.renderer.data);
            clear_root_pending(p);
            p.hr_chars = 0;
            return true;
          }
        }
        p.hr_chars = 0;
      }
      // Unordered list: "* foo", "- foo", but not "_ foo"
      if (!p.pending.startsWith("_") && p.pending[1] === " ") {
        continue_or_add_list(p, LIST_UNORDERED);
        add_list_item(p, 2);
        p.write(p, pending_with_char.slice(2));
        return true;
      }
      break;
    }
    case "`":
      // Code fence handling: need 3+ backticks followed by newline.
      if (p.pending.length < 3) {
        if (char === "`") {
          p.pending = pending_with_char;
          p.fence_start = pending_with_char.length;
          return true;
        }
        p.fence_start = 0;
        break;
      }
      if (char === "`") {
        if (p.pending.length === p.fence_start) {
          p.pending = pending_with_char;
          p.fence_start = pending_with_char.length;
        } else {
          add_token(p, PARAGRAPH);
          clear_root_pending(p);
          p.fence_start = 0;
          p.write(p, pending_with_char);
        }
        return true;
      }
      if (char === "\n") {
        end_tokens_to_indent(p, p.indent_len);
        add_token(p, CODE_FENCE);
        if (p.pending.length > p.fence_start) {
          p.renderer.set_attr(p.renderer.data, LANG, p.pending.slice(p.fence_start));
        }
        clear_root_pending(p);
        p.token = NEWLINE;
        return true;
      }
      p.pending = pending_with_char;
      return true;
    case "+":
      if (char !== " ") {
        break;
      }
      continue_or_add_list(p, LIST_UNORDERED);
      add_list_item(p, 2);
      return true;
    case "0":
    case "1":
    case "2":
    case "3":
    case "4":
    case "5":
    case "6":
    case "7":
    case "8":
    case "9":
      if (p.pending.endsWith(".")) {
        if (char !== " ") {
          break;
        }
        if (continue_or_add_list(p, LIST_ORDERED) && p.pending !== "1.") {
          p.renderer.set_attr(p.renderer.data, START, p.pending.slice(0, -1));
        }
        add_list_item(p, p.pending.length + 1);
        return true;
      } else {
        const cc = char.charCodeAt(0);
        if (cc === 46 || is_digit(cc)) {
          p.pending = pending_with_char;
          return true;
        }
      }
      break;
    case "|":
      end_tokens_to_len(p, p.blockquote_idx);
      add_token(p, TABLE);
      add_token(p, TABLE_ROW);
      p.pending = "";
      p.write(p, char);
      return true;
  }

  // Fallthrough: promote pending to a paragraph or code block.
  let to_write = pending_with_char;
  if (p.token === LINE_BREAK) {
    p.token = p.tokens[p.len] as Token;
    p.renderer.add_token(p.renderer.data, LINE_BREAK);
    p.renderer.end_token(p.renderer.data);
  } else if (p.indent_len >= 4) {
    let code_start = 0;
    for (; code_start < 4; code_start += 1) {
      if (p.indent[code_start] === "\t") {
        code_start = code_start + 1;
        break;
      }
    }
    to_write = p.indent.slice(code_start) + pending_with_char;
    add_token(p, CODE_BLOCK);
  } else {
    add_token(p, PARAGRAPH);
  }

  clear_root_pending(p);
  p.write(p, to_write);
  return true;
}

export function handleTable(p: Parser, char: string, pending_with_char: string): boolean {
  if (p.table_state === 1) {
    switch (char) {
      case "-":
      case " ":
      case "|":
      case ":":
        p.pending = pending_with_char;
        return true;
      case "\n":
        p.table_state = 2;
        p.pending = "";
        return true;
      default:
        end_token(p);
        p.table_state = 0;
        return false;
    }
  }
  switch (p.pending) {
    case "|":
      add_token(p, TABLE_ROW);
      p.pending = "";
      p.write(p, char);
      return true;
    case "\n":
      end_token(p);
      p.pending = "";
      p.table_state = 0;
      p.write(p, char);
      return true;
  }
  return false;
}

export function handleTableRow(p: Parser, char: string, _pending_with_char: string): boolean {
  switch (p.pending) {
    case "":
      return false;
    case "|":
      add_token(p, TABLE_CELL);
      end_token(p);
      p.pending = "";
      p.write(p, char);
      return true;
    case "\n":
      end_token(p);
      p.table_state = Math.min(p.table_state + 1, 2);
      p.pending = "";
      p.write(p, char);
      return true;
    default:
      add_token(p, TABLE_CELL);
      p.write(p, char);
      return true;
  }
}

export function handleTableCell(p: Parser, char: string, _pending_with_char: string): boolean {
  void _pending_with_char;
  if (p.pending === "|") {
    add_text(p);
    end_token(p);
    p.pending = "";
    p.write(p, char);
    return true;
  }
  return false;
}

export function handleCodeBlock(p: Parser, char: string, pending_with_char: string): void {
  switch (pending_with_char) {
    case "\n    ":
    case "\n   \t":
    case "\n  \t":
    case "\n \t":
    case "\n\t":
      p.textBuf += "\n";
      p.pending = "";
      return;
    case "\n":
    case "\n ":
    case "\n  ":
    case "\n   ":
      p.pending = pending_with_char;
      return;
    default:
      if (p.pending.length !== 0) {
        add_text(p);
        end_token(p);
        p.pending = char;
      } else {
        p.textBuf += char;
      }
  }
}

export function handleCodeFence(p: Parser, char: string, pending_with_char: string): void {
  switch (char) {
    case "`":
      p.pending = pending_with_char;
      return;
    case "\n":
      if (pending_with_char.length === p.fence_start + p.fence_end + 1) {
        add_text(p);
        end_token(p);
        p.pending = "";
        p.fence_start = 0;
        p.fence_end = 0;
        p.token = NEWLINE;
        return;
      }
      p.token = NEWLINE;
      break;
    case " ":
      if (p.pending.startsWith("\n")) {
        p.pending = pending_with_char;
        p.fence_end += 1;
        return;
      }
      break;
  }
  p.textBuf += p.pending;
  p.pending = char;
  p.fence_end = 1;
}

export function handleCodeInline(p: Parser, char: string, pending_with_char: string): void {
  switch (char) {
    case "`":
      if (pending_with_char.length === p.fence_start + (p.pending.startsWith(" ") ? 1 : 0)) {
        add_text(p);
        end_token(p);
        p.pending = "";
        p.fence_start = 0;
      } else {
        p.pending = pending_with_char;
      }
      return;
    case "\n":
      p.textBuf += p.pending;
      p.pending = "";
      p.token = LINE_BREAK;
      p.blockquote_idx = 0;
      add_text(p);
      return;
    case " ":
      p.textBuf += p.pending;
      p.pending = char;
      return;
    default:
      p.textBuf += pending_with_char;
      p.pending = "";
  }
}

export function handleMaybeTask(p: Parser, char: string, pending_with_char: string): boolean {
  switch (p.pending.length) {
    case 0:
      if (char !== "[") {
        break;
      }
      p.pending = pending_with_char;
      return true;
    case 1:
      if (char !== " " && char !== "x") {
        break;
      }
      p.pending = pending_with_char;
      return true;
    case 2:
      if (char !== "]") {
        break;
      }
      p.pending = pending_with_char;
      return true;
    case 3:
      if (char !== " ") {
        break;
      }
      p.renderer.add_token(p.renderer.data, CHECKBOX);
      if (p.pending[1] === "x") {
        p.renderer.set_attr(p.renderer.data, CHECKED, "");
      }
      p.renderer.end_token(p.renderer.data);
      p.pending = " ";
      return true;
  }
  p.token = p.tokens[p.len] as Token;
  p.pending = "";
  p.write(p, pending_with_char);
  return true;
}

export function handleStrong(p: Parser, char: string): boolean {
  const isUnd = p.token === STRONG_UND;
  const symbol = isUnd ? "_" : "*";
  const italic = isUnd ? ITALIC_UND : ITALIC_AST;

  if (symbol === p.pending) {
    add_text(p);
    if (symbol === char) {
      end_token(p);
      p.pending = "";
      return true;
    }
    add_token(p, italic);
    p.pending = char;
    return true;
  }
  return false;
}

export function handleItalic(p: Parser, char: string, pending_with_char: string): boolean {
  const isUnd = p.token === ITALIC_UND;
  const symbol = isUnd ? "_" : "*";
  const strong = isUnd ? STRONG_UND : STRONG_AST;

  switch (p.pending) {
    case symbol:
      if (symbol === char) {
        if (p.tokens[p.len - 1] === strong) {
          p.pending = pending_with_char;
        } else {
          add_text(p);
          add_token(p, strong);
          p.pending = "";
        }
      } else {
        add_text(p);
        end_token(p);
        p.pending = char;
      }
      return true;
    case symbol + symbol: {
      const italic = p.token;
      add_text(p);
      end_token(p);
      end_token(p);
      if (symbol !== char) {
        add_token(p, italic);
        p.pending = char;
      } else {
        p.pending = "";
      }
      return true;
    }
  }
  return false;
}

export function handleMaybeEqBlock(p: Parser, char: string): void {
  if (char === "\n") {
    add_text(p);
    add_token(p, EQUATION_BLOCK);
    p.pending = "";
  } else {
    p.token = p.tokens[p.len] as Token;
    if (p.pending.startsWith("\\")) {
      p.textBuf += "[";
    } else {
      p.textBuf += "$";
    }
    p.pending = "";
    p.write(p, char);
  }
}

export function handleMaybeURL(p: Parser, char: string, pending_with_char: string): void {
  if (pending_with_char === "http://" || pending_with_char === "https://") {
    add_text(p);
    add_token(p, RAW_URL);
    p.pending = pending_with_char;
    p.textBuf = pending_with_char;
    return;
  }
  const http = "http:/";
  const https = "https:/";
  if (http[p.pending.length] === char || https[p.pending.length] === char) {
    p.pending = pending_with_char;
    return;
  }
  p.token = p.tokens[p.len] as Token;
  p.write(p, char);
}

export function handleLinkOrImage(p: Parser, char: string, pending_with_char: string): boolean {
  if (p.pending === "]") {
    add_text(p);
    if (char === "(") {
      p.pending = pending_with_char;
    } else {
      end_token(p);
      p.pending = char;
    }
    return true;
  }
  if (p.pending.startsWith("]") && p.pending[1] === "(") {
    if (char === ")") {
      const type = p.token === LINK ? HREF : SRC;
      const url = p.pending.slice(2);
      p.renderer.set_attr(p.renderer.data, type, url);
      end_token(p);
      p.pending = "";
    } else {
      p.pending += char;
    }
    return true;
  }
  return false;
}

// Characters a bare URL may not END on: the markdown emphasis and code
// delimiters, plus the trailing punctuation GFM already trims. ASCII
// parentheses are absent deliberately — trimming one needs GFM's balance rule,
// and without it a Wikipedia disambiguation link is cut short.
const RAW_URL_TAIL = new Set(["*", "_", "~", "`", ".", ",", ":", ";", "!", "?", "'", '"']);

/** CJK punctuation: U+3000-U+303F and U+FF00-U+FF65. */
function isCJKPunctuation(ch: string): boolean {
  const cc = ch.charCodeAt(0);
  return (cc >= 0x3000 && cc <= 0x303f) || (cc >= 0xff00 && cc <= 0xff65);
}

/** Split an accumulated bare URL into the href and the trailing run that is not
 *  part of it.
 *
 *  Computed from the WHOLE accumulated pending and only at its END: `.` and `,`
 *  are legal inside a path, so terminating on one would break every domain
 *  name, and a trim derived from the incoming character alone would not be
 *  chunk-size invariant, which the property and fuzz suites assert. */
function splitRawURLTail(pending: string): { url: string; tail: string } {
  let end = pending.length;
  while (end > 0) {
    const ch = pending.charAt(end - 1);
    if (!RAW_URL_TAIL.has(ch) && !isCJKPunctuation(ch)) {
      break;
    }
    end -= 1;
  }
  return { url: pending.slice(0, end), tail: pending.slice(end) };
}

export function handleRawURL(p: Parser, char: string, pending_with_char: string): void {
  if (char === " " || char === "\n" || char === "\\") {
    const { url, tail } = splitRawURLTail(p.pending);
    p.renderer.set_attr(p.renderer.data, HREF, url);
    // `textBuf` holds only the href half already (see below), so the visible
    // text is cut at the SAME point as the href — a link whose label shows a
    // `**` its href no longer carries would be worse than the bug.
    add_text(p);
    end_token(p);
    p.pending = "";
    // Re-fed rather than dropped, so a stripped emphasis close still closes its
    // token and the punctuation still reaches the paragraph. The terminator
    // rides along: with the tail ahead of it, leaving it in `pending` would
    // overwrite whatever the tail left there.
    for (const c of tail + char) {
      p.write(p, c);
    }
  } else if (RAW_URL_TAIL.has(char) || isCJKPunctuation(char)) {
    // HELD, not appended. `add_text` hands text to the renderer, which appends
    // and cannot take it back, and `parser_write` flushes at every chunk
    // boundary — so a character that may turn out to be the tail must not
    // reach `textBuf` until the URL is known to continue past it.
    p.pending = pending_with_char;
  } else {
    // The URL continues, so everything held is part of it after all.
    p.textBuf += splitRawURLTail(p.pending).tail + char;
    p.pending = pending_with_char;
  }
}

export function handleMaybeBR(p: Parser, char: string, pending_with_char: string): boolean {
  if (pending_with_char.startsWith("<br")) {
    if (
      pending_with_char.length === 3 ||
      char === " " ||
      (char === "/" && (pending_with_char.length === 4 || p.pending.endsWith(" ")))
    ) {
      p.pending = pending_with_char;
      return true;
    }
    if (char === ">") {
      add_text(p);
      p.token = p.tokens[p.len] as Token;
      p.renderer.add_token(p.renderer.data, LINE_BREAK);
      p.renderer.end_token(p.renderer.data);
      p.pending = "";
      return true;
    }
  }
  p.token = p.tokens[p.len] as Token;
  p.textBuf += "<";
  p.pending = p.pending.slice(1);
  p.write(p, char);
  return true;
}

export function handleCommon(p: Parser, char: string, pending_with_char: string): boolean {
  switch (p.pending[0]) {
    case "\\":
      if (p.token === IMAGE || p.token === EQUATION_BLOCK || p.token === EQUATION_INLINE) {
        break;
      }
      switch (char) {
        case "(":
          add_text(p);
          add_token(p, EQUATION_INLINE);
          p.pending = "";
          return true;
        case "[":
          p.token = MAYBE_EQ_BLOCK;
          p.pending = pending_with_char;
          return true;
        case "\n":
          p.pending = char;
          return true;
        default: {
          const cc = char.charCodeAt(0);
          p.pending = "";
          p.textBuf +=
            is_digit(cc) || (cc >= 65 && cc <= 90) || (cc >= 97 && cc <= 122)
              ? pending_with_char
              : char;
          return true;
        }
      }
    case "\n":
      switch (p.token) {
        case IMAGE:
        case EQUATION_BLOCK:
        case EQUATION_INLINE:
          break;
        case HEADING_1:
        case HEADING_2:
        case HEADING_3:
        case HEADING_4:
        case HEADING_5:
        case HEADING_6:
          add_text(p);
          end_tokens_to_len(p, p.blockquote_idx);
          p.blockquote_idx = 0;
          p.pending = char;
          return true;
        default:
          add_text(p);
          p.pending = char;
          p.token = LINE_BREAK;
          p.blockquote_idx = 0;
          return true;
      }
      break;
    case "<":
      if (p.token !== IMAGE && p.token !== EQUATION_BLOCK && p.token !== EQUATION_INLINE) {
        add_text(p);
        p.pending = pending_with_char;
        p.token = MAYBE_BR;
        return true;
      }
      break;
    case "`":
      if (p.token === IMAGE) {
        break;
      }
      if (char === "`") {
        p.fence_start += 1;
        p.pending = pending_with_char;
      } else {
        p.fence_start += 1;
        add_text(p);
        add_token(p, CODE_INLINE);
        p.textBuf = "";
        if (char !== " " && char !== "\n") {
          p.textBuf += char;
        }
        p.pending = "";
      }
      return true;
    case "_":
    case "*": {
      if (
        p.token === IMAGE ||
        p.token === EQUATION_BLOCK ||
        p.token === EQUATION_INLINE ||
        p.token === STRONG_AST
      ) {
        break;
      }
      const symbol = p.pending[0] as string;
      const isUnd = symbol === "_";
      const italic = isUnd ? ITALIC_UND : ITALIC_AST;
      const strong = isUnd ? STRONG_UND : STRONG_AST;

      if (p.pending.length === 1) {
        if (symbol === char) {
          p.pending = pending_with_char;
          return true;
        }
        if (char !== " " && char !== "\n") {
          add_text(p);
          add_token(p, italic);
          p.pending = char;
          return true;
        }
      } else {
        if (symbol === char) {
          add_text(p);
          add_token(p, strong);
          add_token(p, italic);
          p.pending = "";
          return true;
        }
        if (char !== " " && char !== "\n") {
          add_text(p);
          add_token(p, strong);
          p.pending = char;
          return true;
        }
      }
      break;
    }
    case "~":
      if (p.token !== IMAGE && p.token !== STRIKE) {
        if (p.pending === "~") {
          if (char === "~") {
            p.pending = pending_with_char;
            return true;
          }
        } else if (char !== " " && char !== "\n") {
          add_text(p);
          add_token(p, STRIKE);
          p.pending = char;
          return true;
        }
      }
      break;
    case "$":
      // The equation guard every sibling case in this switch already carries,
      // and its absence was a real defect: inside an OPEN equation the closing
      // `$$` fell through to the opener below, re-entered MAYBE_EQ_BLOCK and
      // opened a nested block that never closed. So `$$\nx^2\n$$` rendered its
      // source as text with an empty second wrapper inside it — invisible while
      // the tokens mapped to meaningless custom elements, and wrong the moment
      // they started rendering as mathematics.
      if (
        p.token !== IMAGE &&
        p.token !== STRIKE &&
        p.token !== EQUATION_BLOCK &&
        p.token !== EQUATION_INLINE &&
        p.pending === "$"
      ) {
        if (char === "$") {
          p.token = MAYBE_EQ_BLOCK;
          p.pending = pending_with_char;
          return true;
        }
        if (is_delimiter_or_number(char.charCodeAt(0))) {
          break;
        }
        add_text(p);
        add_token(p, EQUATION_INLINE);
        p.pending = char;
        return true;
      }
      break;
    case "[":
      if (
        p.token !== IMAGE &&
        p.token !== LINK &&
        p.token !== EQUATION_BLOCK &&
        p.token !== EQUATION_INLINE &&
        char !== "]"
      ) {
        add_text(p);
        add_token(p, LINK);
        p.pending = char;
        return true;
      }
      break;
    case "!":
      if (p.token !== IMAGE && char === "[") {
        add_text(p);
        add_token(p, IMAGE);
        p.pending = "";
        return true;
      }
      break;
    case " ":
      if (p.pending.length === 1 && char === " ") {
        return true; // trim consecutive spaces
      }
      break;
  }
  return false;
}
