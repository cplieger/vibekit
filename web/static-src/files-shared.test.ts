// Unit tests for files-shared.ts — pure functions, no DOM dependency.
import { describe, it, expect, vi } from "vitest";
import fc from "fast-check";
import { formatSize, joinPath, parentPath, displayPath, isSafeUrl, sortEntries, relativeTime } from "./files-shared.js";

describe("formatSize", () => {
  const cases: [number, string][] = [
    [0, "0 B"],
    [1, "1 B"],
    [512, "512 B"],
    [1023, "1023 B"],
    [1024, "1.0 KB"],
    [1536, "1.5 KB"],
    [10240, "10.0 KB"],
    [1048576, "1.0 MB"],
    [1572864, "1.5 MB"],
    [1073741824, "1.0 GB"],
    [1610612736, "1.5 GB"],
  ];

  for (const [input, expected] of cases) {
    it(`formats ${String(input)} bytes as "${expected}"`, () => {
      expect(formatSize(input)).toBe(expected);
    });
  }
});

describe("joinPath", () => {
  const cases: [string, string, string][] = [
    [".", "file.txt", "file.txt"],
    [".", "subdir", "subdir"],
    ["src", "main.ts", "src/main.ts"],
    ["src/lib", "utils.ts", "src/lib/utils.ts"],
    ["src/", "main.ts", "src/main.ts"],
    ["src//", "main.ts", "src/main.ts"],
  ];

  for (const [base, name, expected] of cases) {
    it(`joins "${base}" + "${name}" → "${expected}"`, () => {
      expect(joinPath(base, name)).toBe(expected);
    });
  }
});

describe("parentPath", () => {
  const cases: [string, string][] = [
    [".", "."],
    ["", "."],
    ["file.txt", "."],
    ["src", "."],
    ["src/lib", "src"],
    ["src/lib/utils", "src/lib"],
    ["a/b/c/d", "a/b/c"],
  ];

  for (const [input, expected] of cases) {
    it(`parent of "${input}" → "${expected}"`, () => {
      expect(parentPath(input)).toBe(expected);
    });
  }
});

describe("displayPath", () => {
  const cases: [string, string][] = [
    [".", "/"],
    ["src", "/src"],
    ["src/lib", "/src/lib"],
    ["a/b/c", "/a/b/c"],
  ];

  for (const [input, expected] of cases) {
    it(`displays "${input}" as "${expected}"`, () => {
      expect(displayPath(input)).toBe(expected);
    });
  }
});

describe("isSafeUrl", () => {
  const safe: string[] = [
    "https://example.com",
    "http://localhost:8080/path",
    "/relative/path",
    "./local",
    "#anchor",
    "mailto:user@example.com",
    "tel:+1234567890",
  ];

  for (const url of safe) {
    it(`allows safe URL: "${url}"`, () => {
      expect(isSafeUrl(url)).toBe(true);
    });
  }

  const unsafe: [string, string][] = [
    ["javascript:alert(1)", "basic javascript:"],
    ["JAVASCRIPT:alert(1)", "uppercase javascript:"],
    ["JavaScript:void(0)", "mixed case javascript:"],
    ["java\tscript:alert(1)", "tab bypass javascript:"],
    ["java\nscript:alert(1)", "newline bypass javascript:"],
    ["java\rscript:alert(1)", "carriage return bypass"],
    ["java\x00script:alert(1)", "null byte bypass"],
    ["  javascript:alert(1)", "leading whitespace"],
    ["vbscript:MsgBox", "basic vbscript:"],
    ["VBSCRIPT:run", "uppercase vbscript:"],
    ["data:text/html,<script>alert(1)</script>", "basic data:"],
    ["  data:text/html,...", "leading whitespace data:"],
    ["file:///etc/passwd", "basic file:"],
    ["FILE:///etc/shadow", "uppercase file:"],
  ];

  for (const [url, desc] of unsafe) {
    it(`blocks unsafe URL (${desc}): "${url.replace(/[\x00-\x1f]/g, "·")}"`, () => {
      expect(isSafeUrl(url)).toBe(false);
    });
  }
});

