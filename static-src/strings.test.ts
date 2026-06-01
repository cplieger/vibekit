// Unit tests for strings.ts — pure functions, no DOM dependency.
import { describe, it, expect } from "vitest";
import * as fc from "fast-check";
import { escText, escAttr, humanName, truncate } from "./strings.js";

describe("escText", () => {
  it("passes through plain text unchanged", () => {
    expect(escText("hello world")).toBe("hello world");
  });

  it("escapes ampersand", () => {
    expect(escText("a & b")).toBe("a &amp; b");
  });

  it("escapes less-than", () => {
    expect(escText("<script>")).toBe("&lt;script&gt;");
  });

  it("escapes greater-than", () => {
    expect(escText("a > b")).toBe("a &gt; b");
  });

  it("escapes double-quote", () => {
    expect(escText(`say "hi"`)).toBe("say &quot;hi&quot;");
  });

  it("escapes all special chars in one string", () => {
    expect(escText(`<a href="x&y">`)).toBe("&lt;a href=&quot;x&amp;y&quot;&gt;");
  });

  it("handles empty string", () => {
    expect(escText("")).toBe("");
  });

  it("handles string with no special chars", () => {
    expect(escText("abc123")).toBe("abc123");
  });

  it("escapes multiple ampersands", () => {
    expect(escText("a & b & c")).toBe("a &amp; b &amp; c");
  });
});

describe("humanName", () => {
  it("replaces hyphens with spaces", () => {
    expect(humanName("foo-bar")).toBe("foo bar");
  });

  it("replaces underscores with spaces", () => {
    expect(humanName("foo_bar")).toBe("foo bar");
  });

  it("replaces mixed separators", () => {
    expect(humanName("foo-bar_baz")).toBe("foo bar baz");
  });

  it("passes through plain words unchanged", () => {
    expect(humanName("hello")).toBe("hello");
  });

  it("handles empty string", () => {
    expect(humanName("")).toBe("");
  });

  it("handles multiple consecutive separators", () => {
    expect(humanName("a--b")).toBe("a  b");
  });

  it("handles leading separator", () => {
    expect(humanName("-foo")).toBe(" foo");
  });

  it("handles trailing separator", () => {
    expect(humanName("foo-")).toBe("foo ");
  });
});

// ---------------------------------------------------------------------------
// Property-based tests: XSS-prevention invariants (tarch-b14-c4-p1)
// ---------------------------------------------------------------------------

describe("escText property-based invariants", () => {
  it('output never contains literal < > or " characters', () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 200 }), (input) => {
        const out = escText(input);
        // & is expected in output (entity prefix); only <, >, " must be absent.
        expect(out).not.toMatch(/[<>"]/);
      }),
      { numRuns: 500 },
    );
  });

  it("all & in output are part of known entities", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 200 }), (input) => {
        const out = escText(input);
        // Every & must be followed by amp;, lt;, gt;, or quot;
        const stripped = out.replace(/&(amp|lt|gt|quot);/g, "");
        expect(stripped).not.toContain("&");
      }),
      { numRuns: 500 },
    );
  });

  it("output length is always >= input length", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 200 }), (input) => {
        expect(escText(input).length).toBeGreaterThanOrEqual(input.length);
      }),
      { numRuns: 500 },
    );
  });

  it("empty string returns empty string", () => {
    expect(escText("")).toBe("");
  });
});

describe("escAttr property-based invariants", () => {
  it("output never contains literal < > \" or ' characters", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 200 }), (input) => {
        const out = escAttr(input);
        // & is expected in output (entity prefix); only <, >, ", ' must be absent.
        expect(out).not.toMatch(/[<>"']/);
      }),
      { numRuns: 500 },
    );
  });

  it("is a superset of escText (escAttr additionally escapes single quotes)", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 200 }), (input) => {
        const textResult = escText(input);
        const attrResult = escAttr(input);
        // escAttr replaces ' in escText's output, so attrResult is textResult with ' → &#39;
        expect(attrResult).toBe(textResult.replace(/'/g, "&#39;"));
      }),
      { numRuns: 500 },
    );
  });
});

describe("truncate", () => {
  it("returns short strings unchanged", () => {
    expect(truncate("hello")).toBe("hello");
  });

  it("returns exactly-max-length strings unchanged", () => {
    const s = "a".repeat(40);
    expect(truncate(s)).toBe(s);
  });

  it("truncates strings longer than max with ellipsis", () => {
    const s = "a".repeat(41);
    expect(truncate(s)).toBe("a".repeat(37) + "\u2026");
  });

  it("respects custom max parameter", () => {
    expect(truncate("abcdefghij", 7)).toBe("abcd\u2026");
  });

  it("handles empty string", () => {
    expect(truncate("")).toBe("");
  });
});
