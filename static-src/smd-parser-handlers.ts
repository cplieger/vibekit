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
  emit_leaf,
  end_token,
  add_token,
  add_inline_token,
  has_token_room,
  is_inline_token,
  idx_of_token,
  end_tokens_to_len,
  end_tokens_to_indent,
  continue_or_add_list,
  add_list_item,
  clear_root_pending,
  is_digit,
  is_delimiter_or_number,
  prev_is_word_char,
  is_word_char,
  heading_from_level,
  NEWLINE,
  MAYBE_BR,
  MAYBE_EQ_BLOCK,
  HREF,
  SRC,
  LANG,
  CHECKED,
  START,
  ALIGN,
  TITLE,
} from "./smd-parser-types.js";

/** Everything a delimiter row may contain. One character outside this set rules
 *  the line out, which is what bounds the held candidate to two lines. Must stay
 *  a superset of what `is_delimiter_row` accepts, or a row the test would take
 *  never reaches it and the whole table is lost. */
const DELIMITER_ROW_CHARS = new Set(["-", " ", "\t", "|", ":"]);

/** A table row's cells, split the way the row handlers split them: a leading `|`
 *  opens the row rather than a cell, a trailing one closes the last cell rather
 *  than opening another, and neither a backslash-escaped pipe nor one inside a
 *  code span is a delimiter.
 *
 *  Those two are the halves a plain `split("|")` gets wrong, and both matter for
 *  the same reason: this count is what the delimiter row is measured against, so
 *  a count the CELL WALKER would never produce loses the whole table or opens
 *  one whose header and body disagree. `handleCommon`'s `\` arm consumes `\|`
 *  into the cell text, and CODE_INLINE is dispatched with `actionAlwaysContinue`
 *  so the `|` arm never runs inside a code span — which is why vibekit keeps
 *  `` `a | b` `` as ONE cell where the GFM reference splits it into two, and why
 *  the delimiter row has to agree with one cell rather than two. */
function table_row_cells(row: string): string[] {
  const body = row.replace(/^[ \t]+/u, "").replace(/[ \t]+$/u, "");
  const cells: string[] = [];
  let cell = "";
  let fence = 0;
  for (let i = body.startsWith("|") ? 1 : 0; i < body.length; i += 1) {
    const ch = body.charAt(i);
    if (ch === "\\" && i + 1 < body.length) {
      cell += ch + body.charAt(i + 1);
      i += 1;
    } else if (ch === "`") {
      // A run closes the span only at its own length, which is what makes
      // ``` ``a | b`` ``` one cell as well.
      let run = 1;
      while (body.charAt(i + run) === "`") {
        run += 1;
      }
      if (fence === 0) {
        fence = run;
      } else if (fence === run) {
        fence = 0;
      }
      cell += "`".repeat(run);
      i += run - 1;
    } else if (ch === "|" && fence === 0) {
      cells.push(cell);
      cell = "";
    } else {
      cell += ch;
    }
  }
  // A trailing `|` closed the last cell, so the empty remainder is not one.
  if (cell !== "") {
    cells.push(cell);
  }
  return cells;
}

/** GFM's delimiter row: a cell count matching the header's, and every cell a run
 *  of dashes with an optional colon on either side.
 *
 *  A pipe is deliberately NOT required, so a single-column header over a bare
 *  `---` is a table. Requiring one would read that as a thematic break, where
 *  the GFM reference reads a table; the multi-column case needs no rule of its
 *  own, since a pipeless line cannot match a header of two cells or more. */
function is_delimiter_row(cells: string[], header_cells: number): boolean {
  if (cells.length === 0 || cells.length !== header_cells) {
    return false;
  }
  return cells.every((cell) => /^[ \t]*:?-+:?[ \t]*$/u.test(cell));
}

/** Per-column `text-align` from the delimiter row's colons, comma-joined, or ""
 *  when the row asks for no alignment at all. */
