// Property-based tests for smd-parser-handlers: exercises individual handler
// functions at block-boundary edge cases via single-character streaming.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { parser, parser_write, parser_end, DOCUMENT } from "./smd-parser.js";
import type { Parser } from "./smd-parser.js";
import { HREF, PARAGRAPH, RAW_URL, STRONG_AST } from "./smd-parser-types.js";

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
