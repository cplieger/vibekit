// The cross-language pin for the match kernel. Go PRODUCES
// internal/textsearch/testdata/occurrences.json from its own Needle.Occurrences
// (TestOccurrencesFixture, regenerated behind UPDATE_GOLDEN=1) and this file
// CONSUMES it: every row's hit count and every hit's `utf16` position must be
// what `occurrences` answers here. Two hand-written columns that no test compares
// would not be a contract; one producer and one consumer are.
//
// Node placement because the fixture is a disk read, which throws on import in
// the browser project, so a misplacement is loud rather than vacuous.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { occurrences, prepare } from "./scan.js";

const FIXTURE_PATH = "../../internal/textsearch/testdata/occurrences.json";

interface Hit {
  readonly rune: number;
  readonly byte: number;
  readonly utf16: number;
}

interface Row {
  readonly name: string;
  readonly text: string;
  readonly needle: string;
  readonly case_sensitive: boolean;
  readonly hits: readonly Hit[];
}

interface Fixture {
  readonly _comment: readonly string[];
  readonly rows: readonly Row[];
}

function loadFixture(): Fixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as Fixture;
}

describe("the occurrences contract shared with the Go implementation", () => {
  const fx = loadFixture();

  it("carries rows, hits, and the rows this side exists to disagree on", () => {
    // An empty fixture, or one without the rows that separate the simple mapping
    // from String.prototype.toLowerCase, would let every case below pass forever.
    expect(fx.rows.length).toBeGreaterThanOrEqual(18);
    expect(fx.rows.flatMap((r) => r.hits).length).toBeGreaterThanOrEqual(26);
    const names = fx.rows.map((r) => r.name);
    expect(names).toContain("dotted_capital_I_folds_to_i");
    expect(names).toContain("final_sigma_medial_needle_matches");
    expect(names).toContain("final_sigma_final_needle_misses");
    expect(names).toContain("astral_runes_before_the_match");
    for (const row of fx.rows) {
      expect(Array.isArray(row.hits), `${row.name}: hits is an array, never null`).toBe(true);
    }
  });

  it("names static-src/textsearch as its consumer", () => {
    expect(fx._comment.join("\n")).toContain("static-src/textsearch");
  });

  for (const row of loadFixture().rows) {
    it(`agrees with Go on "${row.name}"`, () => {
      const got = occurrences(row.text, prepare(row.needle, row.case_sensitive));
      expect(got, "hit count").toHaveLength(row.hits.length);
      expect(got, "every utf16 position").toEqual(row.hits.map((h) => h.utf16));
    });
  }

  it("has utf16, rune and byte columns that name one position each", () => {
    // The three columns are three spellings of one prefix of the ORIGINAL text.
    // Checking them against each other is what makes the utf16 column a fact
    // about the text rather than a number the producer happened to write.
    const utf8 = new TextEncoder();
    for (const row of loadFixture().rows) {
      for (const h of row.hits) {
        const prefix = row.text.slice(0, h.utf16);
        expect([...prefix].length, `${row.name}: rune of utf16 ${h.utf16}`).toBe(h.rune);
        expect(utf8.encode(prefix).length, `${row.name}: byte of utf16 ${h.utf16}`).toBe(h.byte);
      }
    }
  });
});
