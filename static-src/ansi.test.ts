// Tests for ansi.ts — the ANSI→HTML converter factory + shared helper.
//
// The vitest ansi_up alias (see vitest.config.ts) points at a stub whose
// ansi_to_html() strips SGR escape codes and HTML-escapes the remainder, so
// these tests assert the factory's wiring + the escape/strip contract rather
// than real colour rendering. ESC is written as the \u001b unicode escape.

import { describe, it, expect } from "vitest";

import { createAnsiConverter, ansiToHtml } from "./ansi.js";

const ESC = "\u001b";

describe("createAnsiConverter", () => {
  it("returns an independent converter instance on each call", () => {
    const a = createAnsiConverter();
    const b = createAnsiConverter();
    expect(a).not.toBe(b);
    expect(typeof a.toHtml).toBe("function");
  });

  it("strips ANSI escape codes and escapes HTML", () => {
    const conv = createAnsiConverter();
    expect(conv.toHtml(`${ESC}[32mPASS${ESC}[0m`)).toBe("PASS");
    expect(conv.toHtml("<b> & </b>")).toBe("&lt;b&gt; &amp; &lt;/b&gt;");
  });

  it("renders each instance independently", () => {
    const a = createAnsiConverter();
    const b = createAnsiConverter();
    // Two converters used concurrently must not throw or interfere.
    expect(a.toHtml("alpha")).toBe("alpha");
    expect(b.toHtml("beta")).toBe("beta");
    expect(a.toHtml("-again")).toBe("-again");
  });
});

describe("ansiToHtml (shared converter)", () => {
  it("converts via the shared module-level converter", () => {
    expect(ansiToHtml(`${ESC}[31merr${ESC}[0m`)).toBe("err");
    expect(ansiToHtml("a & b")).toBe("a &amp; b");
  });
});
