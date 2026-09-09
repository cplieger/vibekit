// Unit tests for files-shared.ts — pure functions, no DOM dependency.
import { describe, it, expect, vi } from "vitest";
import fc from "fast-check";
import {
  formatSize,
  joinPath,
  parentPath,
  sortEntries,
  withAncestors,
  matchesRelative,
} from "./files-shared.js";
import { isSafeUrl } from "./utils-url.js";
import { relativeTime } from "./utils-format.js";

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

// These two used to assert the ROOTLESS space — `joinPath(".", "file.txt")` was
// "file.txt" and `parentPath("src")` was "." — so the suite agreed with the
// defect and could not see it. The space is container-absolute; the JOIN against
// the git-status index, the normaliser and the route agreement live in
// `files-path-space.test.ts`, which is the file that pins the contract itself.
describe("joinPath", () => {
  const cases: [string, string, string][] = [
    ["/", "workspace", "/workspace"],
    ["/", "file.txt", "/file.txt"],
    ["/workspace", "vibekit", "/workspace/vibekit"],
    ["/workspace/vibekit", "static-src", "/workspace/vibekit/static-src"],
    ["/workspace/", "vibekit", "/workspace/vibekit"],
    ["/workspace//", "vibekit", "/workspace/vibekit"],
  ];

  for (const [base, name, expected] of cases) {
    it(`joins "${base}" + "${name}" → "${expected}"`, () => {
      expect(joinPath(base, name)).toBe(expected);
    });
  }
});

describe("parentPath", () => {
  const cases: [string, string][] = [
    ["/", "/"],
    ["", "/"],
    ["/workspace", "/"],
    ["/workspace/vibekit", "/workspace"],
    ["/workspace/vibekit/static-src", "/workspace/vibekit"],
    ["/a/b/c/d", "/a/b/c"],
  ];

  for (const [input, expected] of cases) {
    it(`parent of "${input}" → "${expected}"`, () => {
      expect(parentPath(input)).toBe(expected);
    });
  }
});

