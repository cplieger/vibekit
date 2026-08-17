// @vitest-environment happy-dom
// Tests for the shared git-status store: the per-path lookup the docs page and
// the file browser read, and the index rules behind it.
import { describe, it, expect, beforeEach } from "vitest";

import {
  _setReposForTest,
  statusFor,
  statusForPath,
  statusUnder,
  currentRepos,
  onGitStatusChange,
} from "./git-status-store.js";
import type { GitRepoStatus } from "./git-types.js";
import { onWorkspaceRoot, setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

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
  resetWorkspace();
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

// The absolute-path lookups exist because the file browser holds real filesystem
// paths and does not know which repo one belongs to.
//
// THE FIXTURE SHAPE IS THE POINT. /api/git/status-all reports each repo by a bare
// directory NAME under the workspace ("." when the workspace root is itself a
// repo), never by an absolute path — see discoverRepos in internal/git/repos.go.
// These cases used to fabricate absolute names (`repo("/w/r", …)`), which made the
// index keys look absolute and the assertions pass while the real product built
// keys like "vibekit/static-src/a.ts" and looked them up with
// "/workspace/vibekit/static-src/a.ts". No key ever matched, so the browser's
// status letters and every directory rollup were silently empty for every file,
// with a green suite. The join now goes through workspace.ts.
describe("statusForPath", () => {
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
  });

  it("answers for an absolute path, so a consumer needs no repo split rule", () => {
    expect.assertions(1);
    _setReposForTest([repo("vibekit", [{ path: "static-src/files.ts", status: "M" }])]);
    expect(statusForPath("/workspace/vibekit/static-src/files.ts")).toBe("M");
  });

  it("answers for a file in the workspace-root repo, reported as '.'", () => {
    // The shape behind the original report: a file written straight into the
    // workspace root, whose repo name is "." rather than a directory.
    expect.assertions(1);
    _setReposForTest([repo(".", [{ path: "hello.sh", status: "?" }])]);
    expect(statusForPath("/workspace/hello.sh")).toBe("?");
  });

  it("does not key a '.' repo's files under a literal './' prefix", () => {
    expect.assertions(1);
    _setReposForTest([repo(".", [{ path: "hello.sh", status: "?" }])]);
    expect(statusForPath("./hello.sh")).toBe("");
  });

  it("returns empty for a clean path and for a directory (that is statusUnder's job)", () => {
    expect.assertions(2);
    _setReposForTest([repo("r", [{ path: "a/b.md", status: "M" }])]);
    expect(statusForPath("/workspace/r/a/other.md")).toBe("");
    expect(statusForPath("/workspace/r/a")).toBe("");
  });

  it("tolerates a trailing slash", () => {
    expect.assertions(1);
    _setReposForTest([repo("r", [{ path: "a.md", status: "A" }])]);
    expect(statusForPath("/workspace/r/a.md/")).toBe("A");
  });

  it("returns empty before the handshake states the workspace root", () => {
    // Nothing can be keyed absolutely yet, so the honest answer is no letter
    // rather than a letter derived from a guessed root.
    expect.assertions(1);
    resetWorkspace();
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    expect(statusForPath("/workspace/r/a.md")).toBe("");
  });

  it("builds no absolute key at all before the handshake, not a wrong one", () => {
    // The distinction matters for the "." repo: joining a file onto an unknown
    // root yields "/hello.sh", which LOOKS absolute and would answer for a real
    // path at the filesystem root. Declining to index is what makes the previous
    // case a property rather than an accident of keys that happen not to collide.
    expect.assertions(2);
    resetWorkspace();
    _setReposForTest([repo(".", [{ path: "hello.sh", status: "M" }])]);
    expect(statusForPath("/hello.sh")).toBe("");
    expect(statusUnder("/")).toBe("");
  });
});

