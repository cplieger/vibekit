// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";

// Mock scroll.js to avoid eager DOM element lookups at module load time.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

import {
  formatDuration,
  summarizeSameKind,
  summarizeMCP,
  labelWithSamples,
  kindNoun,
  summarizeMixed,
  summarize,
  type CallInfo,
} from "./tool-group.js";
import type { ToolKind } from "./types.js";

// --- formatDuration ---

describe("formatDuration", () => {
  const cases: [number, string][] = [
    [500, "0.5s"],
    [1000, "1.0s"],
    [2500, "2.5s"],
    [59999, "60.0s"],
    [60000, "1m0s"],
    [90000, "1m30s"],
    [125000, "2m5s"],
  ];
  it.each(cases)("formatDuration(%i) => %s", (ms, expected) => {
    expect(formatDuration(ms)).toBe(expected);
  });
});

// --- labelWithSamples ---

describe("labelWithSamples", () => {
  const cases: [number, string, string, string[], string][] = [
    [1, "file", "Read", [], "Read 1 file"],
    [3, "file", "Read", ["a.ts", "b.ts", "c.ts"], "Read 3 files: a.ts, b.ts, +1 more"],
    [2, "file", "Edited", ["x.go", "y.go"], "Edited 2 files: x.go, y.go"],
    [1, "command", "Ran", ["ls -la"], "Ran 1 command: ls -la"],
    [
      5,
      "search",
      "Searched",
      ["foo", "bar", "baz", "qux", "quux"],
      "Searched 5 searchs: foo, bar, +3 more",
    ],
    [2, "file", "Read", ["same.ts", "same.ts"], "Read 2 files: same.ts"],
  ];
  it.each(cases)("labelWithSamples(%i, %s, %s, %j) => %s", (n, noun, verb, samples, expected) => {
    expect(labelWithSamples(n, noun, verb, samples)).toBe(expected);
  });
});

// --- kindNoun ---

describe("kindNoun", () => {
  const cases: [string, number, string][] = [
    ["read", 1, "read"],
    ["read", 2, "reads"],
    ["edit", 1, "edit"],
    ["edit", 3, "edits"],
    ["execute", 1, "command"],
    ["execute", 2, "commands"],
    ["fetch", 1, "fetch"],
    ["fetch", 2, "fetches"],
    ["think", 1, "thinking step"],
    ["think", 2, "thinking steps"],
    ["switch_mode", 1, "mode switch"],
    ["switch_mode", 2, "mode switches"],
    ["mcp", 1, "integration call"],
    ["mcp", 2, "integration calls"],
    ["unknown_kind", 1, "call"],
    ["unknown_kind", 5, "calls"],
  ];
  it.each(cases)("kindNoun(%s, %i) => %s", (kind, count, expected) => {
    expect(kindNoun(kind, count)).toBe(expected);
  });
});

// --- summarizeSameKind ---

describe("summarizeSameKind", () => {
  function makeInfos(kind: ToolKind, filenames: string[], titles: string[]): CallInfo[] {
    return filenames.map((f, i) => ({
      kind,
      filename: f,
      title: titles[i] ?? "",
      mcpServer: "",
    }));
  }

  it("read with files", () => {
    const infos = makeInfos("read", ["a.ts", "b.ts"], ["", ""]);
    expect(summarizeSameKind("read", infos)).toBe("Read 2 files: a.ts, b.ts");
  });

  it("execute with titles", () => {
    const infos: CallInfo[] = [
      { kind: "execute", filename: "", title: "npm test", mcpServer: "" },
      { kind: "execute", filename: "", title: "npm build", mcpServer: "" },
      { kind: "execute", filename: "", title: "npm lint", mcpServer: "" },
    ];
    expect(summarizeSameKind("execute", infos)).toBe(
      "Ran 3 commands: npm test, npm build, +1 more",
    );
  });

  it("think single", () => {
    const infos: CallInfo[] = [{ kind: "think", filename: "", title: "", mcpServer: "" }];
    expect(summarizeSameKind("think", infos)).toBe("1 thinking step");
  });

  it("think plural", () => {
    const infos: CallInfo[] = [
      { kind: "think", filename: "", title: "", mcpServer: "" },
      { kind: "think", filename: "", title: "", mcpServer: "" },
    ];
    expect(summarizeSameKind("think", infos)).toBe("2 thinking steps");
  });

  it("switch_mode plural", () => {
    const infos: CallInfo[] = [
      { kind: "switch_mode", filename: "", title: "", mcpServer: "" },
      { kind: "switch_mode", filename: "", title: "", mcpServer: "" },
      { kind: "switch_mode", filename: "", title: "", mcpServer: "" },
    ];
    expect(summarizeSameKind("switch_mode", infos)).toBe("3 mode switches");
  });

  it("unknown kind falls back to generic", () => {
    // Cast to ToolKind to exercise the unknown-kind fallback path. The
    // production code defends against TOOL_KIND_LABELS[kind] returning
    // undefined; this test pins that fallback behaviour.
    const customKind = "custom" as ToolKind;
    const infos: CallInfo[] = [
      { kind: customKind, filename: "", title: "do thing", mcpServer: "" },
    ];
    expect(summarizeSameKind(customKind, infos)).toBe("Ran 1 call: do thing");
  });
});

