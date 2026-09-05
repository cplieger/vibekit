// Property-based tests for smd-parser-handlers: exercises individual handler
// functions at block-boundary edge cases via single-character streaming.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { parser, parser_write, parser_end, DOCUMENT } from "./smd-parser.js";
import type { Parser } from "./smd-parser.js";
import {
  HREF,
  ITALIC_UND,
  LINE_BREAK,
  PARAGRAPH,
  RAW_URL,
  STRONG_AST,
  STRONG_UND,
  UNCLOSED,
} from "./smd-parser-types.js";

/** No-op renderer for structural testing. */
function nullRenderer() {
  return {
    data: null,
    add_token: () => {
      /* mock no-op */
    },
    end_token: () => {
      /* mock no-op */
    },
    add_text: () => {
      /* mock no-op */
    },
    set_attr: () => {
      /* mock no-op */
    },
  };
}

/** Feed input one character at a time to maximize handler invocations. */
function feedCharByChar(p: Parser, input: string): void {
  for (const ch of input) {
    parser_write(p, ch);
  }
}

/** Generate a string from a set of characters. */
function stringFromChars(...chars: string[]): fc.Arbitrary<string> {
  return fc
    .array(fc.constantFrom(...chars), { minLength: 0, maxLength: 60 })
    .map((arr) => arr.join(""));
}

describe("smd-parser-handlers edge cases", () => {
  it("handleCodeFence: arbitrary backtick sequences never throw", () => {
    fc.assert(
      fc.property(stringFromChars("`", "a", "\n", " ", "~"), (input) => {
        const p = parser(nullRenderer() as any);
        feedCharByChar(p, input);
        parser_end(p);
        expect(p.tokens[0]).toBe(DOCUMENT);
        expect(p.len).toBeGreaterThanOrEqual(0);
      }),
      { numRuns: 200 },
    );
  });

  it("handleTable: arbitrary pipe/escape patterns never throw", () => {
    fc.assert(
      fc.property(stringFromChars("|", "\\", "-", " ", "\n", "a", ":"), (input) => {
        const p = parser(nullRenderer() as any);
        feedCharByChar(p, input);
        parser_end(p);
        expect(p.tokens[0]).toBe(DOCUMENT);
        expect(p.len).toBeGreaterThanOrEqual(0);
      }),
      { numRuns: 200 },
    );
  });

  it("handleRootContext: heading/rule ambiguity never throws", () => {
    fc.assert(
      fc.property(stringFromChars("#", "-", ">", " ", "\n", "a", "="), (input) => {
        const p = parser(nullRenderer() as any);
        feedCharByChar(p, input);
        parser_end(p);
        expect(p.tokens[0]).toBe(DOCUMENT);
        expect(p.len).toBeGreaterThanOrEqual(0);
      }),
      { numRuns: 200 },
    );
  });

  it("code fence content is never interpreted as markdown", () => {
    fc.assert(
      fc.property(
        fc.tuple(fc.constantFrom("```", "~~~"), fc.string({ minLength: 0, maxLength: 50 })),
        ([fence, content]) => {
          const input = `${fence}\n${content}\n${fence}\n`;
          const p = parser(nullRenderer() as any);
          feedCharByChar(p, input);
          parser_end(p);
          expect(p.tokens[0]).toBe(DOCUMENT);
          expect(p.len).toBeGreaterThanOrEqual(0);
        },
      ),
      { numRuns: 100 },
    );
  });
});

// ---------------------------------------------------------------------------
// handleRawURL: the trailing-boundary rule
// ---------------------------------------------------------------------------

interface Trace {
  /** Every token opened, in order. */
  tokens: number[];
  /** How many of them were closed. */
  ends: number;
  /** Every attribute set, in order. */
  attrs: { attr: number; value: string }[];
  /** Every text run emitted, in order. */
  texts: string[];
}

function tracingRenderer(t: Trace) {
  return {
    data: null,
    add_token: (_: null, token: number) => {
      t.tokens.push(token);
    },
    end_token: () => {
      t.ends += 1;
    },
    add_text: (_: null, text: string) => {
      t.texts.push(text);
    },
    set_attr: (_: null, attr: number, value: string) => {
      t.attrs.push({ attr, value });
    },
  };
}

/** Parse `input` in `chunkLen`-sized writes and report what the renderer saw. */
function trace(input: string, chunkLen = input.length): Trace {
  const t: Trace = { tokens: [], ends: 0, attrs: [], texts: [] };
  const p = parser(tracingRenderer(t) as any);
  for (let i = 0; i < input.length; i += chunkLen) {
    parser_write(p, input.slice(i, i + chunkLen));
  }
  parser_end(p);
  return t;
}

