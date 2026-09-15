// The scan's own contract: a position indexes the ORIGINAL text, occurrences do
// not overlap, and the case flag decides whether the fold is applied. The
// cross-language rows are occurrences.node.test.ts's, read off the Go-produced
// fixture; these are the shapes that file does not need a fixture for.

import { describe, it, expect } from "vitest";
import { fold } from "./fold.js";
import { occurrences, prepare } from "./scan.js";

describe("prepare", () => {
  it("folds the query for a case-insensitive scan and keeps it literal for a case-sensitive one", () => {
    expect(prepare("RETRY", false)).toEqual({ text: "retry", caseSensitive: false });
    expect(prepare("RETRY", true)).toEqual({ text: "RETRY", caseSensitive: true });
  });

  it("keeps a leading or trailing space, because a literal find means the characters typed", () => {
    // Trimming is search-popup.ts's per `kind`; the two literal finds keep a
    // space significant, so the kernel must not trim underneath them.
    expect(prepare(" a ", false).text).toBe(" a ");
    expect(occurrences("a a", prepare(" a", false))).toEqual([1]);
  });
});

describe("occurrences", () => {
  it("reports non-overlapping hits in text order", () => {
    expect(occurrences("aaa", prepare("aa", false))).toEqual([0]);
    expect(occurrences("aaaa", prepare("aa", false))).toEqual([0, 2]);
    expect(occurrences("abababa", prepare("aba", false))).toEqual([0, 4]);
  });

  it("matches across case only when the scan is case-insensitive", () => {
    expect(occurrences("Retry retry RETRY", prepare("retry", false))).toEqual([0, 6, 12]);
    expect(occurrences("Retry retry RETRY", prepare("retry", true))).toEqual([6]);
    expect(occurrences("\u212a k K", prepare("K", true))).toEqual([4]);
  });

  it("yields nothing for an empty needle, an empty text, or a needle longer than the text", () => {
    // `"abc".indexOf("", i)` is i at every position, so an empty needle would be
    // a hit per code unit; every surface refuses an empty query before it gets
    // here, and the kernel answers the same way rather than looping.
    expect(occurrences("abc", prepare("", false))).toEqual([]);
    expect(occurrences("", prepare("x", false))).toEqual([]);
    expect(occurrences("ab", prepare("abc", false))).toEqual([]);
  });

  it("indexes the original past a code point whose lowercase grows", () => {
    // Ten U+0130 ahead of the match. `toLowerCase()` on the whole text would put
    // the hit ten units late: the wrong line in the editor, the wrong characters
    // marked in the transcript.
    const text = `${"\u0130".repeat(10)}needle`;
    const n = prepare("needle", false);
    expect(occurrences(text, n)).toEqual([10]);
    expect(text.slice(10, 10 + n.text.length)).toBe("needle");
  });

  it("indexes in UTF-16 code units, two per astral code point", () => {
    expect(occurrences("😀😀x", prepare("x", false))).toEqual([4]);
    expect(occurrences("x😀y😀", prepare("😀", false))).toEqual([1, 4]);
  });

  it("cuts the original at every hit to a slice that folds to the needle", () => {
    // The contract over the whole row rather than a position: whatever the fold
    // did to the text, the ORIGINAL at a hit is the needle up to case.
    const text = "\u212a😀ΟΔΟΣ k İ";
    const n = prepare("K", false);
    const hits = occurrences(text, n);
    expect(hits).toEqual([0, 8]);
    for (const at of hits) {
      expect(fold(text.slice(at, at + n.text.length))).toBe(n.text);
    }
  });

  it("matches the medial sigma in a folded final sigma and never the final form", () => {
    expect(occurrences("ΟΔΟΣ", prepare("σ", false))).toEqual([3]);
    expect(occurrences("ΟΔΟΣ", prepare("ς", false))).toEqual([]);
  });
});
