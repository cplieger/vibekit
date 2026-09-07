// Tests for the PWA share-target + URL shortcut handling in share-target.ts.
// chat.ts has a heavy import graph, so it's mocked to a spy — this file only
// verifies the wiring: which URL params trigger which action.
import { describe, it, expect, vi, beforeEach } from "vitest";

const { createPlannerSessionMock } = vi.hoisted(() => ({
  createPlannerSessionMock: vi.fn(async () => undefined),
}));

vi.mock("./chat.js", () => ({
  createPlannerSession: createPlannerSessionMock,
}));

// share-target.ts touches only $.promptInput; back it with a real textarea so
// the value/focus writes land on a real element. The element is created inside
// the factory (which is hoisted) and read back via the mocked import below,
// avoiding a top-level TDZ reference.
vi.mock("./dom.js", () => ({
  $: { promptInput: document.createElement("textarea") },
}));

import { applyShareTarget } from "./share-target.js";
import type * as ShareTargetModule from "./share-target.js";
import { $ } from "./dom.js";

/** A fresh module per test. `queued` and `bootApplied` are module state, and
 *  `vi.resetModules()` does not re-evaluate a module in Browser Mode — the module
 *  map is URL-keyed, so a busted specifier is what mints a new instance. The `.ts`
 *  extension is load-bearing for coverage attribution. */
let seq = 0;
async function freshShareTarget(): Promise<typeof ShareTargetModule> {
  seq++;
  return (await import(
    /* @vite-ignore */ `./share-target.ts?t=${String(seq)}`
  )) as typeof ShareTargetModule;
}

/** A `launchQueue` that records its consumer, so a test can decide WHEN the
 *  platform delivers — the difference between a cold launch (buffered params
 *  flushed as the consumer is set) and a launch into a running window. */
function stubLaunchQueue(): { deliver: (targetURL: string) => void } {
  let consumer: ((p: { readonly targetURL?: string }) => void) | null = null;
  vi.stubGlobal("launchQueue", {
    setConsumer: (fn: (p: { readonly targetURL?: string }) => void) => {
      consumer = fn;
    },
  });
  return {
    deliver: (targetURL: string) => {
      consumer?.({ targetURL });
    },
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  $.promptInput.value = "";
  history.replaceState(null, "", "/");
});

describe("applyShareTarget", () => {
  it("creates a planner session on ?agent=planner", async () => {
    history.replaceState(null, "", "/?agent=planner");
    await applyShareTarget();
    expect(createPlannerSessionMock).toHaveBeenCalledTimes(1);
  });

  it("does NOT create a planner session without ?agent=planner", async () => {
    history.replaceState(null, "", "/?agent=other");
    await applyShareTarget();
    expect(createPlannerSessionMock).not.toHaveBeenCalled();
  });

  it("does nothing (no planner) on a bare URL", async () => {
    await applyShareTarget();
    expect(createPlannerSessionMock).not.toHaveBeenCalled();
    expect($.promptInput.value).toBe("");
  });

  it("populates the prompt input from ?prompt= without creating a planner", async () => {
    history.replaceState(null, "", "/?prompt=hello");
    await applyShareTarget();
    expect($.promptInput.value).toBe("hello");
    expect(createPlannerSessionMock).not.toHaveBeenCalled();
  });

  // AWAITED, and that is the assertion: the planner create is the one thing here
  // that has to finish before boot applies the initial route, because the chat id
  // is the server's now. A synchronous call would strip the query while the tab
  // the route resolves against did not exist yet.
  it("strips the query string after applying so a reload doesn't re-fire", async () => {
    history.replaceState(null, "", "/?agent=planner");
    await applyShareTarget();
    expect(location.search).toBe("");
  });
});

// A cold launch takes BOTH doors: the browser navigates the document to the
// target URL and enqueues the same params for `launchQueue`. One launch must
// produce one planner chat, and it must be created inside the boot's ordering —
// the boot activates the restored tab, applies the launch, and only then resolves
// the URL against the strip.
describe("initLaunchQueue", () => {
  it("applies a cold launch ONCE even though both doors carry it", async () => {
    history.replaceState(null, "", "/?agent=planner");
    const queue = stubLaunchQueue();
    const { initLaunchQueue, applyShareTarget: applyBootLaunch } = await freshShareTarget();

    initLaunchQueue();
    queue.deliver("/?agent=planner");
    await applyBootLaunch();

    expect(createPlannerSessionMock).toHaveBeenCalledTimes(1);
  });

  it("leaves a cold launch's params for the boot rather than applying them itself", async () => {
    history.replaceState(null, "", "/?agent=planner");
    const queue = stubLaunchQueue();
    const { initLaunchQueue } = await freshShareTarget();

    initLaunchQueue();
    queue.deliver("/?agent=planner");

    // The queue's copy is HELD: creating the chat here would put it outside the
    // ordering applyInitialRoute resolves against.
    expect(createPlannerSessionMock).not.toHaveBeenCalled();
  });

  it("applies a launch that arrives after the boot, which is what focus-existing needs", async () => {
    const queue = stubLaunchQueue();
    const { initLaunchQueue, applyShareTarget: applyBootLaunch } = await freshShareTarget();

    initLaunchQueue();
    // A bare boot: nothing to apply, so the shortcut below is the first launch.
    await applyBootLaunch();
    expect(createPlannerSessionMock).not.toHaveBeenCalled();

    queue.deliver("/?agent=planner");
    await vi.waitFor(() => {
      expect(createPlannerSessionMock).toHaveBeenCalledTimes(1);
    });
  });

  it("populates the composer from a share into a running window", async () => {
    const queue = stubLaunchQueue();
    const { initLaunchQueue, applyShareTarget: applyBootLaunch } = await freshShareTarget();

    initLaunchQueue();
    await applyBootLaunch();
    queue.deliver("/?prompt=shared%20text");

    await vi.waitFor(() => {
      expect($.promptInput.value).toBe("shared text");
    });
  });

  it("registers nothing when the platform has no launch queue", async () => {
    history.replaceState(null, "", "/?agent=planner");
    const { initLaunchQueue, applyShareTarget: applyBootLaunch } = await freshShareTarget();

    // No stub: `launchQueue` is absent, which is every non-Chromium browser.
    initLaunchQueue();
    await applyBootLaunch();

    // The document's own door still carries the launch.
    expect(createPlannerSessionMock).toHaveBeenCalledTimes(1);
  });
});