describe("isSafeUrl", () => {
  const safe: string[] = [
    "https://example.com",
    "http://localhost:8080/path",
    "mailto:user@example.com",
    "/relative/path",
    "./local",
    "#anchor",
    "//cdn.example.com/image.png",
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
    ["\x01javascript:alert(1)", "C0 control lead javascript:"],
    ["\x1fjavascript:alert(1)", "high C0 control lead javascript:"],
    [" \x01 javascript:alert(1)", "C0 control between spaces javascript:"],
    ["\x01data:text/html,x", "C0 control lead data:"],
    ["vbscript:MsgBox", "basic vbscript:"],
    ["VBSCRIPT:run", "uppercase vbscript:"],
    ["data:text/html,<script>alert(1)</script>", "basic data:"],
    ["  data:text/html,...", "leading whitespace data:"],
    ["file:///etc/passwd", "basic file:"],
    ["FILE:///etc/shadow", "uppercase file:"],
    ["vscode://file/workspace/main.go", "unapproved vscode:"],
    ["blob:https://example.com/id", "unapproved blob:"],
    ["tel:+1234567890", "unapproved tel:"],
  ];

  for (const [url, desc] of unsafe) {
    // eslint-disable-next-line no-control-regex -- defensive check
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

  it("no false negatives: blocked prefix + suffix is always rejected", () => {
    fc.assert(
      fc.property(fc.constantFrom(...blockedPrefixes), fc.string(), (prefix, suffix) => {
        expect(isSafeUrl(prefix + suffix)).toBe(false);
      }),
      { numRuns: 500 },
    );
  });

  // The WHATWG URL parser removes every leading C0 control or space before it
  // reads a scheme, so the gate has to remove at least as much or the browser sees
  // a scheme the gate did not. A decoder makes that lead spellable in printable
  // ASCII (`&#1;`), which is how this arrives.
  it("no false negatives: a C0 control or space lead is stripped before the scheme", () => {
    const lead = fc
      .array(
        fc.integer({ min: 0x00, max: 0x20 }).map((c) => String.fromCharCode(c)),
        { minLength: 1, maxLength: 8 },
      )
      .map((chars) => chars.join(""));

    fc.assert(
      fc.property(lead, fc.constantFrom(...blockedPrefixes), fc.string(), (pre, prefix, suffix) => {
        expect(isSafeUrl(pre + prefix + suffix)).toBe(false);
      }),
      { numRuns: 500 },
    );
  });

  // The allowlist is the contract, so an absolute scheme it does not name is
  // refused whatever that scheme is; `vscode:`, `blob:` and `tel:` are the cases
  // the table above names.
  it("no false negatives: an absolute scheme outside the allowlist is rejected", () => {
    const alpha = fc.constantFrom(..."abcdefghijklmnopqrstuvwxyz".split(""));
    const schemeChar = fc.constantFrom(..."abcdefghijklmnopqrstuvwxyz0123456789+.-".split(""));
    const scheme = fc
      .tuple(alpha, fc.array(schemeChar, { maxLength: 12 }))
      .map(([head, rest]) => head + rest.join(""))
      .filter((s) => s !== "http" && s !== "https" && s !== "mailto");

    fc.assert(
      fc.property(scheme, fc.string(), (s, rest) => {
        expect(isSafeUrl(`${s}:${rest}`)).toBe(false);
      }),
      { numRuns: 1000 },
    );
  });

  // The over-blocking bound: a value with no scheme is always allowed, because
  // the browser resolves a relative path, an anchor or a `//host` URL against
  // the document's own HTTP(S) location. The tail cannot spell a scheme — `:` is
  // not in its alphabet — so a failure here is the gate demanding one.
  it("no false positives: a scheme-less value is allowed", () => {
    const pathChar = fc.constantFrom(..."aZ0/._-~?&=%#".split(""));
    const tail = fc.array(pathChar, { maxLength: 20 }).map((chars) => chars.join(""));

    fc.assert(
      fc.property(fc.constantFrom("", "/", "./", "../", "#", "?", "//"), tail, (prefix, rest) => {
        expect(isSafeUrl(prefix + rest)).toBe(true);
      }),
      { numRuns: 1000 },
    );
  });
});

describe("withAncestors", () => {
  it("adds every ancestor directory of a nested path", () => {
    expect([...withAncestors(["a/b/c.go"])].sort()).toEqual(["a", "a/b", "a/b/c.go"]);
  });

  it("leaves a top-level path alone (no empty-string ancestor)", () => {
    expect([...withAncestors(["main.go"])]).toEqual(["main.go"]);
  });

  it("dedupes shared ancestors across paths", () => {
    expect([...withAncestors(["a/b/one.go", "a/b/two.go"])].sort()).toEqual([
      "a",
      "a/b",
      "a/b/one.go",
      "a/b/two.go",
    ]);
  });

  it("is empty for no input", () => {
    expect(withAncestors([]).size).toBe(0);
  });
});

describe("matchesRelative", () => {
  // The ancestor expansion plus this suffix rule is what lets ONE rule decorate
  // a file row and a folder row without the browser knowing the workspace root.
  const changed = withAncestors(["static-src/files.ts"]);

  it("matches the file under any root prefix", () => {
    expect(matchesRelative("/workspace/vibekit/static-src/files.ts", changed)).toBe(true);
    expect(matchesRelative("/somewhere/else/static-src/files.ts", changed)).toBe(true);
  });

  it("matches the containing folder, which is what decorates a collapsed row", () => {
    expect(matchesRelative("/workspace/vibekit/static-src", changed)).toBe(true);
  });

  it("matches a bare relative path (no root prefix at all)", () => {
    expect(matchesRelative("static-src/files.ts", changed)).toBe(true);
  });

  it("does not match a sibling that merely ends with the same characters", () => {
    // The `/` boundary is the whole point: "other-static-src" is not a match.
    expect(matchesRelative("/workspace/vibekit/other-static-src/files.ts", changed)).toBe(false);
    expect(matchesRelative("/workspace/notfiles.ts", withAncestors(["files.ts"]))).toBe(false);
  });

  it("does not match an unrelated path or an empty set", () => {
    expect(matchesRelative("/workspace/vibekit/main.go", changed)).toBe(false);
    expect(matchesRelative("/workspace/vibekit/static-src/files.ts", new Set())).toBe(false);
  });

  // The match probes the ROW's own suffixes against the set rather than walking the
  // set, so the answer must not depend on how big the set is — after a long session
  // it holds every path the chat touched plus every ancestor. A thousand entries is
  // where a set-walking implementation was O(|changed|) per row.
  describe("against a large change set", () => {
    const big = withAncestors(
      Array.from({ length: 1000 }, (_, i) => `pkg/mod${String(i)}/file${String(i)}.go`),
    );

    it("finds a depth-2 match", () => {
      expect(matchesRelative("/workspace/app/pkg/mod742/file742.go", big)).toBe(true);
    });

    it("finds the containing folder of a depth-2 match", () => {
      expect(matchesRelative("/workspace/app/pkg/mod742", big)).toBe(true);
    });

    it("answers false for a path the set does not hold", () => {
      expect(matchesRelative("/workspace/app/pkg/mod742/other.go", big)).toBe(false);
      expect(matchesRelative("/workspace/app/pkg/mod1000/file1000.go", big)).toBe(false);
    });
  });

  // The multi-mount case. The browser lists an allow-list of mounts, so a
  // `/config/...` row has no workspace-relative form at all — and a chat's change
  // set is workspace-relative, so such a row must not be attributed to it.
  it("does not attribute a /config row to a workspace-relative set", () => {
    const rels = withAncestors(["static-src/files.ts"]);
    expect(matchesRelative("/config/chats/c-1.json", rels)).toBe(false);
    expect(matchesRelative("/config", rels)).toBe(false);
  });

  it("DOES match a /config row whose tail coincides with a relative path", () => {
    // Characterization, not a goal. The rule is a suffix rule on a `/` boundary,
    // so a mount row whose tail happens to spell a workspace-relative path is
    // attributed. Pinned because it is the one place the suffix rule is loose, and
    // because it is what proves the O(depth) inversion changed no semantics: the
    // set-walking form answered true here too (`"/config/mcp.json".endsWith(
    // "/config/mcp.json")`). Closing it needs the row's own mount, which the
    // listing does not carry.
    const rels = withAncestors(["config/mcp.json"]);
    expect(matchesRelative("/config/mcp.json", rels)).toBe(true);
    expect(matchesRelative("/config", rels)).toBe(true);
  });
});
