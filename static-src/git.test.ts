// ---------------------------------------------------------------------------
// The git view's activation half and its fetch half.
//
// `initGitPanel` used to fetch twice over: its `onGitTabChange` subscription fires
// immediately on attach, and its `else` branch ran the active sub-tab's refresh
// inline. With the dispatcher supplying the fetch, either would DOUBLE-fetch on
// every activation.
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach } from "vitest";

import type * as ModGit from "./git.js";

let bootSeq = 0;

const H = vi.hoisted(() => ({
  refreshChanges: vi.fn(),
  refreshSources: vi.fn(),
  refreshPRsDispatch: vi.fn(),
  changesClose: vi.fn(),
  prsClose: vi.fn(),
  /** The sub-tab the panel is on. `getGitTab` is the only reader that matters here. */
  tab: { current: "changes" as string },
  /** The subscriber `initGitTabs` would notify. Captured so a case can fire it. */
  listener: { fn: null as ((tab: string) => void) | null },
}));

const find = { open: vi.fn(), toggle: vi.fn(), focused: vi.fn(() => false), kind: vi.fn(() => "") };

vi.mock("./git-tabs.js", () => ({
  initGitTabs: vi.fn(),
  onGitTabChange: (fn: (tab: string) => void) => {
    H.listener.fn = fn;
    // `subscribe` fires immediately on attach, which is the fire the `painted` gate
    // exists to refuse: it is a DOM sync, not a reader switching sub-tabs.
    fn(H.tab.current);
    return () => undefined;
  },
  getGitTab: () => H.tab.current,
  readGitTab: () => H.tab.current,
}));
vi.mock("./git-changes-tab.js", () => ({
  initChangesTab: vi.fn(),
  refreshChanges: H.refreshChanges,
  changesFind: { ...find, close: H.changesClose },
}));
vi.mock("./git-prs-tab.js", () => ({
  initPRsTab: vi.fn(),
  prsFind: { ...find, close: H.prsClose },
}));
vi.mock("./actions/git-prs.js", () => ({
  refreshPRs: { dispatch: H.refreshPRsDispatch },
}));
vi.mock("./git-sources-tab.js", () => ({
  initSourcesTab: vi.fn(),
  refreshSources: H.refreshSources,
}));
vi.mock("./git-status-banner.js", () => ({ initStatusBanner: vi.fn() }));
vi.mock("./git-badge.js", () => ({ initGitBadge: vi.fn(), refreshGitBadge: vi.fn() }));
vi.mock("./git-status-store.js", () => ({ refreshGitStatus: vi.fn() }));
vi.mock("./find-registry.js", () => ({ registerFind: vi.fn() }));

/** A fresh module per test: `initialized` and `painted` are module-level. */
async function load(): Promise<typeof ModGit> {
  bootSeq += 1;
  return (await import(/* @vite-ignore */ `./git.ts?boot=${String(bootSeq)}`)) as typeof ModGit;
}

function fetched(): number {
  return (
    H.refreshChanges.mock.calls.length +
    H.refreshPRsDispatch.mock.calls.length +
    H.refreshSources.mock.calls.length
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  H.tab.current = "changes";
  H.listener.fn = null;
});

describe("initGitPanel", () => {
  it("fires no fetch of its own and closes no find box on the first call", async () => {
    const { initGitPanel } = await load();

    initGitPanel();

    expect(fetched()).toBe(0);
    expect(H.changesClose).not.toHaveBeenCalled();
    expect(H.prsClose).not.toHaveBeenCalled();
  });

  it("fires none on a second call either", async () => {
    const { initGitPanel } = await load();
    initGitPanel();

    initGitPanel();

    expect(fetched()).toBe(0);
  });

  it("refetches on a real sub-tab switch, and closes both find boxes", async () => {
    const { initGitPanel } = await load();
    initGitPanel();

    H.listener.fn?.("prs");

    expect(H.refreshPRsDispatch).toHaveBeenCalledWith({ force: false });
    expect(H.changesClose).toHaveBeenCalledTimes(1);
    expect(H.prsClose).toHaveBeenCalledTimes(1);
  });
});

describe("refreshGitView", () => {
  it("dispatches the ACTIVE sub-tab's refresh and no other", async () => {
    H.tab.current = "prs";
    const { refreshGitView } = await load();

    refreshGitView();

    expect(H.refreshPRsDispatch).toHaveBeenCalledTimes(1);
    expect(H.refreshChanges).not.toHaveBeenCalled();
    expect(H.refreshSources).not.toHaveBeenCalled();
  });

  it("forces the Changes tab and does not force the PRs fan-out", async () => {
    // `?fetch=1` on Changes runs a local `git fetch`, the only way to learn remote
    // state; every PR row is already remote and the server caches the listings.
    const { refreshGitView } = await load();

    refreshGitView();
    H.tab.current = "sources";
    refreshGitView();

    expect(H.refreshChanges).toHaveBeenCalledWith(true);
    expect(H.refreshSources).toHaveBeenCalledTimes(1);
  });

  it("runs the one-shot init itself, so it does not depend on onShow having run", async () => {
    // Two dynamic imports of one specifier resolve in whatever order the host
    // chooses, so the refresh cannot assume the activation's init landed first.
    const { refreshGitView } = await load();

    refreshGitView();

    expect(H.listener.fn).not.toBeNull();
    expect(H.refreshChanges).toHaveBeenCalledTimes(1);
  });
});
