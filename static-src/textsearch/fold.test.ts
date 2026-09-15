// The TypeScript half of the fold contract shared with Go's textsearch.Fold: the
// simple per-code-point lowercase, with the one code point whose lowercase grows
// repaired, so an index into the fold is an index into the original. Browser
// project on purpose: `toLowerCase` is the engine's, and the engine this client
// ships into is the one whose answer counts.

import { describe, it, expect } from "vitest";
import { fold } from "./fold.js";

describe("fold preserves length", () => {
  it("over every code point, one code unit in is one code unit out", () => {
    // The construction gate, the twin of Go's TestFold_PreservesRuneCount. Every
    // scalar value, surrogates skipped: a lone surrogate is its own case below.
    const grew: string[] = [];
    for (let cp = 0; cp <= 0x10ffff; cp++) {
      if (cp >= 0xd800 && cp <= 0xdfff) {
        continue;
      }
      const s = String.fromCodePoint(cp);
      if (fold(s).length !== s.length) {
        grew.push(`U+${cp.toString(16).toUpperCase()}`);
      }
    }
    expect(grew).toEqual([]);
  });

  it("folds U+0130 to a bare i, the one code point whose lowercase grows", () => {
    // `"\u0130".toLowerCase()` is `"i\u0307"`, two units for one; Go's
    // unicode.ToLower gives `i`. The repair is what keeps the gate above true.
    expect(fold("\u0130")).toBe("i");
    expect(fold("\u0130\u0130i")).toBe("iii");
  });

  it("keeps a lone surrogate as it is", () => {
    expect(fold("\ud800a")).toBe("\ud800a");
    expect(fold("a\udc00")).toBe("a\udc00");
  });
});

describe("fold is the simple mapping", () => {
  it("lowercases a final sigma to the medial form, unlike String.prototype.toLowerCase", () => {
    // Final_Sigma is context-sensitive: `"ΟΔΟΣ".toLowerCase()` is `"οδος"`. Go
    // maps rune by rune and gives `"οδοσ"`, so the fold has to as well or a
    // needle that matches the Go text misses the TypeScript text.
    expect(fold("ΟΔΟΣ")).toBe("οδοσ");
  });

  it("lowercases the length-changing runes Go's census names to their simple targets", () => {
    expect(fold("\u212a")).toBe("k");
    expect(fold("\u1e9e")).toBe("ß");
    expect(fold("\u023a")).toBe("\u2c65");
  });

  it("lowercases ascii and leaves what has no case alone", () => {
    expect(fold("Retry RETRY retry")).toBe("retry retry retry");
    expect(fold("😀 42 _")).toBe("😀 42 _");
    expect(fold("")).toBe("");
  });
});
