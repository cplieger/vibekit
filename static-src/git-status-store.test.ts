// @vitest-environment happy-dom
// Tests for the shared git-status store: the per-path lookup the docs page and
// the file browser read, and the index rules behind it.
import { describe, it, expect, beforeEach } from "vitest";

import { _setReposForTest, statusFor, currentRepos } from "./git-status-store.js";
import type { GitRepoStatus } from "./git-types.js";

function repo(
  name: string,
  files: { path: string; status: string; staged?: boolean }[],
): GitRepoStatus {
  return {
    repo: name,
    is_repo: true,
    branch: "main",
    remote: "origin",
    ahead: 0,
    behind: 0,
    has_dirty: files.length > 0,
    stashes: 0,
    files: files.map((f) => ({
      path: f.path,
      status: f.status,
      staged: f.staged ?? false,
      display: f.path,
    })),
  };
}

beforeEach(() => {
  _setReposForTest([]);
});

describe("statusFor", () => {
  it("returns the letter for a known repo-relative path", () => {
    _setReposForTest([repo(".kiro", [{ path: "steering/actions.md", status: "M" }])]);
    expect(statusFor(".kiro", "steering/actions.md")).toBe("M");
  });

  it("returns empty for a clean or unknown path", () => {
    _setReposForTest([repo(".kiro", [{ path: "steering/actions.md", status: "M" }])]);
    expect(statusFor(".kiro", "steering/other.md")).toBe("");
    expect(statusFor("nosuchrepo", "steering/actions.md")).toBe("");
    expect(statusFor("", "")).toBe("");
  });

  it("keys on repo AND path, so the same relative path in two repos is distinct", () => {
    _setReposForTest([
      repo("alpha", [{ path: "README.md", status: "M" }]),
      repo("beta", [{ path: "README.md", status: "A" }]),
    ]);
    expect(statusFor("alpha", "README.md")).toBe("M");
    expect(statusFor("beta", "README.md")).toBe("A");
  });

  it("takes the first letter of a two-character porcelain status", () => {
    _setReposForTest([repo("r", [{ path: "a.md", status: "MM" }])]);
    expect(statusFor("r", "a.md")).toBe("M");
  });

  it("keeps the first entry when a path appears twice (staged + unstaged)", () => {
    _setReposForTest([
      repo("r", [
        { path: "a.md", status: "A", staged: true },
        { path: "a.md", status: "M", staged: false },
      ]),
    ]);
    expect(statusFor("r", "a.md")).toBe("A");
  });

  it("ignores directories that are not repos", () => {
    const notARepo: GitRepoStatus = {
      ...repo("x", [{ path: "a.md", status: "M" }]),
      is_repo: false,
    };
    _setReposForTest([notARepo]);
    expect(statusFor("x", "a.md")).toBe("");
  });

  it("clears stale entries when a poll returns a cleaner tree", () => {
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    expect(statusFor("r", "a.md")).toBe("M");
    // The file was committed: the next poll no longer lists it.
    _setReposForTest([repo("r", [])]);
    expect(statusFor("r", "a.md")).toBe("");
  });
});

describe("currentRepos", () => {
  it("exposes the repos array for aggregate consumers (the badge)", () => {
    _setReposForTest([repo("a", [{ path: "x", status: "M" }]), repo("b", [])]);
    expect(currentRepos().map((r) => r.repo)).toEqual(["a", "b"]);
  });
});