function column_alignments(cells: string[]): string {
  const out = cells.map((cell) => {
    const c = cell.trim();
    if (!c.startsWith(":")) {
      return c.endsWith(":") ? "right" : "";
    }
    return c.endsWith(":") ? "center" : "left";
  });
  return out.some((align) => align !== "") ? out.join(",") : "";
}

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
      if (!has_token_room(p.len, 1)) {
        // Past the cap no block opens, so this line break is the only thing left
        // to separate one line of text from the next.
        p.textBuf += "\n";
      }
      p.pending = char;
      return true;
    case "#":
      // Heading: #..###### + space, or a bare run, which CommonMark 4.2 reads
      // as an empty heading.
      if (char === "#") {
        if (p.pending.length < 6) {
          p.pending = pending_with_char;
          return true;
        }
      } else if (char === " " || char === "\n") {
        end_tokens_to_indent(p, p.indent_len);
        const opened = add_token(p, heading_from_level(p.pending.length));
        if (!opened) {
          p.textBuf += p.indent + pending_with_char;
        }
        clear_root_pending(p);
        if (opened) {
          p.atx_close = true;
          if (char === "\n") {
            // Handed on rather than consumed, or the next line's text lands
            // inside the heading this newline has to close.
            p.pending = char;
          }
        }
        return true;
      }
      break;
    case ">": {
      const next_blockquote_idx = idx_of_token(p, BLOCKQUOTE, p.blockquote_idx + 1);
      if (next_blockquote_idx === -1) {
        end_tokens_to_len(p, p.blockquote_idx);
        if (!add_token(p, BLOCKQUOTE)) {
          // Past the cap the line is text, and so is the `>` that started it.
          // `blockquote_idx` stays put: advancing it past `len` leaves every
          // later search for an enclosing block starting above the stack.
          break;
        }
        p.blockquote_idx += 1;
        p.fence_start = 0;
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
            emit_leaf(p, RULE);
            clear_root_pending(p);
            p.hr_chars = 0;
            return true;
          }
        }
        p.hr_chars = 0;
      }
      // Unordered list: "* foo", "- foo", but not "_ foo"
      if (!p.pending.startsWith("_") && p.pending[1] === " ") {
        if (continue_or_add_list(p, LIST_UNORDERED, "") === "no_room") {
          break;
        }
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
        } else if (add_token(p, PARAGRAPH)) {
          clear_root_pending(p);
          p.fence_start = 0;
          p.write(p, pending_with_char);
        } else {
          p.textBuf += pending_with_char;
          clear_root_pending(p);
          p.fence_start = 0;
        }
        return true;
      }
      if (char === "\n") {
        end_tokens_to_indent(p, p.indent_len);
        if (!add_token(p, CODE_FENCE)) {
          // The info string must not be emitted here: with no fence to carry it
          // the attribute lands on the enclosing element as its `class`.
          p.textBuf += p.indent + pending_with_char;
          clear_root_pending(p);
          p.fence_start = 0;
          return true;
        }
        if (p.pending.length > p.fence_start) {
          p.renderer.set_attr(p.renderer.data, LANG, p.pending.slice(p.fence_start));
        }
        clear_root_pending(p);
        p.fence_end = 0;
        p.token = NEWLINE;
        return true;
      }
      p.pending = pending_with_char;
      return true;
    case "+":
      if (char !== " ") {
        break;
      }
      if (continue_or_add_list(p, LIST_UNORDERED, "") === "no_room") {
        break;
      }
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
      if (p.pending.endsWith(".") || p.pending.endsWith(")")) {
        if (char !== " ") {
          break;
        }
        const number = p.pending.slice(0, -1);
        const opened = continue_or_add_list(p, LIST_ORDERED, p.pending.slice(-1));
        if (opened === "no_room") {
          break;
        }
        if (opened === "created" && number !== "1") {
          p.renderer.set_attr(p.renderer.data, START, number);
        }
        add_list_item(p, p.pending.length + 1);
        return true;
      } else {
        const cc = char.charCodeAt(0);
        // CommonMark 5.2 bounds the marker at nine digits, so a longer run of
        // them is prose — an order number, a phone number, a byte count.
        if (cc === 46 || cc === 41 || (is_digit(cc) && p.pending.length < 9)) {
          p.pending = pending_with_char;
          return true;
        }
      }
      break;
    case "|": {
      // GFM needs a delimiter row on the line AFTER the header, so the header is
      // HELD in `pending` until the next line decides. Painting the pipes as a
      // paragraph and then restructuring them into a table would change the
      // height of content the reader has already scrolled past, which
      // `overflow-anchor: none` gives no net for; one late insertion does not.
      //
      // A rejected candidate needs no state: the promotion below consumes its
      // first line as paragraph text and the rest is offered to this arm again,
      // which is how a table starting on the SECOND line of a rejected candidate
      // still opens, as GFM opens it.
      const nl = p.pending.indexOf("\n");
      if (nl !== -1 && char === "\n") {
        const header = p.pending.slice(0, nl);
        const delim = p.pending.slice(nl + 1);
        const cells = table_row_cells(delim);
        if (!is_delimiter_row(cells, table_row_cells(header).length)) {
          break;
        }
        end_tokens_to_len(p, p.blockquote_idx);
        // TABLE, TABLE_ROW and TABLE_CELL must ALL fit: a failed row or cell
        // push leaves the cell text as a text node directly inside the
        // `<table>`, and `handleTableRow`'s default arm re-feeds its character,
        // so a failure there recurses without bound. Past the depth limit the
        // held lines are text, as they are for a paragraph.
        if (!has_token_room(p.len, 3)) {
          break;
        }
        add_token(p, TABLE);
        const align = column_alignments(cells);
        if (align !== "") {
          p.renderer.set_attr(p.renderer.data, ALIGN, align);
        }
        add_token(p, TABLE_ROW);
        p.pending = "";
        // Replayed through the row handlers rather than emitted here, so the
        // header and the delimiter row go through exactly the path they took
        // before the deferral existed.
        p.write(p, header.slice(1) + "\n" + delim + "\n");
        return true;
      }
      // No line follows the newline `parser_end` writes, so a candidate still
      // being held at end of input can never become a table.
      if (p.at_end) {
        break;
      }
      if (nl === -1 || DELIMITER_ROW_CHARS.has(char)) {
        p.pending = pending_with_char;
        return true;
      }
      break;
    }
  }

  // Fallthrough: promote pending to a paragraph or code block.
  let to_write = pending_with_char;
  let pushed = true;
  if (p.token === LINE_BREAK) {
    p.token = p.tokens[p.len] as Token;
    emit_leaf(p, LINE_BREAK);
  } else {
    if (p.indent_len >= 4) {
      let code_start = 0;
      for (; code_start < 4; code_start += 1) {
        if (p.indent[code_start] === "\t") {
          code_start = code_start + 1;
          break;
        }
      }
      to_write = p.indent.slice(code_start) + pending_with_char;
      pushed = add_token(p, CODE_BLOCK);
    } else {
      pushed = add_token(p, PARAGRAPH);
    }
    if (!pushed) {
      // No block opened, so the leading whitespace one would have consumed is
      // text as well — the four columns that mark indented code included.
      // Promotion runs once per two characters past the cap, so without this a
      // space landing on a promotion boundary disappears.
      to_write = p.indent + pending_with_char;
    }
  }

  clear_root_pending(p);
  if (!pushed) {
    // The stack is at its cap, so nothing changed and re-feeding would re-enter
    // this function with identical state. Past the depth limit the line is text.
    p.textBuf += to_write;
    if (to_write.endsWith("\n")) {
      // The newline arm never ran for this newline, and it owns the two pieces
      // of line-scoped state below. Left alone, `blockquote_idx` still names the
      // blockquote the PREVIOUS line ended in, so the next line's `>` run
      // searches above the stack and reads as literal text.
      p.blockquote_idx = 0;
      p.fence_start = 0;
    }
    return true;
  }
  p.write(p, to_write);
  return true;
}

