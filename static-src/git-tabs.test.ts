// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

// git-tabs is a deduped signal + subscribe store (mirrors settings-tabs.ts).
// The core contract is pure signal behavior, but setGitTab now also pushes a
// URL (pushState) and syncs the git tab's route, so the suite runs under
// happy-dom for window.location / history. A fresh module per test resets the
// activeTab signal to its "changes" default and stops subscriber registrations
// from leaking across tests.
beforeEach(() => {
  vi.resetModules();
  // Reset the shared happy-dom location so URL assertions start from a known
  // state (resetModules clears module state, not the global location).
  history.replaceState(null, "", "/");
});

describe("git-tabs store", () => {
  it("onGitTabChange fires immediately with the current tab", async () => {
    const { onGitTabChange } = await import("./git-tabs.js");

    const seen: GitTabName[] = [];
    const dispose = onGitTabChange((tab) => {
      seen.push(tab);
    });

    expect(seen).toEqual(["changes"]);
    dispose();
  });

  it("setGitTab notifies subscribers with the new tab", async () => {
    const { onGitTabChange, setGitTab, getGitTab } = await import("./git-tabs.js");

    const seen: GitTabName[] = [];
    const dispose = onGitTabChange((tab) => {
      seen.push(tab);
    });

    setGitTab("prs");

    expect(seen).toEqual(["changes", "prs"]);
    expect(getGitTab()).toBe("prs");
    dispose();
  });

  it("same-tab setGitTab is a no-op and does not re-notify", async () => {
    const { onGitTabChange, setGitTab, getGitTab } = await import("./git-tabs.js");

    const fn = vi.fn<(tab: GitTabName) => void>();
    const dispose = onGitTabChange(fn);
    fn.mockClear(); // drop the immediate fire so we only count change notifications

    setGitTab("changes"); // already the default → deduped signal → no notify

    expect(fn).not.toHaveBeenCalled();
    expect(getGitTab()).toBe("changes");
    dispose();
  });

  it("setGitTab pushes the matching URL; changes maps to the canonical /git", async () => {
    const { setGitTab } = await import("./git-tabs.js");

    setGitTab("prs");
    expect(location.pathname).toBe("/git/prs");

    setGitTab("sources");
    expect(location.pathname).toBe("/git/sources");

    setGitTab("changes");
    expect(location.pathname).toBe("/git");
  });
});

// Local alias matching the module's exported GitTab union, kept here so the
// test arrays stay strongly typed without importing the type at module scope
// (each test imports the module fresh inside its own body).
type GitTabName = "changes" | "prs" | "sources";
