import { describe, it, expect, vi, beforeEach } from "vitest";

// git-tabs is a deduped signal + subscribe store (mirrors settings-tabs.ts).
// The core contract is pure signal behavior, so no DOM is needed: we exercise
// onGitTabChange / setGitTab / getGitTab directly. A fresh module per test
// resets the activeTab signal to its "changes" default and stops subscriber
// registrations from leaking across tests.
beforeEach(() => {
  vi.resetModules();
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
});

// Local alias matching the module's exported GitTab union, kept here so the
// test arrays stay strongly typed without importing the type at module scope
// (each test imports the module fresh inside its own body).
type GitTabName = "changes" | "prs" | "sources";
