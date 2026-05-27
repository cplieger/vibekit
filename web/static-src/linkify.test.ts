// Property-based test for linkify.ts PATH_RX regex round-trip invariants.
// Generates random file paths from valid segments and extensions, embeds them
// in prose with various boundary characters, and asserts the regex captures
// exactly the expected path (no more, no less).

import { describe, it, expect } from "vitest";
import fc from "fast-check";

// Re-create the regex and constants locally to test in isolation (no DOM needed).
const FILE_EXTS = [
  "ts",
  "tsx",
  "js",
  "jsx",
  "mjs",
  "cjs",
  "go",
  "mod",
  "sum",
  "py",
  "rs",
  "java",
  "kt",
  "rb",
  "php",
  "cs",
  "cpp",
  "cc",
  "c",
  "h",
  "hpp",
  "sh",
  "bash",
  "zsh",
  "json",
  "yaml",
  "yml",
  "toml",
  "xml",
  "ini",
  "env",
  "md",
  "mdx",
  "txt",
  "rst",
  "html",
  "htm",
  "css",
  "scss",
  "sass",
  "sql",
  "graphql",
  "proto",
  "tmp",
  "log",
  "lock",
  "Dockerfile",
];

const PATH_RX = new RegExp(
  "(?<![\\w/.-])([\\w.-]+\\/[\\w./-]*\\.(?:" +
    FILE_EXTS.join("|") +
    "))(?::(\\d+)(?::\\d+)?)?(?![\\w/.-])",
  "g",
);

/** Arbitrary for a single path segment (alphanumeric only, no dots/dashes to avoid lookbehind issues). */
const segment = fc
  .array(fc.constantFrom(..."abcdefghijklmnopqrstuvwxyz0123456789".split("")), {
    minLength: 1,
    maxLength: 8,
  })
  .map((cs) => cs.join(""));

/** Arbitrary for a valid file extension from FILE_EXTS. */
const ext = fc.constantFrom(...FILE_EXTS);

/** Arbitrary for a multi-segment path like `src/foo/bar.ts`. */
const validPath = fc
  .tuple(fc.array(segment, { minLength: 1, maxLength: 3 }), segment, ext)
  .map(([dirs, basename, e]) => `${dirs.join("/")}/${basename}.${e}`);

/** Arbitrary for an optional line number suffix. */
const lineSuffix = fc.oneof(
  fc.constant(""),
  fc.nat({ max: 9999 }).map((n) => `:${n}`),
  fc.tuple(fc.nat({ max: 9999 }), fc.nat({ max: 200 })).map(([l, c]) => `:${l}:${c}`),
);

/**
 * Boundary characters that are NOT in [\w/.-] — these ensure the lookbehind
 * and lookahead assertions pass, so the regex will match.
 */
const safeBoundaryBefore = fc.constantFrom(" ", "\n", "\t", "(", '"', "'", ",", ";", "[", "{");
const safeBoundaryAfter = fc.constantFrom(" ", "\n", "\t", ")", '"', "'", ",", ";", "]", "}");

describe("PATH_RX property-based tests", () => {
  it("matches valid paths embedded in prose with safe boundaries", () => {
    fc.assert(
      fc.property(
        validPath,
        lineSuffix,
        safeBoundaryBefore,
        safeBoundaryAfter,
        (path, suffix, pre, post) => {
          const input = `${pre}${path}${suffix}${post}`;
          PATH_RX.lastIndex = 0;
          const m = PATH_RX.exec(input);
          expect(m).not.toBeNull();
          expect(m![1]).toBe(path);
        },
      ),
      { numRuns: 500 },
    );
  });

  it("separates path from line number in captured groups", () => {
    fc.assert(
      fc.property(validPath, fc.nat({ max: 9999 }), (path, line) => {
        const input = ` ${path}:${line} `;
        PATH_RX.lastIndex = 0;
        const m = PATH_RX.exec(input);
        expect(m).not.toBeNull();
        expect(m![1]).toBe(path);
        expect(m![2]).toBe(String(line));
      }),
      { numRuns: 300 },
    );
  });

  it("separates path from line:col in captured groups (col not captured)", () => {
    fc.assert(
      fc.property(validPath, fc.nat({ max: 9999 }), fc.nat({ max: 200 }), (path, line, col) => {
        const input = ` ${path}:${line}:${col} `;
        PATH_RX.lastIndex = 0;
        const m = PATH_RX.exec(input);
        expect(m).not.toBeNull();
        expect(m![1]).toBe(path);
        expect(m![2]).toBe(String(line));
        // col is consumed by the regex but not captured in a separate group
      }),
      { numRuns: 200 },
    );
  });

  it("does not capture trailing punctuation as part of the path", () => {
    // Punctuation chars that are NOT in [\w/.-] won't be captured
    const punct = fc.constantFrom(",", ")", ";", ":", "'", '"', "]", "}");
    fc.assert(
      fc.property(validPath, punct, (path, p) => {
        const input = ` ${path}${p} `;
        PATH_RX.lastIndex = 0;
        const m = PATH_RX.exec(input);
        expect(m).not.toBeNull();
        // The matched path group should be exactly the path, not including punctuation
        expect(m![1]).toBe(path);
      }),
      { numRuns: 300 },
    );
  });

  it("negative lookbehind rejects paths preceded by [\\w/.-]", () => {
    // When a path is preceded by a character in [\w/.-], the regex should not
    // match starting at the path's first character.
    const lookbehindChar = fc.constantFrom("/", "x", "_", "-");
    fc.assert(
      fc.property(validPath, lookbehindChar, (path, prefix) => {
        const input = `${prefix}${path} `;
        PATH_RX.lastIndex = 0;
        const m = PATH_RX.exec(input);
        // If it matches at all, the match must NOT start at index = prefix.length
        // (the lookbehind should have rejected that position)
        if (m !== null) {
          expect(m.index).not.toBe(prefix.length);
        }
      }),
      { numRuns: 200 },
    );
  });

  it("negative lookahead rejects paths followed by [\\w/.-]", () => {
    // When a path is followed by a character in [\w/.-], the regex should not
    // match the path at that position (it would need to consume more).
    const lookaheadChar = fc.constantFrom("/", "x", "_", ".");
    fc.assert(
      fc.property(validPath, lookaheadChar, (path, suffix) => {
        const input = ` ${path}${suffix} `;
        PATH_RX.lastIndex = 0;
        const matches: RegExpExecArray[] = [];
        let m: RegExpExecArray | null;
        while ((m = PATH_RX.exec(input)) !== null) {matches.push(m);}
        // If any match captures exactly our path, the lookahead failed to reject
        // (the suffix char should prevent matching at that exact boundary)
        let violated = false;
        for (const match of matches) {
          if (match.index === 1 && match[1] === path) {
            violated = true;
          }
        }
        expect(violated).toBe(false);
      }),
      { numRuns: 200 },
    );
  });
});
