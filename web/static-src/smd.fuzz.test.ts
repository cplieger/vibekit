// Fuzz test for smd.ts parser_write — streaming markdown parser accepting
// arbitrary string input. Feeds random chunks (including split mid-codepoint,
// empty strings, and null bytes) and asserts the parser never throws, never
// produces negative `len`, and always leaves `tokens[0] === DOCUMENT`.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { parser, parser_write, parser_end, DOCUMENT } from "./smd-parser.js";

/** No-op renderer that records nothing — we only care about parser state. */
function nullRenderer() {
  return {
    data: null,
    add_token: () => {
      /* noop */
    },
    end_token: () => {
      /* noop */
    },
    add_text: () => {
      /* noop */
    },
    set_attr: () => {
      /* noop */
    },
  };
}

describe("smd parser_write fuzz", () => {
  it("never throws on arbitrary single-chunk input", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.string({ minLength: 0, maxLength: 2000 }), (input) => {
        const p = parser(nullRenderer());
        parser_write(p, input);
        parser_end(p);
        return p.tokens[0] === DOCUMENT && p.len >= 0;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("never throws on chunked input with random split points", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(
        fc.string({ minLength: 1, maxLength: 1000 }),
        fc.array(fc.nat(), { minLength: 1, maxLength: 20 }),
        (input, splits) => {
          const p = parser(nullRenderer());
          let pos = 0;
          for (const raw of splits) {
            const splitAt = pos + (raw % (input.length - pos + 1));
            const chunk = input.slice(pos, splitAt);
            parser_write(p, chunk);
            if (p.len < 0) {
              return false;
            }
            pos = splitAt;
            if (pos >= input.length) {
              break;
            }
          }
          if (pos < input.length) {
            parser_write(p, input.slice(pos));
          }
          parser_end(p);
          return p.tokens[0] === DOCUMENT && p.len >= 0;
        },
      ),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("handles null bytes and control characters without throwing", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(
        fc.array(
          fc.oneof(
            fc.string({ minLength: 0, maxLength: 100 }),
            fc.constant("\0"),
            fc.constant("\x01"),
            fc.constant("\x7f"),
            fc.constant(""),
          ),
          { minLength: 1, maxLength: 50 },
        ),
        (chunks) => {
          const p = parser(nullRenderer());
          for (const chunk of chunks) {
            parser_write(p, chunk);
            if (p.len < 0) {
              return false;
            }
          }
          parser_end(p);
          return p.tokens[0] === DOCUMENT && p.len >= 0;
        },
      ),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });
});

describe("smd handleCodeFence targeted fuzz", () => {
  // Generates adversarial code fence inputs and asserts parser invariants.
  const fenceLang = fc.oneof(
    fc.string({ minLength: 0, maxLength: 50 }),
    fc.constant(""),
    fc.constant("javascript"),
    fc.constant("```"),
    fc.constant("````"),
    fc.constant("\0"),
    fc.array(fc.constant("`"), { minLength: 1, maxLength: 20 }).map((a) => a.join("")),
  );

  const fenceBody = fc.oneof(
    fc.string({ minLength: 0, maxLength: 500 }),
    fc.array(fc.constant("`"), { minLength: 1, maxLength: 30 }).map((a) => a.join("")),
    fc.constant("```\n```\n```"),
    fc.constant("````\ncontent\n````"),
    fc.constant("\0\0\0"),
  );

  const fenceMarker = fc.oneof(
    fc.constant("```"),
    fc.constant("````"),
    fc.constant("`````"),
    fc.array(fc.constant("`"), { minLength: 3, maxLength: 8 }).map((a) => a.join("")),
  );

  it("never throws on adversarial fence patterns", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fenceMarker, fenceLang, fenceBody, fenceMarker, (open, lang, body, close) => {
        const input = `${open}${lang}\n${body}\n${close}\n`;
        const p = parser(nullRenderer());
        parser_write(p, input);
        parser_end(p);
        return p.tokens[0] === DOCUMENT && p.len >= 0;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("handles fences inside fences without panic", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(
        fc.array(fc.tuple(fenceMarker, fenceLang, fenceBody), { minLength: 1, maxLength: 5 }),
        (fences) => {
          let input = "";
          for (const [marker, lang, body] of fences) {
            input += `${marker}${lang}\n${body}\n${marker}\n`;
          }
          const p = parser(nullRenderer());
          parser_write(p, input);
          parser_end(p);
          return p.tokens[0] === DOCUMENT && p.len >= 0;
        },
      ),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
  });
});
