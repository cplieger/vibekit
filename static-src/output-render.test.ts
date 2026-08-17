// @vitest-environment happy-dom
// Tests for output-render.ts — the transcript's command-output painter.
//
// The load-bearing property is that NOTHING here parses HTML: every one of
// these assertions would also pass against an innerHTML implementation except
// the injection ones, which are the reason the module exists.

import { describe, it, expect, beforeEach } from "vitest";
import { splitBySpans, outputFragment, renderOutput, appendOutput } from "./output-render.js";
import type { TextSpan } from "./types.js";

const span = (start: number, end: number, over: Partial<TextSpan> = {}): TextSpan => ({
  start,
  end,
  fg: -1,
  bg: -1,
  attrs: 0,
  ...over,
});

let host: HTMLElement;

beforeEach(() => {
  document.body.innerHTML = "<pre id='h'></pre>";
  host = document.getElementById("h") as HTMLElement;
});

describe("splitBySpans", () => {
  it("returns one unstyled piece when there are no spans", () => {
    expect(splitBySpans("hello", [])).toEqual([{ text: "hello", span: null }]);
  });

  it("returns nothing for empty text", () => {
    expect(splitBySpans("", [])).toEqual([]);
  });

  it("splits leading, styled and trailing runs", () => {
    const s = span(5, 8, { fg: 1 });
    expect(splitBySpans("plainredtail", [s])).toEqual([
      { text: "plain", span: null },
      { text: "red", span: s },
      { text: "tail", span: null },
    ]);
  });

  it("handles adjacent spans with no gap", () => {
    const a = span(0, 1, { fg: 1 });
    const b = span(1, 2, { fg: 2 });
    expect(splitBySpans("ab", [a, b])).toEqual([
      { text: "a", span: a },
      { text: "b", span: b },
    ]);
  });

  // The server's fuzz target guarantees sorted, non-overlapping, in-range
  // spans. This clamps anyway: a bad offset would otherwise slice garbage into
  // the transcript, and the cost of not trusting is two Math calls.
  it("clamps a span reaching past the end of the text", () => {
    const pieces = splitBySpans("ab", [span(1, 99, { fg: 1 })]);
    expect(pieces.map((p) => p.text).join("")).toBe("ab");
  });

  it("ignores a span that starts before the cursor", () => {
    const pieces = splitBySpans("abc", [span(1, 3, { fg: 1 }), span(0, 2, { fg: 2 })]);
    // Text is never duplicated or dropped, whatever the spans claim.
    expect(pieces.map((p) => p.text).join("")).toBe("abc");
  });

  it("never loses or duplicates text", () => {
    const text = "0123456789";
    const pieces = splitBySpans(text, [span(2, 4, { fg: 1 }), span(7, 9, { fg: 2 })]);
    expect(pieces.map((p) => p.text).join("")).toBe(text);
  });
});

