// ---------------------------------------------------------------------------
// Tests for workspace.ts — the one holder of the workspace root and the one
// conversion between the client's two path spaces.
//
// The bug these close: every path the agent supplies is workspace-RELATIVE
// (translate.relPath strips the prefix so a turn footer reads "hello.sh"), while
// /api/file resolves its `path` against an ABSOLUTE granted-roots allow-list.
// Nothing bridged them, so a click on a changed filename produced
// GET /api/file?path=hello.sh and was denied 403 as outside every root.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach } from "vitest";
import {
  absPath,
  onWorkspaceRoot,
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

  it("notifies subscribers when the root lands", () => {
    // The consumers race the handshake with no ordering between them, so they
    // subscribe rather than betting on arriving second — see git-status-store.
    expect.assertions(2);
    let woke = 0;
    onWorkspaceRoot(() => {
      woke++;
    });
    expect(woke).toBe(0);
    setWorkspaceRoot("/workspace");
    expect(woke).toBe(1);
  });

  it("does not notify on a repeated root", () => {
    // The handshake repeats on every reconnect. A subscriber that rebuilds an
    // index must not be woken by a frame that told it nothing new.
    expect.assertions(1);
    setWorkspaceRoot("/workspace");
    let woke = 0;
    onWorkspaceRoot(() => {
      woke++;
    });
    setWorkspaceRoot("/workspace");
    expect(woke).toBe(0);
  });

  it("stops notifying after unsubscribe", () => {
    expect.assertions(1);
    let woke = 0;
    const off = onWorkspaceRoot(() => {
      woke++;
    });
    off();
    setWorkspaceRoot("/workspace");
    expect(woke).toBe(0);
  });
});

describe("absPath", () => {
  beforeEach(() => {
    _resetForTest();
    setWorkspaceRoot("/workspace");
  });

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
