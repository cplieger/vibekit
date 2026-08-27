// Tests for the one owner of /api/forges.
//
// The subject is REQUEST COUNT, because that is what the store was built to
// change: three modules used to fetch this endpoint and each kept its answer
// private. So every case here asks how many times the action was dispatched, and
// the payload only has to be distinguishable between calls.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as ModStore from "./forge-store.js";

// Cache-buster for the re-imports below: vi.resetModules() does not re-evaluate
// a module in Browser Mode (the module map is URL-keyed), and this store's
// `started` flag and payload signal are module state every case needs fresh.
let bootSeq = 0;

const dispatch = vi.fn();
const pollAction = vi.fn();
const onSSE = vi.fn();

vi.mock("./actions/forge-list.js", () => ({ listForges: { dispatch, cancel: vi.fn() } }));
vi.mock("./actions/index.js", () => ({ pollAction, registerCleanup: vi.fn() }));
vi.mock("./bus.js", () => ({ onSSE }));

const payload = (username: string) => ({
  forges: [
    {
      id: "github:github.com",
      kind: "github" as const,
      host: "github.com",
      username,
      connected: true,
    },
  ],
  kinds: ["github", "gitlab", "codeberg", "gitea"] as const,
  oauth: { github: true },
});

async function load(): Promise<typeof ModStore> {
  bootSeq += 1;
  return (await import(
    /* @vite-ignore */ `./forge-store.ts?boot=${String(bootSeq)}`
  )) as typeof ModStore;
}

beforeEach(() => {
  dispatch.mockReset();
  pollAction.mockReset();
  onSSE.mockReset();
});

describe("forge-store read-through", () => {
  it("ensureForges fetches once, then answers from what it already has", async () => {
    dispatch.mockResolvedValue(payload("alice"));
    const store = await load();

    const first = await store.ensureForges();
    const second = await store.ensureForges();

    expect(first?.forges[0]?.username).toBe("alice");
    expect(second).toEqual(first);
    // The whole point: the PR fan-out is the expensive caller and must not add a
    // round trip of its own once the list is known.
    expect(dispatch).toHaveBeenCalledTimes(1);
  });

  it("refreshForges always fetches, because its callers have a reason to distrust the cache", async () => {
    dispatch.mockResolvedValueOnce(payload("alice")).mockResolvedValueOnce(payload("bob"));
    const store = await load();

    await store.ensureForges();
    const forced = await store.refreshForges();

    expect(dispatch).toHaveBeenCalledTimes(2);
    expect(forced?.forges[0]?.username).toBe("bob");
    // And the refreshed value is what later readers see.
    expect(store.currentForges()[0]?.username).toBe("bob");
  });

  it("publishes the payload to its accessors", async () => {
    dispatch.mockResolvedValue(payload("alice"));
    const store = await load();

    expect(store.currentForges()).toEqual([]);
    expect(store.oauthByKind()).toEqual({});

    await store.ensureForges();

    expect(store.currentForges()).toHaveLength(1);
    expect(store.oauthByKind()).toEqual({ github: true });
  });

  it("notifies subscribers when a payload lands", async () => {
    dispatch.mockResolvedValue(payload("alice"));
    const store = await load();
    const seen: number[] = [];
    // subscribe fires immediately with the current value, so the first entry is
    // the empty state rather than a load.
    store.onForgeChange(() => {
      seen.push(store.currentForges().length);
    });

    await store.ensureForges();

    expect(seen).toEqual([0, 1]);
  });
});

describe("forge-store failure handling", () => {
  it("reports a failure without discarding the last good payload", async () => {
    dispatch.mockResolvedValueOnce(payload("alice")).mockResolvedValueOnce(null);
    const store = await load();

    await store.ensureForges();
    expect(store.forgeLoadFailed()).toBe(false);

    await store.refreshForges();

    expect(store.forgeLoadFailed()).toBe(true);
    // Blanking here would turn one bad round trip into "no forges connected" on
    // the badge and in the PRs tab.
    expect(store.currentForges()).toHaveLength(1);
  });

  it("a failed first load leaves the list empty and retries on the next read", async () => {
    dispatch.mockResolvedValueOnce(null).mockResolvedValueOnce(payload("alice"));
    const store = await load();

    expect(await store.ensureForges()).toBeNull();
    expect(store.forgeLoadFailed()).toBe(true);
    // Nothing was cached, so the next read must reach the endpoint again rather
    // than serving the failure forever.
    expect(await store.ensureForges()).not.toBeNull();
    expect(store.forgeLoadFailed()).toBe(false);
    expect(dispatch).toHaveBeenCalledTimes(2);
  });
});

describe("forge-store lifecycle", () => {
  it("starts exactly one poll and one invalidation listener, however many init paths call it", async () => {
    dispatch.mockResolvedValue(payload("alice"));
    const store = await load();

    store.initForgeStore();
    store.initForgeStore();
    store.initForgeStore();

    // Several modules reach init (the badge today, any future consumer), so a
    // second timer here would be the duplication this store removed.
    expect(pollAction).toHaveBeenCalledTimes(1);
    expect(onSSE).toHaveBeenCalledTimes(1);
    expect(onSSE.mock.calls[0]?.[0]).toBe("forges_changed");
    expect(pollAction.mock.calls[0]?.[2]).toMatchObject({ interval: 15_000 });
  });
});