// --- summarizeMCP ---

describe("summarizeMCP", () => {
  it("single server", () => {
    const infos: CallInfo[] = [
      { kind: "mcp", filename: "", title: "create_issue", mcpServer: "GitHub" },
      { kind: "mcp", filename: "", title: "search_repos", mcpServer: "GitHub" },
    ];
    expect(summarizeMCP(infos)).toBe("Called 2 GitHub tools: create_issue, search_repos");
  });

  it("single server with many tools", () => {
    const infos: CallInfo[] = [
      { kind: "mcp", filename: "", title: "a", mcpServer: "Linear" },
      { kind: "mcp", filename: "", title: "b", mcpServer: "Linear" },
      { kind: "mcp", filename: "", title: "c", mcpServer: "Linear" },
    ];
    expect(summarizeMCP(infos)).toBe("Called 3 Linear tools: a, b, +1 more");
  });

  it("multi server", () => {
    const infos: CallInfo[] = [
      { kind: "mcp", filename: "", title: "x", mcpServer: "GitHub" },
      { kind: "mcp", filename: "", title: "y", mcpServer: "GitHub" },
      { kind: "mcp", filename: "", title: "z", mcpServer: "Linear" },
    ];
    expect(summarizeMCP(infos)).toBe("3 integration calls: 2 GitHub, 1 Linear");
  });
});

// --- summarizeMixed ---

describe("summarizeMixed", () => {
  it("mixed kinds", () => {
    const infos: CallInfo[] = [
      { kind: "read", filename: "a.ts", title: "", mcpServer: "" },
      { kind: "read", filename: "b.ts", title: "", mcpServer: "" },
      { kind: "edit", filename: "c.ts", title: "", mcpServer: "" },
      { kind: "execute", filename: "", title: "npm test", mcpServer: "" },
    ];
    expect(summarizeMixed(infos)).toBe("4 operations: 2 reads, 1 edit, 1 command");
  });
});

// --- summarize (grouping logic edge cases) ---

describe("summarize", () => {
  function makeCall(kind: string, filename = "", title = "", mcpServer = ""): HTMLElement {
    const el = document.createElement("div");
    el.className = "tool-call";
    el.dataset["kind"] = kind;
    el.dataset["filename"] = filename;
    el.dataset["title"] = title;
    el.dataset["mcpServer"] = mcpServer;
    return el;
  }

  const cases: [string, HTMLElement[], string][] = [
    ["empty array", [], "0 tool calls"],
    ["single tool call", [makeCall("read", "a.ts")], "Read 1 file: a.ts"],
    [
      "3 consecutive same-kind (read)",
      [makeCall("read", "a.ts"), makeCall("read", "b.ts"), makeCall("read", "c.ts")],
      "Read 3 files: a.ts, b.ts, +1 more",
    ],
    [
      "mixed sequence produces mixed summary",
      [
        makeCall("read", "a.ts"),
        makeCall("read", "b.ts"),
        makeCall("edit", "c.ts"),
        makeCall("read", "d.ts"),
      ],
      "4 operations: 3 reads, 1 edit",
    ],
    ["single execute with title", [makeCall("execute", "", "npm test")], "Ran 1 command: npm test"],
  ];

  it.each(cases)("%s", (_label, calls, expected) => {
    expect(summarize(calls)).toBe(expected);
  });
});
