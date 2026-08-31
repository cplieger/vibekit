import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as ModGitTabs from "./git-tabs.js";

/** Cache-buster for the re-imports below.
 *
 * `vi.resetModules()` does not re-evaluate a module in Browser Mode: the module
 * map is URL-keyed, so a following `await import()` hands back the CACHED
 * instance and every test after the first observes stale module state. Busting
 * the specifier per evaluation is what actually mints a fresh instance. The `.ts`
 * extension is load-bearing — written `.js` the suite still passes while coverage
 * silently attributes every evaluation to a file that does not exist.
 *
 * Only the module under test is busted. Its own dependencies keep their plain
 * specifiers, so `vi.mock` still intercepts them and a shared module the test
 * also imports is the same instance the fresh module got.
 */
let bootSeq = 0;

// git-tabs is a deduped signal + subscribe store (mirrors settings-tabs.ts).
// The core contract is pure signal behavior, but setGitTab now also pushes a
// URL (pushState) and syncs the git tab's route, so the suite runs under
// the real window.location / history. A fresh module per test resets the
// activeTab signal to its "changes" default and stops subscriber registrations
// from leaking across tests.
beforeEach(() => {
  vi.resetModules();
  bootSeq++;
  // Reset the shared location so URL assertions start from a known
  // state (resetModules clears module state, not the global location).
  history.replaceState(null, "", "/");
});

describe("git-tabs store", () => {
  it("onGitTabChange fires immediately with the current tab", async () => {
    const { onGitTabChange } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

    const seen: GitTabName[] = [];
    const dispose = onGitTabChange((tab) => {
      seen.push(tab);
    });

    expect(seen).toEqual(["changes"]);
    dispose();
  });

  it("setGitTab notifies subscribers with the new tab", async () => {
    const { onGitTabChange, setGitTab, getGitTab } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

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
    const { onGitTabChange, setGitTab, getGitTab } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

    const fn = vi.fn<(tab: GitTabName) => void>();
    const dispose = onGitTabChange(fn);
    fn.mockClear(); // drop the immediate fire so we only count change notifications

    setGitTab("changes"); // already the default → deduped signal → no notify

    expect(fn).not.toHaveBeenCalled();
    expect(getGitTab()).toBe("changes");
    dispose();
  });

  it("setGitTab pushes the matching URL; changes maps to the canonical /git", async () => {
    const { setGitTab } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

    setGitTab("prs");
    expect(location.pathname).toBe("/git/prs");

    setGitTab("sources");
    expect(location.pathname).toBe("/git/sources");

    setGitTab("changes");
    expect(location.pathname).toBe("/git");
  });

  it("names the active Git section below the tab bar", async () => {
    document.body.innerHTML = `
      <nav id="git-tab-bar">
        <button data-git-tab="changes"></button>
        <button data-git-tab="prs"></button>
        <button data-git-tab="sources"></button>
      </nav>
      <span id="git-page-title"></span>
      <div data-git-panel="changes"></div>
      <div data-git-panel="prs"></div>
      <div data-git-panel="sources"></div>`;
    const { initGitTabs, setGitTab } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

    initGitTabs();
    expect(document.getElementById("git-page-title")?.textContent).toBe("Changes");

    setGitTab("prs");
    expect(document.getElementById("git-page-title")?.textContent).toBe("Pull requests");
  });
});

// Local alias matching the module's exported GitTab union, kept here so the
// test arrays stay strongly typed without importing the type at module scope
// (each test imports the module fresh inside its own body).
type GitTabName = "changes" | "prs" | "sources";