// The handshake and the first poll race with no ordering between them: pollAction
// fires its first tick synchronously when the store starts, while the root arrives
// on an SSE frame. A poll that won that race left the absolute indexes unbuildable
// and the browser's letters blank until the next poll 15s later.
describe("the root landing after a poll", () => {
  it("rebuilds the absolute index against the root that just arrived", () => {
    expect.assertions(2);
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    expect(statusForPath("/workspace/r/a.md")).toBe("");
    setWorkspaceRoot("/workspace");
    expect(statusForPath("/workspace/r/a.md")).toBe("M");
  });

  it("rebuilds the directory rollup too", () => {
    expect.assertions(2);
    _setReposForTest([repo("r", [{ path: "a/b.md", status: "M" }])]);
    expect(statusUnder("/workspace/r/a")).toBe("");
    setWorkspaceRoot("/workspace");
    expect(statusUnder("/workspace/r/a")).toBe("M");
  });

  it("republishes so rows painted with no letter get repainted", () => {
    // The index changed while the data did not, and `repos` is the only thing
    // consumers watch — without the republish the rows already on screen would
    // keep their blank decoration until the next poll.
    expect.assertions(1);
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    let repaints = 0;
    const off = onGitStatusChange(() => {
      repaints++;
    });
    // onGitStatusChange fires immediately with the current value.
    repaints = 0;
    setWorkspaceRoot("/workspace");
    off();
    expect(repaints).toBe(1);
  });

  it("does not wake consumers on a reconnect that restates the same root", () => {
    expect.assertions(1);
    setWorkspaceRoot("/workspace");
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    let repaints = 0;
    const off = onGitStatusChange(() => {
      repaints++;
    });
    repaints = 0;
    setWorkspaceRoot("/workspace");
    off();
    expect(repaints).toBe(0);
  });

  it("keeps the store subscribed to the root for the module's life", () => {
    // The subscription is module wiring, so the test seam must not be able to
    // switch it off — resetWorkspace() resets the root and nothing else.
    expect.assertions(1);
    let subscribers = 0;
    const off = onWorkspaceRoot(() => {
      subscribers++;
    });
    resetWorkspace();
    setWorkspaceRoot("/workspace");
    off();
    expect(subscribers).toBe(1);
  });
});

describe("statusUnder", () => {
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
  });

  it("rolls a nested change up to every ancestor, including the repo root", () => {
    expect.assertions(3);
    _setReposForTest([repo("r", [{ path: "a/b/c.md", status: "M" }])]);
    expect(statusUnder("/workspace/r/a/b")).toBe("M");
    expect(statusUnder("/workspace/r/a")).toBe("M");
    expect(statusUnder("/workspace/r")).toBe("M");
  });

  it("stops at the repo root — a sibling repo's parent is not decorated", () => {
    expect.assertions(1);
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    expect(statusUnder("/workspace")).toBe("");
  });

  it("rolls up to the workspace root for the '.' repo, which IS that root", () => {
    expect.assertions(2);
    _setReposForTest([repo(".", [{ path: "a/b.md", status: "M" }])]);
    expect(statusUnder("/workspace/a")).toBe("M");
    expect(statusUnder("/workspace")).toBe("M");
  });

  it("reports the WORST letter beneath it, so a conflict outranks an untracked file", () => {
    expect.assertions(1);
    _setReposForTest([
      repo("r", [
        { path: "a/untracked.md", status: "?" },
        { path: "a/conflict.md", status: "U" },
        { path: "a/mod.md", status: "M" },
      ]),
    ]);
    expect(statusUnder("/workspace/r/a")).toBe("U");
  });

  it("orders the whole precedence chain U > D > M > R > A > ?", () => {
    expect.assertions(5);
    const worst = (letters: string[]): string => {
      _setReposForTest([
        repo(
          "r",
          letters.map((s, i) => ({ path: `a/f${String(i)}.md`, status: s })),
        ),
      ]);
      return statusUnder("/workspace/r/a");
    };
    expect(worst(["?", "A"])).toBe("A");
    expect(worst(["A", "R"])).toBe("R");
    expect(worst(["R", "M"])).toBe("M");
    expect(worst(["M", "D"])).toBe("D");
    expect(worst(["D", "U"])).toBe("U");
  });

  it("does not let an unrecognised letter win by accident", () => {
    expect.assertions(1);
    _setReposForTest([
      repo("r", [
        { path: "a/x.md", status: "Z" },
        { path: "a/y.md", status: "?" },
      ]),
    ]);
    expect(statusUnder("/workspace/r/a")).toBe("?");
  });

  it("returns empty for a directory with nothing changed beneath it", () => {
    expect.assertions(1);
    _setReposForTest([repo("r", [{ path: "a/b.md", status: "M" }])]);
    expect(statusUnder("/workspace/r/other")).toBe("");
  });

  it("clears the rollup when the tree goes clean", () => {
    expect.assertions(2);
    _setReposForTest([repo("r", [{ path: "a/b.md", status: "M" }])]);
    expect(statusUnder("/workspace/r/a")).toBe("M");
    _setReposForTest([repo("r", [])]);
    expect(statusUnder("/workspace/r/a")).toBe("");
  });
});

describe("currentRepos", () => {
  it("exposes the repos array for aggregate consumers (the badge)", () => {
    _setReposForTest([repo("a", [{ path: "x", status: "M" }]), repo("b", [])]);
    expect(currentRepos().map((r) => r.repo)).toEqual(["a", "b"]);
  });
});
