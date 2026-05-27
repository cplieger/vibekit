// @vitest-environment happy-dom
// Unit tests for tool-card.ts pure functions (extractSubtitle, mcpHue).
import { describe, it, expect, vi, beforeAll } from "vitest";
import fc from "fast-check";

// Provide minimal DOM elements that transitive imports require at module level.
beforeAll(() => {
  for (const id of ["messages", "messages-wrap", "banner-stack"]) {
    if (!document.getElementById(id)) {
      const el = document.createElement("div");
      el.id = id;
      document.body.appendChild(el);
    }
  }
});

// Mock scroll.ts to avoid its eager DOM access ($.messages at module level).
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// Mock editor-openers.ts to avoid its transitive DOM dependencies.
vi.mock("./editor-openers.js", () => ({
  openFile: () => {},
  openFileDiff: () => {},
}));

// Mock tool-group.ts to avoid its transitive DOM dependencies.
vi.mock("./tool-group.js", () => ({
  trackInProgress: () => {},
}));

const { extractSubtitle, mcpHue } = await import("./tool-card.js");

// ---------------------------------------------------------------------------
// extractSubtitle — table-driven
// ---------------------------------------------------------------------------

describe("extractSubtitle", () => {
  const cases: Array<{
    name: string;
    input: Record<string, unknown> | undefined;
    expected: string;
  }> = [
    { name: "undefined input", input: undefined, expected: "" },
    { name: "empty object", input: {}, expected: "" },
    { name: "query key", input: { query: "find all files" }, expected: "find all files" },
    { name: "pattern key", input: { pattern: "*.ts" }, expected: "*.ts" },
    { name: "command key", input: { command: "ls -la" }, expected: "ls -la" },
    { name: "url key", input: { url: "https://example.com" }, expected: "https://example.com" },
    { name: "path key", input: { path: "/src/main.ts" }, expected: "/src/main.ts" },
    { name: "explanation key", input: { explanation: "doing stuff" }, expected: "doing stuff" },
    {
      name: "priority order: query wins over path",
      input: { path: "/a", query: "q" },
      expected: "q",
    },
    {
      name: "priority order: pattern wins over command",
      input: { command: "c", pattern: "p" },
      expected: "p",
    },
    { name: "empty string value skipped", input: { query: "", path: "/x" }, expected: "/x" },
    { name: "non-string value skipped", input: { query: 42, path: "/y" }, expected: "/y" },
    {
      name: "truncation at 121 chars",
      input: { query: "a".repeat(121) },
      expected: "a".repeat(117) + "\u2026",
    },
    {
      name: "exactly 120 chars not truncated",
      input: { query: "b".repeat(120) },
      expected: "b".repeat(120),
    },
    { name: "no matching keys", input: { foo: "bar", baz: "qux" }, expected: "" },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(extractSubtitle(input)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// mcpHue — table-driven snapshot tests
// ---------------------------------------------------------------------------

describe("mcpHue", () => {
  const knownServers: Array<{ server: string; hue: number }> = [
    { server: "github", hue: mcpHue("github") },
    { server: "s3", hue: mcpHue("s3") },
    { server: "postgres", hue: mcpHue("postgres") },
    { server: "filesystem", hue: mcpHue("filesystem") },
    { server: "brave-search", hue: mcpHue("brave-search") },
  ];

  it.each(knownServers)("deterministic for $server → $hue", ({ server, hue }) => {
    // Call multiple times to verify determinism.
    expect(mcpHue(server)).toBe(hue);
    expect(mcpHue(server)).toBe(hue);
  });

  it("returns value in [0, 360) for empty string", () => {
    const result = mcpHue("");
    expect(result).toBeGreaterThanOrEqual(0);
    expect(result).toBeLessThan(360);
  });

  it("different inputs produce different hues (for known distinct servers)", () => {
    const hues = new Set(knownServers.map((s) => s.hue));
    // With 5 distinct server names, we expect at least 3 distinct hues.
    expect(hues.size).toBeGreaterThanOrEqual(3);
  });
});

// ---------------------------------------------------------------------------
// mcpHue — property-based tests
// ---------------------------------------------------------------------------

describe("mcpHue properties", () => {
  it("output is always in [0, 360)", () => {
    fc.assert(
      fc.property(fc.string(), (s) => {
        const h = mcpHue(s);
        expect(h).toBeGreaterThanOrEqual(0);
        expect(h).toBeLessThan(360);
        expect(Number.isInteger(h)).toBe(true);
      }),
    );
  });

  it("deterministic: same input always yields same output", () => {
    fc.assert(
      fc.property(fc.string(), (s) => {
        expect(mcpHue(s)).toBe(mcpHue(s));
      }),
    );
  });

  it("distribution: 100 random strings cover at least 3 of 4 quadrants", () => {
    fc.assert(
      fc.property(
        fc.array(fc.string({ minLength: 1, maxLength: 50 }), { minLength: 100, maxLength: 100 }),
        (strings) => {
          const quadrants = new Set<number>();
          for (const s of strings) {
            quadrants.add(Math.floor(mcpHue(s) / 90));
          }
          expect(quadrants.size).toBeGreaterThanOrEqual(3);
        },
      ),
    );
  });
});
