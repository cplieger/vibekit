// Unit tests for tool-schema.ts — pure functions, no DOM dependency.
import { describe, it, expect } from "vitest";
import fc from "fast-check";
import {
  mcpToolInfo,
  profileFor,
  toolDepth1,
  hasDepth1,
  readSubject,
  formatMCPToolName,
  renderInfoFor,
} from "./tool-schema.js";
import type { ToolKind, ToolDepth1 as Depth1 } from "./tool-schema.js";

// ---------------------------------------------------------------------------
// mcpToolInfo — table-driven
// ---------------------------------------------------------------------------

describe("mcpToolInfo", () => {
  const valid: { input: string; expected: { server: string; tool: string } }[] = [
    { input: "mcp__github__create_issue", expected: { server: "github", tool: "create_issue" } },
    { input: "mcp__s3__list_buckets", expected: { server: "s3", tool: "list_buckets" } },
    { input: "mcp__a__b", expected: { server: "a", tool: "b" } },
    { input: "mcp__Server1__tool.name", expected: { server: "Server1", tool: "tool.name" } },
    { input: "mcp__my-server__my-tool", expected: { server: "my-server", tool: "my-tool" } },
    { input: "mcp:github:create_issue", expected: { server: "github", tool: "create_issue" } },
    { input: "mcp:s3:list_buckets", expected: { server: "s3", tool: "list_buckets" } },
    { input: "mcp:a:b", expected: { server: "a", tool: "b" } },
    { input: "mcp:Server1:tool.name", expected: { server: "Server1", tool: "tool.name" } },
    { input: "mcp:my-server:my-tool", expected: { server: "my-server", tool: "my-tool" } },
  ];

  it.each(valid)("parses $input", ({ input, expected }) => {
    expect(mcpToolInfo(input)).toEqual(expected);
  });

  const invalid = [
    "",
    "mcp_github_tool", // single underscore
    "mcp___github__tool", // triple underscore prefix
    "mcp__", // missing segments
    "mcp____", // empty segments
    "mcp::github:tool", // double colon
    "mcp::", // empty colon segments
    "notmcp__a__b", // wrong prefix
    "MCP__a__b", // case-sensitive prefix
    "mcp__ __tool", // space in server
    "mcp:server:", // empty tool
    "mcp::tool", // empty server
    "readFile", // normal tool name
    "mcp__-invalid__tool", // leading dash in server
    "mcp__.invalid__tool", // leading dot in server
    "mcp__server__-tool", // leading dash in tool
    "mcp__server__.tool", // leading dot in tool
  ];

  it.each(invalid)("returns null for %j", (input) => {
    expect(mcpToolInfo(input)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// mcpToolInfo — property-based fuzz
// ---------------------------------------------------------------------------

describe("mcpToolInfo (property-based)", () => {
  // Arbitrary for valid server/tool name segments: starts with alnum, rest is alnum/underscore/dot/dash.
  // Exclude double-underscore sequences since those are the separator in the mcp__ format.
  const nameSegment = fc
    .stringMatching(/^[A-Za-z0-9][A-Za-z0-9_.-]{0,20}$/)
    .filter((s) => !s.includes("__"));

  it("round-trips underscore variant: mcp__<server>__<tool>", () => {
    fc.assert(
      fc.property(nameSegment, nameSegment, (server, tool) => {
        const title = `mcp__${server}__${tool}`;
        const result = mcpToolInfo(title);
        expect(result).toEqual({ server, tool });
      }),
      { numRuns: 200 },
    );
  });

  it("round-trips colon variant: mcp:<server>:<tool>", () => {
    // Colon variant: segments must not contain colons (they don't — regex char class excludes them)
    fc.assert(
      fc.property(nameSegment, nameSegment, (server, tool) => {
        const title = `mcp:${server}:${tool}`;
        const result = mcpToolInfo(title);
        expect(result).toEqual({ server, tool });
      }),
      { numRuns: 200 },
    );
  });

  it("returns null for strings not starting with mcp__ or mcp:", () => {
    const nonMcp = fc
      .string({ minLength: 0, maxLength: 100 })
      .filter((s) => !s.startsWith("mcp__") && !s.startsWith("mcp:"));
    fc.assert(
      fc.property(nonMcp, (s) => {
        expect(mcpToolInfo(s)).toBeNull();
      }),
      { numRuns: 200 },
    );
  });

  it("result is never partially populated", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 100 }), (s) => {
        const result = mcpToolInfo(s);
        if (result !== null) {
          expect(result.server.length).toBeGreaterThan(0);
          expect(result.tool.length).toBeGreaterThan(0);
        } else {
          expect(result).toBeNull();
        }
      }),
      { numRuns: 500 },
    );
  });
});

// ---------------------------------------------------------------------------
// profileFor — table-driven
// ---------------------------------------------------------------------------

