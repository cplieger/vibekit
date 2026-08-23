// ---------------------------------------------------------------------------
// banner-stack regression tests.
//
// The stack is a single bindList over a computed active-chat view, so every
// add / remove / chat-switch flows through ONE reactive render source. These
// tests lock that contract in:
//   1. desync repro — a banner is visible only for its own active chat, hides
//      on switch-away, and a cleared banner never resurrects on switch-back.
//   2. identity — A -> B -> A reuses the SAME entry-owned DOM node.
//   3. idempotency — calling ensureBound() twice does not double-bind.
//   4. single live region — the stack container is the only aria-live region;
//      individual banners carry no role/aria-live (no nested live regions).
//
// `./store.js` is mocked so `activeSession` is a writable signal we control;
// `./ui-state.js` load/save back a plain dismissals array; `$.bannerStack` is
// stubbed with a bare container.
//
// INSTANCE ALIGNMENT: each test does `vi.resetModules()` so banner-stack gets
// fresh module-level state. That also re-imports `@cplieger/reactive`, so the
// signal driving the test (`activeSig`) MUST come from that same post-reset
// instance — otherwise the computed view tracks a foreign signal (no
// subscription crosses instances) and never re-derives on switch. So setup()
// imports reactive fresh and builds `activeSig` from it.
//
// There is no drain call here and none is needed: a write flushes before the
// assignment returns, so `activeSig.value = …` has already re-run the bindList
// effect. (This used to also return the module's `flushSync`, on the belief that
// the barrier had to come from the aligned instance too. The instance reasoning
// is right about the signal and was moot for the barrier, which did nothing on
// either instance.)
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, afterEach } from "vitest";
import { signal } from "@cplieger/reactive";
import type * as BannerStack from "./banner-stack.js";

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

interface MiniSession {
  readonly id: string;
}

// ONE signal for the whole file, reset per test rather than replaced. The mocks
// below are registered once at module scope and their factories run once, so a
// re-created signal would leave every banner-stack instance after the first
// subscribed to a dead one. (A getter does not help: the mocked module's exports
// are read out of the factory result once, when that module is evaluated.)
const activeSig = signal<MiniSession | undefined>(undefined);
let dismissed: string[] = [];
let container: HTMLDivElement;

// The three mocks are registered ONCE, at module scope, and reach the per-test
// values through this file's module state rather than closing over per-test
// values. A mocked module is evaluated the first time it is imported and then
// cached, so a per-test `vi.doMock` re-registers a factory that never runs again:
// the second test's banner-stack instance kept reading the FIRST test's signal
// and container. `$` stays a getter because a property read on the exported
// object IS live.
vi.mock("./store.js", () => ({ activeSession: activeSig }));
vi.mock("./ui-state.js", () => ({
  load: (): { dismissed_banners: string[] } => ({ dismissed_banners: dismissed }),
  save: (patch: { dismissed_banners?: string[] }): void => {
    if (patch.dismissed_banners !== undefined) {
      dismissed = patch.dismissed_banners;
    }
  },
}));
vi.mock("./dom.js", () => ({
  $: {
    get bannerStack(): HTMLDivElement {
      return container;
    },
  },
}));

async function setup(): Promise<{
  container: HTMLDivElement;
  mod: typeof BannerStack;
}> {
  vi.resetModules();
  bootSeq++;
  // One reactive instance exists for the whole run now — nothing re-evaluates it
  // — so the signal driving the test and the one the module subscribes to are
  // necessarily the same. (This used to be delicate: under a runner where
  // resetModules re-evaluated the graph, a signal built before the reset tracked
  // a foreign instance and never re-derived.)
  activeSig.value = undefined;
  dismissed = [];
  container = document.createElement("div");
  const mod = (await import(
    /* @vite-ignore */ `./banner-stack.ts?boot=${bootSeq}`
  )) as typeof BannerStack;
  return { container, mod };
}

afterEach(() => {
  // No doUnmock: the mocks above are module-scoped and permanent by necessity.
  vi.resetModules();
  bootSeq++;
});

