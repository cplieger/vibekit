// Unit tests for strings.ts — pure functions, no DOM dependency.
import { describe, it, expect } from "vitest";
import * as fc from "fast-check";
import { escText, escAttr, formatElapsed, humanName, isoDuration, truncate } from "./strings.js";

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

describe("windowOutput boundaries", () => {
  it("keeps exactly 2n lines whole, as one kept range", async () => {
    const { windowOutput } = await import("./strings.js");
    // 4 lines with n=2 is the last size that fits. Windowing it anyway would
    // rebuild the same text out of two ranges: identical to read, and a
    // different mapping for every span that crosses the seam.
    expect(windowOutput("l0\nl1\nl2\nl3", 2)).toEqual({
      text: "l0\nl1\nl2\nl3",
      elided: 0,
      kept: [{ from: 0, to: 11, at: 0 }],
    });
  });

  it("reads the last line's offset from the source when there is no trailing newline", async () => {
    const { windowOutput } = await import("./strings.js");
    // "c" is the remainder after the final newline, so its start offset is
    // recorded on a different branch from every other line's. At n=1 that
    // offset IS the tail range, and losing it widens the tail to the whole
    // text — the elided middle comes back, duplicated.
    expect(windowOutput("a\nb\nc", 1)).toEqual({
      text: "a\nc",
      elided: 1,
      kept: [
        { from: 0, to: 1, at: 0 },
        { from: 4, to: 5, at: 2 },
      ],
    });
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

  it("drops a span that only touches the seams of the elision", async () => {
    const { windowOutput, windowSpans } = await import("./strings.js");
    const win = windowOutput("a\nb\nc", 1);
    // This span is the elided middle plus both boundaries. Clipping it against
    // each kept range leaves an empty range, which must be dropped rather than
    // emitted as a zero-width span.
    expect(windowSpans([span(1, 4)], win.kept)).toEqual([]);
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

// ---------------------------------------------------------------------------
// A span's two spellings. `formatElapsed` writes the words and `isoDuration`
// writes the `datetime` beside them, and `<time>`'s own contract is that the
// attribute is a machine-readable form of the element's CONTENTS — so a pair that
// disagrees is not two views of one value, it is a wrong attribute.
//
// The pair is asserted here rather than at the footer that renders it because both
// functions live here; the footer's own tests cover the ELEMENT.
// ---------------------------------------------------------------------------

describe("a span's text and its machine-readable twin", () => {
  /** `[ms, text, datetime]`, hardcoded on all three axes. Computing either
   *  expectation from the other's thresholds would assert the split against a copy
   *  of itself. */
  const PAIRS: [number, string, string][] = [
    [0, "0.0s", "PT0.0S"],
    [400, "0.4s", "PT0.4S"],
    [45_500, "45.5s", "PT45.5S"],
    [59_999, "60.0s", "PT60.0S"],
    [60_000, "1m 0s", "PT1M"],
    [92_000, "1m 32s", "PT1M32S"],
    [119_999, "1m 59s", "PT1M59S"],
    [3_600_000, "1h 0m", "PT1H"],
    [3_661_000, "1h 1m", "PT1H1M"],
    [7_200_000, "2h 0m", "PT2H"],
    [8_999_999, "2h 29m", "PT2H29M"],
  ];

  it("spells the same span both ways", () => {
    for (const [ms, text, iso] of PAIRS) {
      expect(formatElapsed(ms), `text for ${String(ms)}ms`).toBe(text);
      expect(isoDuration(ms), `datetime for ${String(ms)}ms`).toBe(iso);
    }
  });

  it("never carries a seconds component the words do not show", () => {
    // The invariant the doc states, checked against the TEXT rather than against a
    // second copy of the thresholds: above an hour `formatElapsed` prints `1h 1m`
    // and nothing finer, so a `PT1H1M1S` beside it claims a precision the reader
    // cannot see. That is the exact pair this caught.
    //
    // ONE DIRECTION, and the converse is deliberately not asserted: ISO 8601 omits
    // a ZERO component, so `1m 0s` is spelled `PT1M` and that is the same span at
    // the same precision rather than a coarser one. What must never happen is the
    // attribute naming a unit the reader has no digit for.
    //
    // BOTH SIDES ARE COMPUTED: the table supplies only the input here. Reading its
    // expected `iso` column instead would assert a property of a string literal,
    // which no change to either function could ever falsify — the first draft of
    // this case did exactly that and survived the red-check.
    for (const [ms] of PAIRS) {
      const text = formatElapsed(ms);
      const iso = isoDuration(ms);
      if (/\d(?:\.\d)?s$/.test(text)) {
        continue;
      }
      expect(iso, `${text} / ${iso}`).not.toMatch(/S$/);
    }
  });

  it("floors rather than rounds, in both spellings", () => {
    // 1.9999s of a minute is still `1m 59s`, not `1m 60s` — and the attribute has
    // to agree, or the two disagree by a whole second at every boundary.
    expect(formatElapsed(119_999)).toBe("1m 59s");
    expect(isoDuration(119_999)).toBe("PT1M59S");
  });

  it("treats a negative span as zero rather than emitting a negative duration", () => {
    // `PT-0.4S` is not a duration any consumer should have to parse. The words are
    // the footer's problem and it never renders one: the slot is hidden without a
    // stamped duration.
    expect(isoDuration(-400)).toBe("PT0.0S");
  });
});
