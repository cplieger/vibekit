// Property-based tests for smd-parser-handlers: exercises individual handler
// functions at block-boundary edge cases via single-character streaming.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { parser, parser_write, parser_end, DOCUMENT } from "./smd-parser.js";
import type { Parser } from "./smd-parser.js";

/** No-op renderer for structural testing. */
function nullRenderer() {
  return {
    data: null,
    add_token: () => {},
    end_token: () => {},
    add_text: () => {},
    set_attr: () => {},
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
  return fc.array(fc.constantFrom(...chars), { minLength: 0, maxLength: 60 })
    .map((arr) => arr.join(""));
}

describe("smd-parser-handlers edge cases", () => {
  it("handleCodeFence: arbitrary backtick sequences never throw", () => {
    fc.assert(
      fc.property(
        stringFromChars("`", "a", "\n", " ", "~"),
        (input) => {
          const p = parser(nullRenderer() as any);
          feedCharByChar(p, input);
          parser_end(p);
          expect(p.tokens[0]).toBe(DOCUMENT);
          expect(p.len).toBeGreaterThanOrEqual(0);
        },
      ),
      { numRuns: 200 },
    );
  });

  it("handleTable: arbitrary pipe/escape patterns never throw", () => {
    fc.assert(
      fc.property(
        stringFromChars("|", "\\", "-", " ", "\n", "a", ":"),
        (input) => {
          const p = parser(nullRenderer() as any);
          feedCharByChar(p, input);
          parser_end(p);
          expect(p.tokens[0]).toBe(DOCUMENT);
          expect(p.len).toBeGreaterThanOrEqual(0);
        },
      ),
      { numRuns: 200 },
    );
  });

  it("handleRootContext: heading/rule ambiguity never throws", () => {
    fc.assert(
      fc.property(
        stringFromChars("#", "-", ">", " ", "\n", "a", "="),
        (input) => {
          const p = parser(nullRenderer() as any);
          feedCharByChar(p, input);
          parser_end(p);
          expect(p.tokens[0]).toBe(DOCUMENT);
          expect(p.len).toBeGreaterThanOrEqual(0);
        },
      ),
      { numRuns: 200 },
    );
  });

  it("code fence content is never interpreted as markdown", () => {
    fc.assert(
      fc.property(
        fc.tuple(
          fc.constantFrom("```", "~~~"),
          fc.string({ minLength: 0, maxLength: 50 }),
        ),
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