describe("outputFragment", () => {
  it("builds a text node for unstyled output and no elements", () => {
    const frag = outputFragment("plain", []);
    host.appendChild(frag);
    expect(host.textContent).toBe("plain");
    expect(host.querySelectorAll("span")).toHaveLength(0);
  });

  it("wraps a styled run in one span and leaves the rest as text", () => {
    host.appendChild(outputFragment("plainredtail", [span(5, 8, { fg: 1 })]));
    expect(host.textContent).toBe("plainredtail");
    const spans = host.querySelectorAll("span");
    expect(spans).toHaveLength(1);
    expect(spans[0]!.textContent).toBe("red");
    expect(spans[0]!.className).toBe("ansi-red-fg");
  });

  // HTML in the output must arrive as TEXT. This is the whole reason the module
  // replaced an ansi-to-HTML converter: correctness no longer depends on an
  // escaper being right about every character.
  it("does not interpret markup in the output", () => {
    host.appendChild(outputFragment('<img src=x onerror="alert(1)">', []));
    expect(host.querySelectorAll("img")).toHaveLength(0);
    expect(host.textContent).toBe('<img src=x onerror="alert(1)">');
  });

  it("does not interpret markup inside a styled run either", () => {
    host.appendChild(outputFragment("<script>x</script>", [span(0, 18, { fg: 1 })]));
    expect(host.querySelectorAll("script")).toHaveLength(0);
    expect(host.textContent).toBe("<script>x</script>");
  });

  it("maps every attribute bit to its class", () => {
    const cases: [number, string][] = [
      [1, "ansi-bold"],
      [2, "ansi-italic"],
      [4, "ansi-underline"],
      [16, "ansi-strike"],
      [32, "ansi-dim"],
      [64, "ansi-hidden"],
      [128, "ansi-blink"],
      [256, "ansi-overline"],
      [512, "ansi-double-underline"],
    ];
    for (const [bit, cls] of cases) {
      host.replaceChildren(outputFragment("x", [span(0, 1, { attrs: bit })]));
      expect(host.querySelector("span")!.classList.contains(cls)).toBe(true);
    }
  });

  it("combines multiple attribute bits on one span", () => {
    host.appendChild(outputFragment("x", [span(0, 1, { attrs: 1 | 2 | 4 })]));
    const cls = host.querySelector("span")!.classList;
    expect(cls.contains("ansi-bold")).toBe(true);
    expect(cls.contains("ansi-italic")).toBe(true);
    expect(cls.contains("ansi-underline")).toBe(true);
  });

  it("uses classes for the 16 basic colours", () => {
    host.replaceChildren(outputFragment("x", [span(0, 1, { fg: 2, bg: 4 })]));
    const cls = host.querySelector("span")!.classList;
    expect(cls.contains("ansi-green-fg")).toBe(true);
    expect(cls.contains("ansi-blue-bg")).toBe(true);
  });

  it("maps a bright index to its bright class", () => {
    host.appendChild(outputFragment("x", [span(0, 1, { fg: 9 })]));
    expect(host.querySelector("span")!.classList.contains("ansi-bright-red-fg")).toBe(true);
  });

  // The 256-colour palette is computed rather than tabulated, so these pin the
  // formula: index 16 is the cube origin, 231 its far corner, and 232-255 the
  // grey ramp from 8 in steps of 10.
  it("computes the 256-colour cube", () => {
    host.replaceChildren(outputFragment("x", [span(0, 1, { fg: 16 })]));
    expect(host.querySelector("span")!.style.color).toBe("rgb(0 0 0)");

    host.replaceChildren(outputFragment("x", [span(0, 1, { fg: 231 })]));
    expect(host.querySelector("span")!.style.color).toBe("rgb(255 255 255)");

    // 208 is the familiar orange: n=192, so 192/36=5 → 255 red, (192/6)%6=2 →
    // 135 green, 192%6=0 → 0 blue.
    host.replaceChildren(outputFragment("x", [span(0, 1, { fg: 208 })]));
    expect(host.querySelector("span")!.style.color).toBe("rgb(255 135 0)");
  });

  it("computes the 256-colour grey ramp", () => {
    host.replaceChildren(outputFragment("x", [span(0, 1, { fg: 232 })]));
    expect(host.querySelector("span")!.style.color).toBe("rgb(8 8 8)");
    host.replaceChildren(outputFragment("x", [span(0, 1, { fg: 255 })]));
    expect(host.querySelector("span")!.style.color).toBe("rgb(238 238 238)");
  });

  it("renders truecolour as an rgb value", () => {
    // 0x1000000 | (10<<16) | (20<<8) | 30
    const packed = 0x1000000 | (10 << 16) | (20 << 8) | 30;
    host.appendChild(outputFragment("x", [span(0, 1, { fg: packed })]));
    expect(host.querySelector("span")!.style.color).toBe("rgb(10 20 30)");
  });

  // Inverse swaps the colour VALUES rather than relying on a filter, because
  // only this code knows what the two colours resolve to once defaults are in
  // play.
  it("swaps foreground and background for inverse", () => {
    host.appendChild(outputFragment("x", [span(0, 1, { fg: 1, bg: 4, attrs: 8 })]));
    const cls = host.querySelector("span")!.classList;
    expect(cls.contains("ansi-blue-fg")).toBe(true);
    expect(cls.contains("ansi-red-bg")).toBe(true);
    // .ansi-inverse sets BOTH colour properties, so adding it here would
    // override the swapped classes and paint the default inverse pair instead.
    expect(cls.contains("ansi-inverse-fg")).toBe(false);
    expect(cls.contains("ansi-inverse-bg")).toBe(false);
  });

  it("resolves the default side when inverse has only one explicit colour", () => {
    host.appendChild(outputFragment("x", [span(0, 1, { fg: 2, attrs: 8 })]));
    const cls = host.querySelector("span")!.classList;
    // fg green, bg default → swapped: bg becomes green, fg takes the default
    // background. Only the default SIDE gets an inverse class.
    expect(cls.contains("ansi-green-bg")).toBe(true);
    expect(cls.contains("ansi-inverse-fg")).toBe(true);
    expect(cls.contains("ansi-inverse-bg")).toBe(false);
  });

  it("resolves both sides when inverse has nothing to swap", () => {
    host.appendChild(outputFragment("x", [span(0, 1, { attrs: 8 })]));
    const cls = host.querySelector("span")!.classList;
    expect(cls.contains("ansi-inverse-fg")).toBe(true);
    expect(cls.contains("ansi-inverse-bg")).toBe(true);
  });

  it("indexes by UTF-16 units, so a span after an emoji lands correctly", () => {
    // The emoji is one code point but TWO UTF-16 units, which is why the
    // server counts in the browser's unit rather than bytes.
    const text = "\u{1F600}red";
    host.appendChild(outputFragment(text, [span(2, 5, { fg: 1 })]));
    expect(host.querySelector("span")!.textContent).toBe("red");
  });
});

describe("renderOutput", () => {
  it("replaces previous content rather than appending", () => {
    renderOutput(host, "first", []);
    renderOutput(host, "second", []);
    expect(host.textContent).toBe("second");
  });
});

describe("appendOutput", () => {
  it("appends without disturbing what is already there", () => {
    renderOutput(host, "one", []);
    appendOutput(host, "two", [], 3);
    expect(host.textContent).toBe("onetwo");
  });

  it("rebases absolute span offsets onto the chunk", () => {
    renderOutput(host, "abc", []);
    // The chunk's text starts at offset 3 of the accumulated output, and its
    // span addresses [4,6) absolutely, so it must paint "ef" not "de".
    appendOutput(host, "def", [span(4, 6, { fg: 1 })], 3);
    expect(host.textContent).toBe("abcdef");
    expect(host.querySelector("span")!.textContent).toBe("ef");
  });

  it("still escapes markup on the live path", () => {
    appendOutput(host, "<b>x</b>", [], 0);
    expect(host.querySelectorAll("b")).toHaveLength(0);
    expect(host.textContent).toBe("<b>x</b>");
  });
});
