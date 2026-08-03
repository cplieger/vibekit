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
const opened: string[] = [];
vi.mock("./editor-openers.js", () => ({
  openFile: (p: string) => {
    opened.push(`file:${p}`);
  },
  openFileDiff: (p: string) => {
    opened.push(`diff:${p}`);
  },
  openFileGitDiff: (p: string) => {
    opened.push(`gitdiff:${p}`);
  },
}));

// Mock tool-group.ts to avoid its transitive DOM dependencies.
vi.mock("./tool-group.js", () => ({
  trackInProgress: () => {
    /* noop */
  },
}));

const { extractSubtitle, mcpHue } = await import("./tool-card.js");

// ---------------------------------------------------------------------------
// extractSubtitle — table-driven
// ---------------------------------------------------------------------------

describe("extractSubtitle", () => {
  const cases: {
    name: string;
    input: Record<string, unknown> | undefined;
    expected: string;
  }[] = [
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
  const knownServers: { server: string; hue: number }[] = [
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

// ---------------------------------------------------------------------------
// The depth ladder's visible contract. These are task E's own done-when
// criteria, and none of them had a test before: the status word had no coverage
// at all, which is how a card printing `completed` survived.
// ---------------------------------------------------------------------------

describe("outcome is a glyph, not a word", () => {
  it("a finished card prints no status word anywhere in its text", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    for (const status of ["completed", "failed"] as const) {
      const card = buildToolCard({
        id: "t1",
        title: "strReplace",
        kind: "edit",
        status,
        input: { path: "src/a.ts", oldStr: "a", newStr: "b" },
        live: false,
      });
      // The literal wire enum must not appear as visible text.
      expect(card.textContent).not.toContain("completed");
      expect(card.textContent).not.toContain("failed");
      expect(card.querySelector(".tool-status")).toBeNull();
    }
  });

  it("tints the glyph AND composites a shape, because tint alone is one channel", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const ok = buildToolCard({
      id: "t2",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    const okIcon = ok.querySelector(".tool-icon");
    expect(okIcon?.classList.contains("is-ok")).toBe(true);
    expect(okIcon?.querySelector(".tool-outcome-badge")?.textContent).toBe("\u2713");

    const bad = buildToolCard({
      id: "t3",
      title: "executePwsh",
      kind: "execute",
      status: "failed",
      live: false,
    });
    const badIcon = bad.querySelector(".tool-icon");
    expect(badIcon?.classList.contains("is-fail")).toBe(true);
    expect(badIcon?.querySelector(".tool-outcome-badge")?.textContent).toBe("\u2717");
  });

  it("carries the outcome word in the accessible name instead", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t4",
      title: "strReplace",
      kind: "edit",
      status: "failed",
      input: { path: "src/auth.go", oldStr: "a", newStr: "b" },
      live: false,
    });
    expect(card.getAttribute("aria-label")).toContain("auth.go");
    expect(card.getAttribute("aria-label")).toContain("failed");
  });
});

describe("the depth ladder", () => {
  it("a claim-only kind gets no details region and no toggle", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t5",
      title: "read_files",
      kind: "read",
      status: "completed",
      input: { path: "src/a.ts" },
      live: false,
    });
    expect(card.querySelector(".tool-details")).toBeNull();
    expect(card.querySelector(".tool-toggle")).toBeNull();
  });

  it("an edit gets a details region — the old tier axis gave it none", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t6",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one\ntwo", newStr: "one\nTWO" },
      live: false,
    });
    expect(card.querySelector(".tool-details")).not.toBeNull();
    expect(card.querySelector(".tool-toggle")).not.toBeNull();
  });

  it("has no second View diff button — the subject is the link", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t7",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one", newStr: "two" },
      live: false,
    });
    expect(card.querySelector(".tool-diff-view-btn")).toBeNull();
    expect(card.textContent).not.toContain("View diff");
  });

  it("the filename opens the DIFF on a change and the FILE on a read", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    opened.length = 0;
    const edit = buildToolCard({
      id: "t8",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one", newStr: "two" },
      live: false,
    });
    edit.querySelector<HTMLElement>(".tool-file-link")?.click();
    expect(opened).toEqual(["gitdiff:src/a.ts"]);

    opened.length = 0;
    const read = buildToolCard({
      id: "t9",
      title: "read_files",
      kind: "read",
      status: "completed",
      input: { path: "src/a.ts" },
      live: false,
    });
    read.querySelector<HTMLElement>(".tool-file-link")?.click();
    expect(opened).toEqual(["file:src/a.ts"]);
  });

  it("a move states from and to, which its claim line cannot carry", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t10",
      title: "smartRelocate",
      kind: "move",
      status: "completed",
      input: { sourcePath: "old/a.ts", destinationPath: "new/a.ts" },
      live: false,
    });
    const row = card.querySelector(".tool-move-row");
    expect(row?.textContent).toContain("old/a.ts");
    expect(row?.textContent).toContain("new/a.ts");
  });
});
