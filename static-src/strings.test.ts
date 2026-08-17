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

describe("windowOutput", () => {
  it("keeps everything when it fits", async () => {
    const { windowOutput } = await import("./strings.js");
    const text = "a\nb\nc";
    expect(windowOutput(text, 20)).toEqual({
      text,
      elided: 0,
      kept: [{ from: 0, to: text.length, at: 0 }],
    });
  });

  it("keeps the first and last N lines and reports the elided middle", async () => {
    const { windowOutput } = await import("./strings.js");
    const lines = Array.from({ length: 50 }, (_, i) => `line${String(i)}`);
    const got = windowOutput(lines.join("\n"), 5);
    // A build's first lines say what it did and its last say how it ended.
    expect(got.text.split("\n")).toHaveLength(10);
    expect(got.text).toContain("line0");
    expect(got.text).toContain("line49");
    expect(got.text).not.toContain("line25");
    expect(got.elided).toBe(40);
  });

  it("does not count a trailing newline as a line", async () => {
    const { windowOutput } = await import("./strings.js");
    expect(windowOutput("a\nb\n", 1).elided).toBe(0);
  });
});

describe("windowOutput line splitting", () => {
  // The splitter was rewritten to record each line's source offset, so these pin
  // that it still agrees element-for-element with the `split("\n")` + pop it
  // replaced — INCLUDING the one place it deliberately does not: the elided
  // branch used to join head and tail without the trailing newline the
  // non-elided branch kept, so the same input reported a different last line
  // depending only on how long it was.
  it("agrees with split-and-pop on the boundary inputs", async () => {
    const { windowOutput } = await import("./strings.js");
    const oldSplit = (text: string): string[] => {
      const lines = text.split("\n");
      if (lines.length > 0 && lines[lines.length - 1] === "") {
        lines.pop();
      }
      return lines;
    };
    for (const text of ["", "\n", "a\nb", "a\nb\n", "a\n\nb", "\na", "a"]) {
      // n is large enough that nothing is elided, so the reported text is the
      // input and `kept` covers all of it — which is what makes the line count
      // comparable to the old splitter's.
      const win = windowOutput(text, 100);
      expect(win.text, `text for ${JSON.stringify(text)}`).toBe(text);
      expect(win.elided).toBe(0);
      expect(win.kept).toEqual([{ from: 0, to: text.length, at: 0 }]);
      // The elision threshold keys on the line COUNT, so the count itself has to
      // match: windowOutput(text, k).elided is 0 exactly while lines <= 2k.
      const lines = oldSplit(text).length;
      expect(windowOutput(text, Math.max(1, Math.ceil(lines / 2))).elided).toBe(0);
    }
  });

  it("keeps the trailing newline's last line in the elided branch too", async () => {
    const { windowOutput } = await import("./strings.js");
    const text = Array.from({ length: 10 }, (_, i) => `l${String(i)}`).join("\n") + "\n";
    const win = windowOutput(text, 2);
    // `l9` is the last real line; the old elided branch dropped it because
    // `slice(-n)` picked up the empty remainder the trailing newline left.
    expect(win.text.split("\n")).toEqual(["l0", "l1", "l8", "l9", ""]);
    expect(win.elided).toBe(6);
  });
});

describe("windowOutput kept ranges", () => {
  it("reports source ranges whose slices reconstruct the windowed text", async () => {
    const { windowOutput } = await import("./strings.js");
    const lines = Array.from({ length: 50 }, (_, i) => `line${String(i)}`);
    const text = lines.join("\n") + "\n";
    const win = windowOutput(text, 3);

    // The ranges are the contract: slicing the SOURCE by them, in order, must
    // reproduce the windowed text (bar the join newline the window inserts).
    expect(win.kept).toHaveLength(2);
    const pieces = win.kept.map((r) => text.slice(r.from, r.to));
    expect(pieces.join("\n")).toBe(win.text);
    // And each range must land where it says it lands.
    for (const r of win.kept) {
      expect(win.text.slice(r.at, r.at + (r.to - r.from))).toBe(text.slice(r.from, r.to));
    }
  });
});

describe("windowSpans", () => {
  const span = (start: number, end: number) => ({ start, end, fg: 1, bg: -1, attrs: 0 });

  it("passes spans through unchanged when nothing was elided", async () => {
    const { windowOutput, windowSpans } = await import("./strings.js");
    const win = windowOutput("a\nb\nc", 20);
    expect(windowSpans([span(2, 3)], win.kept)).toEqual([span(2, 3)]);
  });

  it("drops a span that fell entirely in the elided middle", async () => {
    const { windowOutput, windowSpans } = await import("./strings.js");
    const lines = Array.from({ length: 50 }, (_, i) => `line${String(i)}`);
    const text = lines.join("\n") + "\n";
    const win = windowOutput(text, 3);
    const middle = text.indexOf("line25");
    expect(windowSpans([span(middle, middle + 6)], win.kept)).toEqual([]);
  });

  it("splits a span straddling the elision into one piece per side", async () => {
    const { windowOutput, windowSpans } = await import("./strings.js");
    const lines = Array.from({ length: 50 }, (_, i) => `line${String(i)}`);
    const text = lines.join("\n") + "\n";
    const win = windowOutput(text, 3);
    // One span covering the whole output survives as two, one per kept range.
    const out = windowSpans([span(0, text.length)], win.kept);
    expect(out).toHaveLength(2);
    // Each piece must address real text in the WINDOWED string.
    for (const s of out) {
      expect(s.start).toBeGreaterThanOrEqual(0);
      expect(s.end).toBeLessThanOrEqual(win.text.length);
      expect(s.end).toBeGreaterThan(s.start);
    }
  });

  it("rebases a tail span onto its position in the windowed text", async () => {
    const { windowOutput, windowSpans } = await import("./strings.js");
    const lines = Array.from({ length: 50 }, (_, i) => `line${String(i)}`);
    const text = lines.join("\n") + "\n";
    const win = windowOutput(text, 3);
    const at = text.lastIndexOf("line49");
    const out = windowSpans([span(at, at + 6)], win.kept);
    expect(out).toHaveLength(1);
    // The rebased offsets must select the same word in the windowed text.
    expect(win.text.slice(out[0]!.start, out[0]!.end)).toBe("line49");
  });
});