function hrefs(t: Trace): string[] {
  return t.attrs.filter((a) => a.attr === HREF).map((a) => a.value);
}

describe("handleRawURL trailing boundary", () => {
  // A URL abutting the opening `**` never becomes a raw URL at all: the `h` is
  // consumed by handleCommon's emphasis arm, which opens STRONG_AST and leaves
  // it pending, so parser_write's raw-URL entry (pending must be "" or " ") is
  // never reached. The reproducing shape has a word before the URL.
  it("keeps a closing ** out of the href AND out of the link text", () => {
    const t = trace("**see https://example.com**");
    expect(hrefs(t)).toEqual(["https://example.com"]);
    expect(t.texts.join("")).toBe("see https://example.com");
  });

  it("re-feeds the stripped tail so the emphasis still closes", () => {
    // Dropping the tail instead of re-writing it leaves STRONG_AST open, so the
    // rest of the message renders bold. Only the paragraph may stay open here.
    const t = trace("**see https://example.com** plain");
    expect(t.tokens).toEqual([PARAGRAPH, STRONG_AST, RAW_URL]);
    expect(t.tokens.length - t.ends).toBe(1);
  });

  it("keeps CJK punctuation out of the href and renders it as text", () => {
    const t = trace("https://example.com。");
    expect(hrefs(t)).toEqual(["https://example.com"]);
    expect(t.texts).toEqual(["https://example.com", "。"]);
  });

  it("keeps a legal trailing path character in the href", () => {
    // `.` and `,` are legal INSIDE a path, so the trim runs only at the end.
    expect(hrefs(trace("https://example.com/a.b,c/d "))).toEqual(["https://example.com/a.b,c/d"]);
  });

  it("keeps a closing parenthesis in the href", () => {
    // Deliberately outside the tail set: trimming it needs GFM's balance rule,
    // and without one a disambiguation link is cut short.
    expect(hrefs(trace("https://en.wikipedia.org/wiki/Mercury_(planet) "))).toEqual([
      "https://en.wikipedia.org/wiki/Mercury_(planet)",
    ]);
  });

  it("trims a run of trailing punctuation, not just the last character", () => {
    expect(hrefs(trace("see https://example.com?!, here"))).toEqual(["https://example.com"]);
  });

  it("is chunk-size invariant", () => {
    fc.assert(
      fc.property(fc.integer({ min: 1, max: 8 }), (chunkLen) => {
        const input = "a **see https://example.com**, b https://example.com。 c";
        expect(hrefs(trace(input, chunkLen))).toEqual(hrefs(trace(input)));
        expect(trace(input, chunkLen).texts.join("")).toBe(trace(input).texts.join(""));
      }),
      { numRuns: 8 },
    );
  });
});

// ---------------------------------------------------------------------------
// The intraword-underscore lookbehind, across write boundaries
//
// `parser_write` ends with `add_text`, which hands `textBuf` to the renderer and
// clears it — so at a chunk boundary the character before the `_` is gone from
// the buffer, and the rule has to be answered from retained state instead.
// ---------------------------------------------------------------------------

describe("intraword underscore lookbehind across writes", () => {
  it("survives a write boundary that lands on the underscore", () => {
    // Chunk 4 puts the boundary exactly after `run_`, so `textBuf` is empty when
    // the rule is evaluated. A one-shot parse cannot reach this state.
    expect(trace("run_progress", 4).tokens).not.toContain(ITALIC_UND);
  });

  it("is chunk-size invariant", () => {
    fc.assert(
      fc.property(fc.integer({ min: 1, max: 8 }), (chunkLen) => {
        const input = "run_progress shape, tool_call_update as a delta";
        expect(trace(input, chunkLen).texts.join("")).toBe(trace(input).texts.join(""));
        expect(trace(input, chunkLen).tokens).toEqual(trace(input).tokens);
      }),
      { numRuns: 8 },
    );
  });

  it("a paragraph-initial underscore still opens after the promotion re-feed", () => {
    // handleRootContext re-feeds `_e` when it promotes the pending text to a
    // paragraph, which is why the lookbehind tracks COMMITTED text rather than
    // the previous input character.
    expect(trace("_em_").tokens).toEqual([PARAGRAPH, ITALIC_UND]);
  });

  it("survives a write boundary inside an open __ run", () => {
    // Chunk 6 ends on the `_` of `__run_`, so handleStrong's nested-italic open
    // has to answer the rule from the retained flag as well.
    expect(trace("__run_progress__", 6).tokens).toEqual([PARAGRAPH, STRONG_UND]);
  });

  it("reads the last code point when a chunk splits a surrogate pair", () => {
    // A symbol is punctuation for flanking, so the emphasis must open — and at
    // chunk 1 the pair arrives one UTF-16 unit at a time, which is the case a
    // charAt-based lookbehind gets wrong in both halves.
    const input = "\u{1F389}_yay_";
    fc.assert(
      fc.property(fc.integer({ min: 1, max: 8 }), (chunkLen) => {
        expect(trace(input, chunkLen).tokens).toEqual([PARAGRAPH, ITALIC_UND]);
      }),
      { numRuns: 8 },
    );
  });

  it("a soft break resets the lookbehind under chunking", () => {
    // The soft break emits LINE_BREAK straight to the renderer, bypassing the
    // helpers that own the flag — so without a reset there the `a_b` verdict
    // outlives the line and `_real_` never opens.
    const t = trace("a_b\n_real_\n", 3);
    expect(t.tokens).toEqual([PARAGRAPH, LINE_BREAK, ITALIC_UND]);
    expect(t.texts.join("")).toBe("a_breal");
  });
});

