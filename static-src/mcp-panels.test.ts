// @vitest-environment happy-dom
// Unit tests for mcp-panels.ts — pure functions only.
import { describe, it, expect, vi } from "vitest";
import fc from "fast-check";

// Mock DOM-dependent modules that mcp-panels.ts imports at module level.
vi.mock("./dom.js", () => ({
  $: new Proxy({}, { get: () => document.createElement("div") }),
  el: () => document.createElement("div"),
}));
vi.mock("./api-client.js", () => ({
  apiGet: async () => null,
}));
vi.mock("./modals.js", () => ({
  closeModal: () => {
    /* noop */
  },
}));
vi.mock("./mcp-state.js", () => ({
  mcpState: {
    refetchServers: async () => {
      /* noop */
    },
  },
  configured: [],
  SECRET_MASK: "***",
}));
vi.mock("./mcp-pairs.js", () => ({
  renderKeyPairList: () => {
    /* noop */
  },
  appendKeyPair: () => {
    /* noop */
  },
  collectKeyPairs: () => [],
}));
vi.mock(import("./icons.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual };
});
vi.mock("./actions/mcp.js", () => ({
  saveServer: { dispatch: async () => ({}) },
  importServers: { dispatch: async () => ({}) },
  searchRegistry: { cancel: () => undefined },
}));

import { simplifyName } from "./mcp-panels-search.js";
import { extractNpxPackage } from "./mcp-panels.js";
import type { Server } from "./mcp-state.js";

// ---------------------------------------------------------------------------
// simplifyName — table-driven
// ---------------------------------------------------------------------------

describe("simplifyName", () => {
  const cases: { input: string; expected: string }[] = [
    { input: "@modelcontextprotocol/server-github", expected: "server-github" },
    { input: "my-server", expected: "my-server" },
    { input: "simple", expected: "simple" },
    { input: "@scope/pkg-name", expected: "pkg-name" },
    { input: "has spaces and !chars", expected: "has-spaces-and--chars" },
    { input: "---leading-trailing---", expected: "leading-trailing" },
    { input: "", expected: "server" },
    { input: "!!!@@@", expected: "server" },
    { input: "a".repeat(100), expected: "a".repeat(48) },
    { input: "@scope/" + "x".repeat(60), expected: "x".repeat(48) },
  ];

  for (const { input, expected } of cases) {
    it(`simplifyName(${JSON.stringify(input)}) => ${JSON.stringify(expected)}`, () => {
      expect(simplifyName(input)).toBe(expected);
    });
  }
});

// ---------------------------------------------------------------------------
// extractNpxPackage — table-driven
// ---------------------------------------------------------------------------

describe("extractNpxPackage", () => {
  const stub = (args: string[]): Server => ({
    id: "x",
    name: "x",
    transport: "stdio",
    enabled: true,
    args,
    created_at: 0,
    updated_at: 0,
  });

  const cases: { label: string; args: string[]; expected: string }[] = [
    { label: "typical npx args", args: ["-y", "@scope/pkg"], expected: "@scope/pkg" },
    { label: "only package", args: ["my-pkg"], expected: "my-pkg" },
    { label: "--yes flag", args: ["--yes", "pkg"], expected: "pkg" },
    { label: "empty args", args: [], expected: "" },
    { label: "only flags", args: ["-y", "--yes"], expected: "" },
    { label: "whitespace-only arg then package", args: ["  ", "-y", "pkg"], expected: "pkg" },
  ];

  for (const { label, args, expected } of cases) {
    it(label, () => {
      expect(extractNpxPackage(stub(args))).toBe(expected);
    });
  }
});

// ---------------------------------------------------------------------------
// simplifyName — property-based (tarch-c5-p3)
// ---------------------------------------------------------------------------

describe("simplifyName property", () => {
  const VALID_PATTERN = /^[A-Za-z0-9_-]+$/;

  it("always produces a non-empty string matching /^[A-Za-z0-9_-]+$/ with length ≤ 48", () => {
    fc.assert(
      fc.property(fc.string(), (input) => {
        const result = simplifyName(input);
        expect(result.length).toBeGreaterThan(0);
        expect(result.length).toBeLessThanOrEqual(48);
        expect(result).toMatch(VALID_PATTERN);
      }),
      { numRuns: 1000 },
    );
  });

  it("preserves alphanumeric content from the last path segment", () => {
    fc.assert(
      fc.property(
        fc.string({ minLength: 1 }).filter((s) => {
          // Must have alphanumeric content AND not simplify to "server" naturally
          if (!/[A-Za-z0-9]/.test(s)) {
            return false;
          }
          const afterSlash = s.slice(s.lastIndexOf("/") + 1);
          const cleaned = afterSlash
            .replace(/[^A-Za-z0-9_-]/g, "-")
            .replace(/^-+|-+$/g, "")
            .slice(0, 48);
          return cleaned !== "" && cleaned !== "server";
        }),
        (input) => {
          const result = simplifyName(input);
          expect(result).not.toBe("server");
        },
      ),
      { numRuns: 500 },
    );
  });
});
