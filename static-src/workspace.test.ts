import { describe, it, expect, beforeEach } from "vitest";
import {
  absPath,
  relToWorkspace,
  setWorkspaceRoot,
  workspaceRoot,
  _resetForTest,
} from "./workspace.js";

describe("workspace root", () => {
  beforeEach(() => {
    _resetForTest();
  });

  it("is empty until the handshake states it", () => {
    expect.assertions(2);
    expect(workspaceRoot()).toBe("");
    setWorkspaceRoot("/workspace");
    expect(workspaceRoot()).toBe("/workspace");
  });
});

describe("absPath", () => {
  beforeEach(() => {
    _resetForTest();
    setWorkspaceRoot("/workspace");
  });

  // The bug this closes: the turn footer, a tool card and an approval row all
  // carry the agent's workspace-relative path, and sending it to
  // GET /api/file?path=… was denied 403 "outside granted roots" because that
  // handler resolves its path against an absolute allow-list.
  it.each([
    { desc: "joins a relative path onto the root", in: "hello.sh", want: "/workspace/hello.sh" },
    { desc: "joins a nested relative path", in: "a/b/c.go", want: "/workspace/a/b/c.go" },
    { desc: "leaves an absolute path alone", in: "/config/x", want: "/config/x" },
    {
      desc: "leaves a path already under the root alone",
      in: "/workspace/x",
      want: "/workspace/x",
    },
    { desc: "leaves the empty string alone", in: "", want: "" },
  ])("$desc", ({ in: input, want }) => {
    expect.assertions(1);
    expect(absPath(input)).toBe(want);
  });

  // Returning the input unchanged is deliberate: the request then fails exactly
  // as it did before rather than being rewritten into a path that names
  // something else.
  it("returns the input unchanged before the handshake lands", () => {
    expect.assertions(1);
    _resetForTest();
    expect(absPath("hello.sh")).toBe("hello.sh");
  });
});

describe("relToWorkspace", () => {
  beforeEach(() => {
    _resetForTest();
    setWorkspaceRoot("/workspace");
  });

  it.each([
    { desc: "strips the root", in: "/workspace/hello.sh", want: "hello.sh" },
    { desc: "strips the root from a nested path", in: "/workspace/a/b.go", want: "a/b.go" },
    // A sibling mount is addressable, not mangled — the file browser spans
    // several granted roots, so /config must survive intact.
    { desc: "leaves another mount alone", in: "/config/mcp.json", want: "/config/mcp.json" },
    // The separator test is what stops a lookalike sibling reporting as "-old/x".
    {
      desc: "leaves a root-prefixed sibling alone",
      in: "/workspace-old/x",
      want: "/workspace-old/x",
    },
    { desc: "leaves the root itself alone", in: "/workspace", want: "/workspace" },
    { desc: "leaves an already-relative path alone", in: "hello.sh", want: "hello.sh" },
  ])("$desc", ({ in: input, want }) => {
    expect.assertions(1);
    expect(relToWorkspace(input)).toBe(want);
  });

  it("returns the input unchanged before the handshake lands", () => {
    expect.assertions(1);
    _resetForTest();
    expect(relToWorkspace("/workspace/hello.sh")).toBe("/workspace/hello.sh");
  });

  it("round-trips with absPath for a relative path", () => {
    expect.assertions(1);
    expect(relToWorkspace(absPath("a/b.go"))).toBe("a/b.go");
  });
});