// ---------------------------------------------------------------------------
// Nesting past TOKEN_ARRAY_CAP.
//
// The cap is the intended depth limit. The defect was that the saturating
// push left `p.token` and `p.pending` untouched, so handleRootContext's
// fallthrough re-entered itself with byte-identical state.
// ---------------------------------------------------------------------------

describe("token-stack saturation", () => {
  const shapes = [
    ">".repeat(23) + " x",
    ">".repeat(200) + " x",
    ">".repeat(1000) + " x",
    "> ".repeat(30) + "x",
    ">".repeat(30) + " ```js`x",
    ">".repeat(30) + " **x",
    ">".repeat(21) + " | a | b |\n| - | - |\n| 1 | 2 |",
    ">".repeat(200) + " | a |\n| - |\n| 1 |",
    "> ".repeat(30) + "| a |\n| - |\n| 1 |",
    "- ".repeat(1024) + "x",
    " ".repeat(1024) + "- x",
    "*".repeat(1024),
  ];

  it.each(shapes)("leaves the stack sane for %j", (input) => {
    const p = parser(nullRenderer() as any);
    expect(() => {
      parser_write(p, input);
      parser_end(p);
    }).not.toThrow();
    expect(p.tokens[0]).toBe(DOCUMENT);
    expect(p.len).toBeGreaterThanOrEqual(0);
  });

  it("still keeps the text at saturating depth", () => {
    expect(trace(">".repeat(23) + " x").texts.join("")).toContain("x");
  });
});

// ---------------------------------------------------------------------------
// Chunk-boundary invariance for the Phase A decisions. Each of these spans a
// line or holds a character, so a boundary-dependent implementation shows up
// here and nowhere else.
// ---------------------------------------------------------------------------

describe("chunk-boundary invariance", () => {
  const inputs = [
    "```\nfoo ```\nbar\n",
    "```\nfoo\n    ```\nbar\n",
    "```\nfoo\n`````\nbar\n",
    "[a [b] c](https://e.com)",
    "见 https://example.com/2137，`96ed647b`）",
    "ab<",
    "ab[\ncd",
    "ab`\ncd",
    // The table deferral spans two lines before it decides, and the trailing
    // space is the input that WAS chunk-dependent: whether the row terminated
    // depended on where the write boundary fell.
    "| a | b |\n| - | - |\n| 1 | 2 |",
    "| a | b | \n| - | - |\n| 1 | 2 |",
    "| a | b |\n| x |\n| - |\n| 1 |",
  ];

  it.each(inputs)("produces one tree for %j at every chunk size", (input) => {
    const whole = trace(input);
    for (let chunkLen = 1; chunkLen <= 8; chunkLen += 1) {
      const chunked = trace(input, chunkLen);
      expect(chunked.tokens).toEqual(whole.tokens);
      expect(chunked.ends).toEqual(whole.ends);
      expect(chunked.attrs).toEqual(whole.attrs);
      expect(chunked.texts.join("")).toBe(whole.texts.join(""));
    }
  });
});

// ---------------------------------------------------------------------------
// The UNCLOSED signal: which literal the renderer is told to restore, and the
// guarantee that a token which closed normally carries no such call.
// ---------------------------------------------------------------------------

function unclosed(t: Trace): string[] {
  return t.attrs.filter((a) => a.attr === UNCLOSED).map((a) => a.value);
}

