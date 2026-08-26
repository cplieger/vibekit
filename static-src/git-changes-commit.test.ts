// ---------------------------------------------------------------------------
// Tests for git-changes-commit.ts's recent-commits section: each commit hash
// is a link to its page on the forge when the server derived a location, and
// the plain text it has always been when it did not. api-client is mocked so
// the /api/git/log payload is the fixture.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { GitRepoStatus } from "./git-types.js";

const mockApiGet = vi.fn();
vi.mock("./api-client.js", () => ({
  apiGet: (...args: unknown[]) => mockApiGet(...args),
  // Present-but-inert so real-ESM linking succeeds; no case here calls them.
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(),
}));

const { renderRecentCommits } = await import("./git-changes-commit.js");

/** A repo status carrying only what renderRecentCommits reads. */
function repoStatus(): GitRepoStatus {
  return {
    repo: "vibekit",
    is_repo: true,
    branch: "main",
    remote: "origin",
    ahead: 0,
    behind: 0,
    files: [],
    has_dirty: false,
    stashes: 0,
  };
}

/** CommitDeps with the commit-area collaborators stubbed; the recent-commits
 *  section reads only `diffAbort`. */
function deps(): Parameters<typeof renderRecentCommits>[1] {
  return {
    commitMessages: new Map<string, string>(),
    bindingCleanups: [],
    diffAbort: null,
    // The recent-commits section reads neither, so a stub that throws says so
    // and turns a future coupling into a loud failure instead of a silent pass.
    refreshChanges: () => {
      throw new Error("refreshChanges: not reached by the recent-commits section");
    },
    assertOk: () => {
      throw new Error("assertOk: not reached by the recent-commits section");
    },
  };
}

/** Expand the section (which is what triggers the fetch) and flush the
 *  apiGet().then(render) microtask chain. */
async function expand(section: HTMLElement): Promise<void> {
  (section as HTMLDetailsElement).open = true;
  section.dispatchEvent(new Event("toggle"));
  await Promise.resolve();
  await Promise.resolve();
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("renderRecentCommits commit hash", () => {
  it("links each hash to the forge commit page the server derived", async () => {
    mockApiGet.mockResolvedValue({
      entries: ["a1b2c3d first subject", "e4f5a6b second subject"],
      remote: "https://github.com/cplieger/vibekit.git",
      behind: 0,
      commit_url_prefix: "https://github.com/cplieger/vibekit/commit/",
    });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    // Every row carries its own commit, so the whole list is one assertion
    // rather than an indexed pair.
    const links = [...section.querySelectorAll<HTMLAnchorElement>("a.git-recent-commits-sha-link")];
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      "https://github.com/cplieger/vibekit/commit/a1b2c3d",
      "https://github.com/cplieger/vibekit/commit/e4f5a6b",
    ]);

    const first = section.querySelector<HTMLAnchorElement>("a.git-recent-commits-sha-link");
    // The hash alone is not a usable accessible name, so the label says where
    // the link goes while still containing the visible text (WCAG 2.5.3).
    expect(first?.getAttribute("aria-label")).toBe("Open commit a1b2c3d on github.com");
    expect(first?.textContent).toBe("a1b2c3d");
    // A new tab must not hand the opener a window reference.
    expect(first?.getAttribute("target")).toBe("_blank");
    expect(first?.getAttribute("rel")).toBe("noopener noreferrer");
    // The subject is untouched.
    expect(section.querySelector(".git-recent-commits-subject")?.textContent).toBe("first subject");
  });

  it("keeps the hash as plain text when the server derived no location", async () => {
    mockApiGet.mockResolvedValue({
      entries: ["a1b2c3d first subject"],
      remote: "",
      behind: 0,
      commit_url_prefix: "",
    });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    expect(section.querySelectorAll("a.git-recent-commits-sha-link")).toHaveLength(0);
    expect(section.querySelector(".git-recent-commits-sha")?.textContent).toBe("a1b2c3d");
  });

  it("keeps the hash as plain text when the field is absent", async () => {
    mockApiGet.mockResolvedValue({ entries: ["a1b2c3d first subject"] });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    expect(section.querySelectorAll("a.git-recent-commits-sha-link")).toHaveLength(0);
    expect(section.querySelector(".git-recent-commits-sha")?.textContent).toBe("a1b2c3d");
  });

  // The prefix is built server-side from a repository's own origin remote,
  // which is config vibekit does not control, so the client re-checks the
  // scheme rather than rendering whatever it was handed.
  //
  // The `//host/` spellings are the cases that actually exercise the scheme
  // guard: a non-special scheme with an authority parses to a NON-empty host,
  // so the empty-host fallback does not cover them and dropping isSafeURL puts
  // that href on the page. `javascript:alert(1)//` and `file:///…` have an
  // empty host and are refused twice over.
  it.each([
    "javascript://evil.com/x",
    "data://evil.com/x",
    "vbscript://evil.com/",
    "javascript:alert(1)//",
    "data:text/html,x",
    "file:///etc/passwd",
    "not a url",
  ])("refuses to link a %s prefix", async (prefix) => {
    mockApiGet.mockResolvedValue({
      entries: ["a1b2c3d first subject"],
      commit_url_prefix: prefix,
    });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    expect(section.querySelectorAll("a")).toHaveLength(0);
    expect(section.querySelector(".git-recent-commits-sha")?.textContent).toBe("a1b2c3d");
  });

  it("renders a non-GitHub forge shape verbatim from the server prefix", async () => {
    mockApiGet.mockResolvedValue({
      entries: ["a1b2c3d first subject"],
      commit_url_prefix: "https://gitlab.example.com/grp/sub/bar/-/commit/",
    });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    const link = section.querySelector<HTMLAnchorElement>("a.git-recent-commits-sha-link");
    expect(link?.getAttribute("href")).toBe(
      "https://gitlab.example.com/grp/sub/bar/-/commit/a1b2c3d",
    );
    expect(link?.getAttribute("aria-label")).toBe("Open commit a1b2c3d on gitlab.example.com");
  });
});