describe("sortEntries", () => {
  const cases: { desc: string; input: { name: string; isDir: boolean }[]; expected: string[] }[] = [
    {
      desc: "directories before files",
      input: [
        { name: "file.txt", isDir: false },
        { name: "dir", isDir: true },
      ],
      expected: ["dir", "file.txt"],
    },
    {
      desc: "alphabetical within same type",
      input: [
        { name: "banana", isDir: false },
        { name: "apple", isDir: false },
        { name: "cherry", isDir: false },
      ],
      expected: ["apple", "banana", "cherry"],
    },
    {
      desc: "dirs sorted among dirs, files among files",
      input: [
        { name: "z-file", isDir: false },
        { name: "b-dir", isDir: true },
        { name: "a-file", isDir: false },
        { name: "a-dir", isDir: true },
      ],
      expected: ["a-dir", "b-dir", "a-file", "z-file"],
    },
    {
      desc: "empty array",
      input: [],
      expected: [],
    },
    {
      desc: "single entry",
      input: [{ name: "only", isDir: false }],
      expected: ["only"],
    },
  ];

  for (const { desc, input, expected } of cases) {
    it(desc, () => {
      const result = sortEntries(input);
      expect(result.map((e) => e.name)).toEqual(expected);
    });
  }

  it("does not mutate the original array", () => {
    const original = [
      { name: "b", isDir: false },
      { name: "a", isDir: true },
    ];
    const copy = [...original];
    sortEntries(original);
    expect(original).toEqual(copy);
  });
});

describe("relativeTime", () => {
  it("returns 'just now' for timestamps less than 60s ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-15T12:00:00Z"));
    expect(relativeTime(Date.now() - 30_000)).toBe("just now");
    expect(relativeTime(Date.now() - 59_000)).toBe("just now");
    vi.useRealTimers();
  });

  it("returns minutes for timestamps 1-59 minutes ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-15T12:00:00Z"));
    expect(relativeTime(Date.now() - 60_000)).toBe("1m ago");
    expect(relativeTime(Date.now() - 5 * 60_000)).toBe("5m ago");
    expect(relativeTime(Date.now() - 59 * 60_000)).toBe("59m ago");
    vi.useRealTimers();
  });

  it("returns hours for timestamps 1-23 hours ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-15T12:00:00Z"));
    expect(relativeTime(Date.now() - 3600_000)).toBe("1h ago");
    expect(relativeTime(Date.now() - 12 * 3600_000)).toBe("12h ago");
    expect(relativeTime(Date.now() - 23 * 3600_000)).toBe("23h ago");
    vi.useRealTimers();
  });

  it("returns days for timestamps 1-29 days ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-15T12:00:00Z"));
    expect(relativeTime(Date.now() - 86400_000)).toBe("1d ago");
    expect(relativeTime(Date.now() - 7 * 86400_000)).toBe("7d ago");
    expect(relativeTime(Date.now() - 29 * 86400_000)).toBe("29d ago");
    vi.useRealTimers();
  });

  it("returns months for timestamps 30-364 days ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-15T12:00:00Z"));
    expect(relativeTime(Date.now() - 30 * 86400_000)).toBe("1mo ago");
    expect(relativeTime(Date.now() - 90 * 86400_000)).toBe("3mo ago");
    vi.useRealTimers();
  });

  it("returns years for timestamps 365+ days ago", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-15T12:00:00Z"));
    expect(relativeTime(Date.now() - 365 * 86400_000)).toBe("1y ago");
    expect(relativeTime(Date.now() - 730 * 86400_000)).toBe("2y ago");
    vi.useRealTimers();
  });
});

describe("isSafeUrl property-based", () => {
  const blockedPrefixes = ["javascript:", "vbscript:", "data:", "file:"] as const;

  it("soundness: rejected URLs normalize to a blocked prefix", () => {
    fc.assert(
      fc.property(fc.string(), (s) => {
        const result = isSafeUrl(s);
        if (!result) {
          const normalized = s.trim().replace(/[\t\n\r\x00]/g, "").toLowerCase();
          const hasBlocked = blockedPrefixes.some((p) => normalized.startsWith(p));
          expect(hasBlocked).toBe(true);
        } else {
          expect(result).toBe(true);
        }
      }),
      { numRuns: 1000 },
    );
  });

  it("no false negatives: blocked prefix + suffix is always rejected", () => {
    fc.assert(
      fc.property(
        fc.constantFrom(...blockedPrefixes),
        fc.string(),
        (prefix, suffix) => {
          expect(isSafeUrl(prefix + suffix)).toBe(false);
        },
      ),
      { numRuns: 500 },
    );
  });

  it("safe strings stay safe: no blocked prefix after normalization means true", () => {
    fc.assert(
      fc.property(
        fc.string().filter((s) => {
          const n = s.trim().replace(/[\t\n\r\x00]/g, "").toLowerCase();
          return !blockedPrefixes.some((p) => n.startsWith(p));
        }),
        (s) => {
          expect(isSafeUrl(s)).toBe(true);
        },
      ),
      { numRuns: 1000 },
    );
  });
});