describe("banner-stack: active-chat scoping", () => {
  it("shows a banner only for its active chat and never resurrects a cleared one", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "x", "boom", "error", false);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);

    // Switch away: chat A's banner must hide (chat B has none).
    activeSig.value = { id: "B" };
    expect(container.querySelectorAll(".banner")).toHaveLength(0);

    // Clear chat A's banner while it is hidden, then switch back: the bug was
    // that a stale render source resurrected it. It must stay gone.
    mod.clearBannerCodes("A", ["x"]);
    activeSig.value = { id: "A" };
    expect(container.querySelectorAll(".banner")).toHaveLength(0);
  });

  it("reuses the same banner node across A -> B -> A switches", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "x", "boom", "error", false);
    const node1 = container.querySelector(".banner");
    expect(node1).not.toBeNull();

    activeSig.value = { id: "B" };
    expect(container.querySelectorAll(".banner")).toHaveLength(0);

    activeSig.value = { id: "A" };
    const node2 = container.querySelector(".banner");
    expect(node2).toBe(node1);
  });

  it("ensureBound() is idempotent: a second call does not double-bind", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.ensureBound();
    mod.showBanner("A", "x", "boom", "error", false);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);
  });
});

describe("banner-stack: composite key (keyenc)", () => {
  it("keeps the pre-adoption key bytes, so no persisted dismissal is lost", async () => {
    // The dismissal set is PERSISTED in localStorage under dismissed_banners.
    // A chat id is [A-Za-z0-9_-] (ids.ValidChatID) and a code is a call-site
    // literal, so keyenc emits both verbatim and the key is byte-identical to
    // the old `${chatID}:${code}` template. Asserted through the public API: a
    // dismissal recorded in the OLD format must still suppress its banner.
    const { container, mod } = await setup();
    dismissed = ["c-1750000000000-ab12cd:rate_limit"];

    activeSig.value = { id: "c-1750000000000-ab12cd" };
    mod.ensureBound();
    mod.showBanner("c-1750000000000-ab12cd", "rate_limit", "slow down", "warning", true);

    expect(container.querySelectorAll(".banner")).toHaveLength(0);
  });

  it("does not let one field's content forge the other's boundary", async () => {
    // Both fields are colon-free today; this pins the property the join adds,
    // so a future loosening of either field can't silently collide two
    // banners into one collection slot. Under the old template ("a:b" + "c")
    // and ("a" + "b:c") were the same key.
    const { container, mod } = await setup();

    activeSig.value = { id: "a:b" };
    mod.ensureBound();
    mod.showBanner("a:b", "c", "first", "error", false);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);

    // Same forged key under the old scheme, different chat: must not be seen
    // as the same entry, and must not show on chat "a:b".
    mod.showBanner("a", "b:c", "second", "error", false);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);
    expect(container.textContent).toContain("first");

    activeSig.value = { id: "a" };
    expect(container.textContent).toContain("second");
  });

  it("clearBannersForChat prefix scan: chat \u201cabc\u201d does not clear chat \u201cabcd\u201d", async () => {
    // clearBannersForChat keeps a `${chatID}:` prefix scan (keyenc exports no
    // prefix primitive). The trailing ":" is what bounds it — this is the
    // regression test named in that function's comment.
    const { container, mod } = await setup();

    activeSig.value = { id: "abc" };
    mod.ensureBound();
    mod.showBanner("abc", "x", "short", "error", true);
    mod.showBanner("abcd", "x", "long", "error", true);
    expect(container.textContent).toContain("short");

    // Persist both dismissals so the localStorage half of the scan is covered.
    dismissed = ["abc:x", "abcd:x"];

    mod.clearBannersForChat("abc");

    // Only chat abc's persisted dismissal was pruned.
    expect(dismissed).toEqual(["abcd:x"]);

    // And the sibling chat's in-memory banner survived the sweep.
    activeSig.value = { id: "abcd" };
    expect(container.textContent).toContain("long");
  });
});

describe("banner-stack: single live region", () => {
  it("marks only the stack container as a live region; banners are not separately live", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    // The stack container is the SINGLE polite live region.
    expect(container.getAttribute("aria-live")).toBe("polite");

    mod.showBanner("A", "err", "boom", "error", false);
    mod.showBanner("A", "inf", "note", "info", false);

    const nodes = container.querySelectorAll(".banner");
    expect(nodes).toHaveLength(2);
    // No nested live region on individual banners: dropping role="alert"/"status"
    // is what prevents the double-announce.
    for (const node of nodes) {
      expect(node.hasAttribute("role")).toBe(false);
      expect(node.hasAttribute("aria-live")).toBe(false);
    }
  });
});
