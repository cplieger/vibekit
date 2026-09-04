// What makes the git-status store read the tree — and what no longer does.
//
// It polled `/api/git/status-all` every 15 s and fired an extra FULL scan on
// every `turn_ended`. One scan is 270 git subprocesses across 54 worktrees, so
// an idle page paid for a scan every 15 seconds for a tree nothing had touched,
// and a turn ending is a GUESS that the tree changed. The client already holds
// the fact — `handlers/messages.ts` sees each repo-mutating tool call complete —
// and was throwing it away.
//
// Its own file because it has to mock the actions layer to count the reads, and
// `git-status-store.test.ts` drives the index rules through `_setReposForTest`
// with no mocks at all.
import { describe, it, expect, vi } from "vitest";

// PLAIN counters rather than `vi.fn` call history: the root config resets mocks
// between tests, so a count taken in one test is unreadable from the next — and
// what has to be asserted here is that starting the store registered NOTHING,
// which is only observable after the call that could have.
const { observed } = vi.hoisted(() => ({
  observed: { reads: 0, pollers: 0, sseSubs: 0 },
}));

vi.mock("./actions/index.js", () => ({
  apiAction: () => ({
    dispatch: () => Promise.resolve({ repos: [] }),
  }),
  defineAction: () => ({
    dispatch: () => {
      observed.reads++;
      return Promise.resolve({ repos: [] });
    },
  }),
  pollAction: () => {
    observed.pollers++;
  },
}));
// The bus, so a `turn_ended` subscription would be visible if one were made.
vi.mock("./bus.js", () => ({
  onSSE: () => {
    observed.sseSubs++;
  },
}));

const store = await import("./git-status-store.js");

describe("the git-status store's triggers", () => {
  // ONE test, because `started` is module state: the store can only be observed
  // STARTING once per module instance, and the absence claims are meaningful only
  // after the call that could have made each of them false.
  it("reads nothing until a surface subscribes, then reads once and schedules nothing", async () => {
    // IMPORTING IS NOT A REASON TO SCAN. The read used to ride
    // `initGitStatusStore()`, which `initFileBrowser` called at boot, so one scan
    // of every worktree in the workspace fired during the boot chain for a file
    // browser nobody had opened.
    await Promise.resolve();
    expect(observed.reads).toBe(0);

    const off = store.onGitStatusChange(() => undefined);
    await Promise.resolve();
    expect(observed.reads).toBe(1);

    // No timer: an idle page costs nothing.
    expect(observed.pollers).toBe(0);
    // No `turn_ended` subscription: a turn ending is a guess, and the fact it was
    // guessing at now arrives per completed tool call through markGitDirty.
    expect(observed.sseSubs).toBe(0);

    // Three surfaces subscribe (the badge, the file browser, the docs page) and
    // the read belongs to the store, so it happens once for all of them.
    const off2 = store.onGitStatusChange(() => undefined);
    const off3 = store.onGitStatusChange(() => undefined);
    await Promise.resolve();
    expect(observed.reads).toBe(1);

    off();
    off2();
    off3();
  });

  it("reads when the fact arrives, which is the only automatic trigger left", async () => {
    const before = observed.reads;
    await store.refreshGitStatus();
    expect(observed.reads).toBe(before + 1);
    await store.refreshGitStatus();
    expect(observed.reads).toBe(before + 2);
  });
});