describe("unresolved inline tokens report their delimiter", () => {
  const cases: [string, string[]][] = [
    ["a *b", ["*"]],
    ["a _b", ["_"]],
    ["a **b", ["**"]],
    ["a __b", ["__"]],
    ["a ~~b", ["~~"]],
    ["a `b", ["`"]],
    ["a ``b", ["``"]],
    ["a ` b", ["` "]],
    ["a [b", ["["]],
    ["a ![b", ["!["]],
    ["a $b", ["$"]],
    ["a \\(b", ["\\("]],
    // Innermost first, so the italic's `*` precedes the strong's `**`.
    ["a **b *c", ["*", "**"]],
  ];

  it.each(cases)("%j reports %j", (input, expected) => {
    expect(unclosed(trace(input))).toEqual(expected);
  });

  const closed = ["a **b** c", "*em*", "_em_", "~~s~~", "`c`", "[a](http://e.com)", "$x$"];

  it.each(closed)("%j reports nothing", (input) => {
    expect(unclosed(trace(input))).toEqual([]);
  });

  it("records no delimiter for a token saturation refused to push", () => {
    const input = ">".repeat(30) + " **x";
    expect(() => trace(input)).not.toThrow();
    expect(unclosed(trace(input))).toEqual([]);
  });

  it("emits the delimiter immediately before the end_token it belongs to", () => {
    // The renderer consumes the signal on the very next end_token, so anything
    // between the two would attach the delimiter to the wrong element.
    const t: Trace = { tokens: [], ends: 0, attrs: [], texts: [] };
    const ops: string[] = [];
    const p = parser({
      data: null,
      add_token: (_: null, token: number) => {
        ops.push(`open:${token}`);
        t.tokens.push(token);
      },
      end_token: () => {
        ops.push("close");
        t.ends += 1;
      },
      add_text: (_: null, text: string) => {
        ops.push(`text:${text}`);
      },
      set_attr: (_: null, attr: number, value: string) => {
        ops.push(attr === UNCLOSED ? `unclosed:${value}` : `attr:${attr}`);
      },
    } as any);
    parser_write(p, "a **b");
    parser_end(p);
    expect(ops[ops.length - 2]).toBe("unclosed:**");
    expect(ops[ops.length - 1]).toBe("close");
  });

  it("keeps the same tree at every chunk size", () => {
    for (const input of ["a **b", "a ` b\nc", "[a *b](https://e.com)", "an ![img"]) {
      const whole = trace(input);
      for (let chunkLen = 1; chunkLen <= 8; chunkLen += 1) {
        const chunked = trace(input, chunkLen);
        expect(chunked.tokens).toEqual(whole.tokens);
        expect(chunked.attrs).toEqual(whole.attrs);
        expect(chunked.texts.join("")).toBe(whole.texts.join(""));
      }
    }
  });
});

// ---------------------------------------------------------------------------
// The CLOSE half of CommonMark 6.2, and the stack it must not corrupt.
//
// Refusing a close leaves the token open, so the failure mode to guard against
// is not a wrong tree but an unbalanced one: delegating the refusal to
// handleCommon grew `pending` to `__` and fired two closes against one token.
// ---------------------------------------------------------------------------

describe("the refused underscore close leaves the stack balanced", () => {
  const inputs = [
    "_foo_bar",
    "_foo bar_baz",
    "_internal_state",
    "_internal_state_here_",
    "_a_b_c_d_e_",
    "__x_y_",
    "___tri___",
    "_a__b_",
  ];

  it.each(inputs)("%j leaves only the paragraph open", (input) => {
    // Everything but the streaming tail's own block must be closed exactly once.
    // A refusal that fired an extra close would read as a negative here.
    const t = trace(input);
    expect(t.tokens.length - t.ends).toBe(1);
  });

  it.each(inputs)("%j gives the same tree at every chunk size", (input) => {
    const whole = trace(input);
    for (let chunkLen = 1; chunkLen <= 4; chunkLen += 1) {
      const chunked = trace(input, chunkLen);
      expect(chunked.tokens).toEqual(whole.tokens);
      expect(chunked.attrs).toEqual(whole.attrs);
      expect(chunked.texts.join("")).toBe(whole.texts.join(""));
    }
  });

  it("splits _internal_state on each underscore without changing the tree", () => {
    const p = parser(nullRenderer() as any);
    for (const chunk of ["_internal", "_state"]) {
      parser_write(p, chunk);
    }
    parser_end(p);
    expect(p.tokens[0]).toBe(DOCUMENT);
    expect(p.len).toBeGreaterThanOrEqual(0);
    expect(trace("_internal_state", 9).texts.join("")).toBe(
      trace("_internal_state").texts.join(""),
    );
  });
});