describe("profileFor", () => {
  // Title-based lookups
  const titleCases: {
    title: string;
    kind: string;
    expectedKind: ToolKind;
    expectedWrites: boolean;
  }[] = [
    { title: "readFile", kind: "read", expectedKind: "read", expectedWrites: false },
    { title: "readCode", kind: "read", expectedKind: "read", expectedWrites: false },
    { title: "readMultipleFiles", kind: "read", expectedKind: "read", expectedWrites: false },
    { title: "listDirectory", kind: "read", expectedKind: "read", expectedWrites: false },
    { title: "fsWrite", kind: "write", expectedKind: "write", expectedWrites: true },
    { title: "fsAppend", kind: "write", expectedKind: "write", expectedWrites: true },
    { title: "strReplace", kind: "edit", expectedKind: "edit", expectedWrites: true },
    { title: "FileEdit", kind: "edit", expectedKind: "edit", expectedWrites: true },
    { title: "FileWrite", kind: "write", expectedKind: "write", expectedWrites: true },
    { title: "deleteFile", kind: "delete", expectedKind: "delete", expectedWrites: false },
    { title: "smartRelocate", kind: "move", expectedKind: "move", expectedWrites: false },
    { title: "semanticRename", kind: "edit", expectedKind: "edit", expectedWrites: false },
    { title: "fileSearch", kind: "search", expectedKind: "search", expectedWrites: false },
    { title: "grepSearch", kind: "search", expectedKind: "search", expectedWrites: false },
    { title: "executePwsh", kind: "execute", expectedKind: "execute", expectedWrites: false },
    { title: "webFetch", kind: "fetch", expectedKind: "fetch", expectedWrites: false },
    { title: "remote_web_search", kind: "fetch", expectedKind: "fetch", expectedWrites: false },
  ];

  it.each(titleCases)(
    "title=$title → kind=$expectedKind, writesFile=$expectedWrites",
    ({ title, kind, expectedKind, expectedWrites }) => {
      const p = profileFor(title, kind);
      expect(p.kind).toBe(expectedKind);
      expect(p.writesFile).toBe(expectedWrites);
    },
  );

  // Kind fallback (unknown title)
  const kindFallbackCases: {
    kind: string;
    expectedKind: ToolKind;
    expectedWrites: boolean;
  }[] = [
    { kind: "read", expectedKind: "read", expectedWrites: false },
    { kind: "edit", expectedKind: "edit", expectedWrites: true },
    { kind: "write", expectedKind: "write", expectedWrites: true },
    { kind: "delete", expectedKind: "delete", expectedWrites: false },
    { kind: "move", expectedKind: "move", expectedWrites: false },
    { kind: "search", expectedKind: "search", expectedWrites: false },
    { kind: "execute", expectedKind: "execute", expectedWrites: false },
    { kind: "fetch", expectedKind: "fetch", expectedWrites: false },
    { kind: "think", expectedKind: "think", expectedWrites: false },
    { kind: "switch_mode", expectedKind: "switch_mode", expectedWrites: false },
  ];

  it.each(kindFallbackCases)(
    "unknown title + kind=$kind → $expectedKind",
    ({ kind, expectedKind, expectedWrites }) => {
      const p = profileFor("unknownTool", kind);
      expect(p.kind).toBe(expectedKind);
      expect(p.writesFile).toBe(expectedWrites);
    },
  );

  it("unknown title + unknown kind → other", () => {
    const p = profileFor("unknownTool", "unknownKind");
    expect(p.kind).toBe("other");
    expect(p.writesFile).toBe(false);
  });

  it("MCP-prefixed title → mcp kind", () => {
    const p = profileFor("mcp__github__create_issue", "execute");
    expect(p.kind).toBe("mcp");
    expect(p.writesFile).toBe(false);
  });

  it("MCP colon-prefixed title → mcp kind", () => {
    const p = profileFor("mcp:github:create_issue", "execute");
    expect(p.kind).toBe("mcp");
    expect(p.writesFile).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// toolDepth1 / hasDepth1 — what a card reveals, per kind
// ---------------------------------------------------------------------------

describe("toolDepth1", () => {
  const cases: { kind: ToolKind; expected: Depth1 }[] = [
    // Claim-only, and each for a stated reason: reads arrive in floods (p99 75
    // per turn), and delete_file takes ONE path so a list would restate the claim.
    { kind: "read", expected: "none" },
    { kind: "delete", expected: "none" },
    { kind: "hook", expected: "none" },
    { kind: "think", expected: "none" },
    { kind: "switch_mode", expected: "none" },
    // The two that carry real information.
    { kind: "edit", expected: "diff" },
    { kind: "write", expected: "diff" },
    { kind: "execute", expected: "output" },
    { kind: "shell", expected: "output" },
    { kind: "command", expected: "output" },
    { kind: "move", expected: "move" },
    { kind: "search", expected: "search" },
    { kind: "fetch", expected: "fetch" },
    { kind: "mcp", expected: "mcp" },
    { kind: "browser", expected: "generic" },
    { kind: "other", expected: "generic" },
  ];

  it.each(cases)("$kind → $expected", ({ kind, expected }) => {
    expect(toolDepth1(kind)).toBe(expected);
  });

  it("hasDepth1 is false exactly for the claim-only kinds", () => {
    for (const { kind, expected } of cases) {
      expect(hasDepth1(kind)).toBe(expected !== "none");
    }
  });

  it("an edit CAN be expanded, which the old tier axis prevented", () => {
    // The tier this replaced gave `simple` (which included edit) no toggle at
    // all, so the diff had to live in the resting state to be visible.
    expect(hasDepth1("edit")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// readSubject — a read claim must be true for the whole family
// ---------------------------------------------------------------------------

describe("readSubject", () => {
  // `kind: "read"` covers seven action types and four of them read something
  // other than files, so "Read 3 files" is false for those four.
  it.each([
    ["read_files", "files"],
    ["read_file", "files"],
    ["read_code", "files"],
    ["list_processes", "processes"],
    ["get_process_output", "processes"],
    ["open_folders", "folders"],
    ["get_diagnostics", "diagnostics"],
  ])("%s reads %s", (name, subject) => {
    expect(readSubject(name)).toBe(subject);
  });

  it("resolves the legacy camelCase aliases persisted sessions carry", () => {
    expect(readSubject("listProcesses")).toBe("processes");
    expect(readSubject("getProcessOutput")).toBe("processes");
    expect(readSubject("openFolders")).toBe("folders");
    expect(readSubject("getDiagnostics")).toBe("diagnostics");
  });

  it("falls back to files for an unknown reader", () => {
    expect(readSubject("someNewReadTool")).toBe("files");
  });
});

// ---------------------------------------------------------------------------
// formatMCPToolName
// ---------------------------------------------------------------------------

describe("formatMCPToolName", () => {
  const cases: { input: string; expected: string }[] = [
    { input: "create_issue", expected: "create issue" },
    { input: "list_buckets", expected: "list buckets" },
    { input: "tool", expected: "tool" },
    { input: "", expected: "" },
    { input: "a_b_c", expected: "a b c" },
    { input: "_leading", expected: " leading" },
    { input: "trailing_", expected: "trailing " },
  ];

  it.each(cases)("$input → $expected", ({ input, expected }) => {
    expect(formatMCPToolName(input)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// renderInfoFor — integration-level table-driven
// ---------------------------------------------------------------------------

describe("renderInfoFor", () => {
  it("file write with strReplace input", () => {
    const info = renderInfoFor("strReplace", "edit", {
      path: "src/main.ts",
      oldStr: "a",
      newStr: "b",
    });
    expect(info.kind).toBe("edit");
    expect(info.writesFile).toBe(true);
    expect(info.filePath).toBe("src/main.ts");
    expect(info.fileBasename).toBe("main.ts");
    expect(info.diffSources).toEqual({ oldText: "a", newText: "b" });
    expect(info.mcp).toBeNull();
  });

  it("fsWrite with text input", () => {
    const info = renderInfoFor("fsWrite", "write", { path: "out.txt", text: "hello" });
    expect(info.kind).toBe("write");
    expect(info.writesFile).toBe(true);
    expect(info.diffSources).toEqual({ oldText: "", newText: "hello" });
  });

  it("read tool — no diff, no mcp", () => {
    const info = renderInfoFor("readFile", "read", { path: "foo.ts" });
    expect(info.kind).toBe("read");
    expect(info.writesFile).toBe(false);
    expect(info.diffSources).toBeNull();
    expect(info.mcp).toBeNull();
  });

  it("MCP tool — extracts server/tool", () => {
    const info = renderInfoFor("mcp__github__create_issue", "execute", {});
    expect(info.kind).toBe("mcp");
    expect(info.mcp).toEqual({ server: "github", tool: "create_issue" });
  });

  it("unknown tool with no input", () => {
    const info = renderInfoFor("mystery", "unknown", undefined);
    expect(info.kind).toBe("other");
    expect(info.writesFile).toBe(false);
    expect(info.filePath).toBe("");
    expect(info.fileBasename).toBe("");
    expect(info.diffSources).toBeNull();
    expect(info.mcp).toBeNull();
  });

  it("picks targetFile when path is absent", () => {
    const info = renderInfoFor("fsWrite", "write", { targetFile: "dest.ts", text: "x" });
    expect(info.filePath).toBe("dest.ts");
    expect(info.fileBasename).toBe("dest.ts");
  });
});