/** The ATX closing sequence at the end of a held run: whitespace, an optional
 *  run of `#`, then whitespace. Anchored at the end, so `" ## #"` loses only the
 *  LAST run and keeps the first as content, which is CommonMark 4.2's reading. */
const ATX_CLOSE_TAIL = /[ \t]*(?:#+[ \t]*)?$/u;

/** Hold a candidate ATX closing sequence, so a `#` run that turns out to be
 *  syntax never reaches the renderer as text.
 *
 *  The hold is at most one whitespace run plus a `#` run plus another
 *  whitespace run, and it is released on the newline that ends the line — so a
 *  heading's text lags by the length of its own trailing run and by nothing
 *  else. Every other character falls through, which leaves `handleCommon`'s
 *  consecutive-space trim and every inline construct inside a heading exactly as
 *  they were. */
export function handleHeading(p: Parser, char: string, pending_with_char: string): boolean {
  if (char === "\n") {
    // A whitespace-only hold is the line's trailing whitespace, which 4.2 strips
    // from a heading's content whether or not a closing run follows it.
    if (p.atx_close || /^[ \t]+$/u.test(p.pending)) {
      p.textBuf += p.pending.replace(ATX_CLOSE_TAIL, "");
      p.pending = "";
      p.atx_close = false;
    }
    return false;
  }
  if (char === "#") {
    // A closing run must be preceded by whitespace. `atx_close` covers the run
    // that starts the content, whose whitespace was the opening sequence's own.
    if (p.atx_close || /^[ \t]+$/u.test(p.pending)) {
      p.pending = pending_with_char;
      p.atx_close = true;
      return true;
    }
    p.atx_close = false;
    return false;
  }
  if ((char === " " || char === "\t") && p.atx_close && p.pending.includes("#")) {
    p.pending = pending_with_char;
    return true;
  }
  if (char !== " " && char !== "\t") {
    p.atx_close = false;
  }
  return false;
}

export function handleTable(p: Parser, char: string, pending_with_char: string): boolean {
  if (p.table_state === 1) {
    if (char === "\n") {
      p.table_state = 2;
      p.pending = "";
      return true;
    }
    if (DELIMITER_ROW_CHARS.has(char)) {
      p.pending = pending_with_char;
      return true;
    }
    end_token(p);
    p.table_state = 0;
    return false;
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

function is_blank(s: string): boolean {
  for (const ch of s) {
    if (ch !== " " && ch !== "\t") {
      return false;
    }
  }
  return true;
}

/** The row and cell pushes here are unchecked because the `|` arm in
 *  `handleRootContext` reserves room for all three table tokens before it opens
 *  the table. */
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
      // GFM makes the trailing pipe optional and trims every cell, so whitespace
      // between the last pipe and the newline is not a cell. Opening one for it
      // left the row unterminated: `table_state` never advanced, the delimiter
      // row was read as a fresh table, and which of the two happened depended on
      // where the write boundary fell.
      if (is_blank(p.pending)) {
        if (char === "\n") {
          p.pending = char;
          return true;
        }
        if (char === " " || char === "\t") {
          return true;
        }
      }
      add_token(p, TABLE_CELL);
      p.write(p, char);
      return true;
  }
}

export function handleTableCell(p: Parser, char: string, _pending_with_char: string): boolean {
  if (p.pending === "|") {
    add_text(p);
    end_token(p);
    p.pending = "";
    p.write(p, char);
    return true;
  }
  if (p.pending === "\n") {
    // GFM makes the trailing pipe optional, so a newline ends the last cell as
    // well as the row. `handleTableRow` has always had this arm; without the
    // cell's own the newline reached handleCommon and became a LINE BREAK, the
    // row never terminated, and the delimiter row underneath was read as more
    // header cells.
    add_text(p);
    end_token(p);
    p.pending = "\n";
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

/** Whether a held line is a closing fence per CommonMark 4.5: at most three
 *  spaces of indent, then a run of at least `fence_start` backticks, then
 *  nothing but spaces and tabs. `line` may carry the newline that started it. */
function is_fence_close(line: string, fence_start: number): boolean {
  let i = line.startsWith("\n") ? 1 : 0;
  const indent_start = i;
  while (line[i] === " ") {
    i += 1;
  }
  if (i - indent_start > 3) {
    return false;
  }
  const run_start = i;
  while (line[i] === "`") {
    i += 1;
  }
  if (i - run_start < fence_start) {
    return false;
  }
  while (i < line.length) {
    if (line[i] !== " " && line[i] !== "\t") {
      return false;
    }
    i += 1;
  }
  return true;
}

export function handleCodeFence(p: Parser, char: string, pending_with_char: string): void {
  // `p.fence_end` is the current line's verdict: 0 while the line could still be
  // the closing fence, 1 once a character ruled it out. While it could, the line
  // is HELD in `pending` — a closing fence swallows the newline before it, so
  // the newline that started the line is held with it and only reaches `textBuf`
  // if the line turns out to be content.
  if (p.fence_end === 0) {
    switch (char) {
      case "`":
      case " ":
      case "\t":
        p.pending = pending_with_char;
        return;
      case "\n":
        if (is_fence_close(p.pending, p.fence_start)) {
          add_text(p);
          end_token(p);
          p.pending = "";
          p.fence_start = 0;
          p.fence_end = 0;
          p.token = NEWLINE;
          return;
        }
        break;
      default:
        p.fence_end = 1;
    }
  }
  if (char === "\n") {
    p.textBuf += p.pending;
    p.pending = char;
    p.token = NEWLINE;
    p.fence_end = 0;
    return;
  }
  p.textBuf += pending_with_char;
  p.pending = "";
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
      emit_leaf(p, CHECKBOX, p.pending[1] === "x" ? CHECKED : undefined);
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
    if (symbol === char) {
      add_text(p);
      end_token(p);
      p.pending = "";
      return true;
    }
    // Same delimiter-run rule as handleCommon, on the nested open only: the
    // close above must stay ungated, or `__run_progress__` would never close.
    // Falling through leaves the `_` to handleCommon's literal path.
    if (isUnd && prev_is_word_char(p)) {
      return false;
    }
    add_text(p);
    add_inline_token(p, italic, symbol);
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
        } else if (isUnd && prev_is_word_char(p)) {
          // Same rule again, on the nested open. handleCommon cannot be
          // delegated to here: it grows `pending` to `__`, which the next
          // character reads as "close both" and fires two end_token calls
          // against one open token. Consume the run as text instead.
          p.textBuf += pending_with_char;
          p.pending = "";
        } else {
          add_text(p);
          add_inline_token(p, strong, symbol + symbol);
          p.pending = "";
        }
      } else if (isUnd && prev_is_word_char(p) && is_word_char(char)) {
        // CommonMark 6.2 rule 6: a `_` run with a word character on both sides is
        // both left- and right-flanking, so it cannot close either. Refused IN
        // PLACE rather than delegated to handleCommon, which would grow `pending`
        // to `__`, have the next character read that as "close both", and fire
        // two end_token calls against one open token. The token stays open and is
        // unwrapped at block close, restoring the whole run as literal text.
        p.textBuf += p.pending;
        p.pending = char;
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
        add_inline_token(p, italic, symbol);
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
    if (!add_token(p, EQUATION_BLOCK)) {
      // Past the cap the opener is literal text, and the token that would have
      // consumed the rest of the expression does not exist.
      p.token = p.tokens[p.len] as Token;
      p.textBuf += p.pending + char;
      p.pending = "";
      return;
    }
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
    if (!add_token(p, RAW_URL)) {
      // Past the cap the scheme is text like the rest of the line.
      p.token = p.tokens[p.len] as Token;
      p.textBuf += pending_with_char;
      p.pending = "";
      return;
    }
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

/** Index of the innermost open token from `wanted`, reachable through inline
 *  tokens only, or -1. Used where a structural delimiter has to win over an
 *  inline token that never closed: CommonMark resolves a link before emphasis,
 *  and GFM splits a table row into cells before parsing inline content at all.
 *  Stopping at the first other block token keeps the search inside one block. */
function enclosing_idx(p: Parser, wanted: readonly Token[]): number {
  for (let i = p.len; i > 0; i -= 1) {
    const token = p.tokens[i] as Token;
    if (wanted.includes(token)) {
      return i;
    }
    if (!is_inline_token(token)) {
      return -1;
    }
  }
  return -1;
}

const LABEL_TOKENS: readonly Token[] = [LINK, IMAGE];
const CELL_TOKENS: readonly Token[] = [TABLE_CELL];

/** The closer for each title delimiter CommonMark 6.3 allows. */
const TITLE_CLOSE: Readonly<Record<string, string>> = { '"': '"', "'": "'", "(": ")" };

/** Whether a backslash before `ch` is an escape that consumes it. Mirrors
 *  handleCommon's `\` arm, which keeps the backslash before an alphanumeric and
 *  drops it before anything else. */
function is_escaped_char(ch: string): boolean {
  const cc = ch.charCodeAt(0);
  return !(is_digit(cc) || (cc >= 65 && cc <= 90) || (cc >= 97 && cc <= 122));
}

interface Destination {
  url: string;
  title: string | null;
  /** Whether a `)` belongs to the run rather than ending it: an unbalanced `(`
   *  in the destination, or a `(…)` title whose closer it would be. The caller
   *  keeps accumulating instead of closing the link. */
  open: boolean;
}

/** Split the run between `](` and a candidate closing `)` into a destination and
 *  a title.
 *
 *  A run that does not read as `destination [whitespace title] whitespace*`
 *  keeps the whole run as the destination, which is what every link got before
 *  titles were parsed — a destination containing a space is the common shape
 *  that reaches it, and routing it to a literal instead would change every real
 *  link written with an unencoded space. The backslash keeps its literal
 *  reading in the destination for the same reason, and is unescaped only in the
 *  title, which had no reading to preserve. */
function scan_destination(run: string): Destination {
  const whole: Destination = { url: run, title: null, open: false };
  let i = 0;
  let url = "";
  if (run.startsWith("<")) {
    for (i = 1; i < run.length && run.charAt(i) !== ">"; i += 1) {
      if (run.charAt(i) === "\\" && i + 1 < run.length) {
        url += run.charAt(i);
        i += 1;
      }
      url += run.charAt(i);
    }
    if (i === run.length) {
      return whole;
    }
    i += 1;
  } else {
    let depth = 0;
    for (; i < run.length; i += 1) {
      const ch = run.charAt(i);
      if (ch === "\\" && i + 1 < run.length) {
        url += ch + run.charAt(i + 1);
        i += 1;
        continue;
      }
      if (ch === " " || ch === "\t") {
        break;
      }
      if (ch === "(") {
        depth += 1;
      } else if (ch === ")") {
        depth -= 1;
      }
      url += ch;
    }
    if (depth > 0) {
      return { url: run, title: null, open: true };
    }
  }
  const gap_start = i;
  while (run.charAt(i) === " " || run.charAt(i) === "\t") {
    i += 1;
  }
  if (i === run.length) {
    return { url, title: null, open: false };
  }
  const close = TITLE_CLOSE[run.charAt(i)];
  if (close === undefined || i === gap_start) {
    return whole;
  }
  let title = "";
  for (i += 1; i < run.length; i += 1) {
    const ch = run.charAt(i);
    if (ch === "\\" && i + 1 < run.length) {
      const next = run.charAt(i + 1);
      title += is_escaped_char(next) ? next : ch + next;
      i += 1;
      continue;
    }
    if (ch === close) {
      break;
    }
    title += ch;
  }
  if (i === run.length) {
    // A `(…)` title is closed by the `)` the caller is holding, so keep
    // accumulating. A quoted one cannot be, so the run is a destination.
    return close === ")" ? { url: run, title: null, open: true } : whole;
  }
  i += 1;
  while (run.charAt(i) === " " || run.charAt(i) === "\t") {
    i += 1;
  }
  return i === run.length ? { url, title, open: false } : whole;
}

export function handleLinkOrImage(p: Parser, char: string, pending_with_char: string): boolean {
  // CommonMark 6.3 allows balanced brackets in a label, so the label ends at a
  // `]` only when no `[` is open. A counter rather than a re-scan: a label that
  // fails to close would make every later `[` restart the scan, which is
  // quadratic on bracket-heavy input and a streaming parser sees every prefix.
  if (p.pending === "[") {
    p.link_depth += 1;
    p.textBuf += p.pending;
    p.pending = char;
    return true;
  }
  if (p.pending === "]") {
    if (p.link_depth > 0) {
      p.link_depth -= 1;
      p.textBuf += p.pending;
      p.pending = char;
      return true;
    }
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
    if (char !== ")") {
      p.pending += char;
      return true;
    }
    const dest = scan_destination(p.pending.slice(2));
    if (dest.open) {
      p.pending += char;
      return true;
    }
    const type = p.token === LINK ? HREF : SRC;
    p.renderer.set_attr(p.renderer.data, type, dest.url);
    if (dest.title !== null) {
      p.renderer.set_attr(p.renderer.data, TITLE, dest.title);
    }
    end_token(p);
    p.pending = "";
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

/** The CJK marks that separate clauses rather than end sentences. Sentence
 *  enders (`。．！？…｡`) are deliberately absent: a URL genuinely containing one
 *  is indistinguishable from a sentence that ends after a URL, so the only
 *  honest answer there is to leave the run alone. */
const CJK_SEPARATOR = new Set(["，", "、", "；", "：", "､"]);

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

/** Close the open RAW_URL at the last character that can belong to it, then
 *  re-feed the trimmed tail plus `trailing`.
 *
 *  Re-fed rather than dropped, so a stripped emphasis close still closes its
 *  token and the punctuation still reaches the paragraph. The terminator rides
 *  along: with the tail ahead of it, leaving it in `pending` would overwrite
 *  whatever the tail left there. */
function endRawURL(p: Parser, trailing: string): void {
  const { url, tail } = splitRawURLTail(p.pending);
  p.renderer.set_attr(p.renderer.data, HREF, url);
  // `textBuf` holds only the href half already (see below), so the visible text
  // is cut at the SAME point as the href — a link whose label shows a `**` its
  // href no longer carries would be worse than the bug.
  add_text(p);
  end_token(p);
  p.pending = "";
  for (const c of tail + trailing) {
    p.write(p, c);
  }
}

export function handleRawURL(p: Parser, char: string, pending_with_char: string): void {
  if (char === " " || char === "\n" || char === "\\") {
    endRawURL(p, char);
  } else if (RAW_URL_TAIL.has(char) || isCJKPunctuation(char)) {
    // A CJK separator immediately followed by a backtick is the one shape that
    // PROVES the URL ended: RFC 3986 excludes the backtick, so it cannot be in
    // the URL. Letting it ride is not just a wrong href — it eats the opening
    // delimiter of the code span that follows and shifts every later backtick
    // pairing in the paragraph. Punctuation followed by anything else is not
    // evidence, because real URLs carry it raw (…/wiki/苹果（公司）,
    // …/wiki/我，机器人, …/wiki/モーニング娘。).
    if (char === "`" && CJK_SEPARATOR.has(p.pending.charAt(p.pending.length - 1))) {
      endRawURL(p, char);
      return;
    }
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
      emit_leaf(p, LINE_BREAK);
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
          add_inline_token(p, EQUATION_INLINE, "\\(");
          p.pending = "";
          return true;
        case "[":
          p.token = MAYBE_EQ_BLOCK;
          p.pending = pending_with_char;
          return true;
        case "\n":
          if (p.at_end) {
            // Nothing follows the synthetic newline, so this is a literal
            // backslash rather than a hard line break.
            p.textBuf += p.pending;
          }
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
      if (char === "\n") {
        // A code span cannot open on the last character of a line, so the held
        // backticks are literal text. Without this they were consumed into a
        // token that never received any content.
        p.fence_start = 0;
        break;
      }
      if (char === "`") {
        p.fence_start += 1;
        p.pending = pending_with_char;
      } else {
        p.fence_start += 1;
        add_text(p);
        // The space after the opening run is dropped from the content, so it is
        // part of the delimiter for restoration purposes.
        add_inline_token(p, CODE_INLINE, "`".repeat(p.fence_start) + (char === " " ? " " : ""));
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
      // CommonMark 6.2: a `_` run preceded by a word character is both left- and
      // right-flanking, so it cannot open emphasis whatever follows — which is
      // why this needs the lookbehind only. `*` is deliberately exempt: rules 1
      // and 2 put no such exclusion on it, so `foo*bar*baz` still emphasises.
      const undBlocked = isUnd && prev_is_word_char(p);

      if (p.pending.length === 1) {
        if (symbol === char) {
          p.pending = pending_with_char;
          return true;
        }
        if (char !== " " && char !== "\n" && !undBlocked) {
          add_text(p);
          add_inline_token(p, italic, symbol);
          p.pending = char;
          return true;
        }
      } else {
        if (undBlocked) {
          break;
        }
        if (symbol === char) {
          add_text(p);
          add_inline_token(p, strong, symbol + symbol);
          add_inline_token(p, italic, symbol);
          p.pending = "";
          return true;
        }
        if (char !== " " && char !== "\n") {
          add_text(p);
          add_inline_token(p, strong, symbol + symbol);
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
          add_inline_token(p, STRIKE, "~~");
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
        add_inline_token(p, EQUATION_INLINE, "$");
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
        char !== "]" &&
        // A label cannot open on the last character of a line; the held `[` is
        // literal text.
        char !== "\n"
      ) {
        add_text(p);
        add_inline_token(p, LINK, "[");
        p.link_depth = 0;
        p.pending = char;
        return true;
      }
      break;
    case "]":
      // CommonMark resolves a link before emphasis (6.3 before 6.2), so a `]`
      // ends the label even when an inline token opened inside it never closed.
      // Without this the label end is never seen, the link loses its href and
      // the destination shows up as visible text.
      if (p.pending === "]" && p.link_depth === 0) {
        const label_idx = enclosing_idx(p, LABEL_TOKENS);
        if (label_idx !== -1) {
          add_text(p);
          end_tokens_to_len(p, label_idx);
          return handleLinkOrImage(p, char, pending_with_char);
        }
      }
      break;
    case "|":
      // GFM splits a row into cells BEFORE parsing their inline content, so a
      // `|` ends the cell even when an inline token opened inside it never
      // closed. Without this the run swallowed the cell delimiters and collapsed
      // the rest of the row — and, once the delimiter row is required, the rest
      // of the table with it.
      if (p.pending === "|") {
        const cell_idx = enclosing_idx(p, CELL_TOKENS);
        if (cell_idx !== -1) {
          add_text(p);
          end_tokens_to_len(p, cell_idx);
          return handleTableCell(p, char, pending_with_char);
        }
      }
      break;
    case "!":
      if (p.token !== IMAGE && char === "[") {
        add_text(p);
        add_inline_token(p, IMAGE, "![");
        p.link_depth = 0;
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
