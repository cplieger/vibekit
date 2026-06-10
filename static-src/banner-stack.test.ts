// @vitest-environment happy-dom
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
//
// `./store.js` is mocked so `activeSession` is a writable signal we control;
// `./ui-state.js` load/save back a plain dismissals array; `$.bannerStack` is
// stubbed with a bare container.
//
// INSTANCE ALIGNMENT: each test does `vi.resetModules()` so banner-stack gets
// fresh module-level state. That also re-imports `@cplieger/reactive`, so the
// signal driving the test (`activeSig`) AND the `flushSync` that drains the
// bindList effect MUST come from that same post-reset instance — otherwise the
// computed view tracks a foreign signal (no subscription crosses instances)
// and never re-derives on switch. So setup() imports reactive fresh, builds
// `activeSig` from it, and returns its `flushSync`.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, afterEach } from "vitest";
import type { Signal } from "@cplieger/reactive";
import type * as BannerStack from "./banner-stack.js";

interface MiniSession {
  readonly id: string;
}

let activeSig: Signal<MiniSession | undefined>;
let dismissed: string[] = [];

async function setup(): Promise<{
  container: HTMLDivElement;
  mod: typeof BannerStack;
  flush: () => void;
}> {
  vi.resetModules();
  const reactive = await import("@cplieger/reactive");
  activeSig = reactive.signal<MiniSession | undefined>(undefined);
  dismissed = [];
  const container = document.createElement("div");
  vi.doMock("./store.js", () => ({ activeSession: activeSig }));
  vi.doMock("./ui-state.js", () => ({
    load: (): { dismissed_banners: string[] } => ({ dismissed_banners: dismissed }),
    save: (patch: { dismissed_banners?: string[] }): void => {
      if (patch.dismissed_banners !== undefined) {
        dismissed = patch.dismissed_banners;
      }
    },
  }));
  vi.doMock("./dom.js", () => ({ $: { bannerStack: container } }));
  const mod = await import("./banner-stack.js");
  return { container, mod, flush: reactive.flushSync };
}

afterEach(() => {
  vi.doUnmock("./store.js");
  vi.doUnmock("./ui-state.js");
  vi.doUnmock("./dom.js");
  vi.resetModules();
});

describe("banner-stack: active-chat scoping", () => {
  it("shows a banner only for its active chat and never resurrects a cleared one", async () => {
    const { container, mod, flush } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "x", "boom", "error", false);
    flush();
    expect(container.querySelectorAll(".banner")).toHaveLength(1);

    // Switch away: chat A's banner must hide (chat B has none).
    activeSig.value = { id: "B" };
    flush();
    expect(container.querySelectorAll(".banner")).toHaveLength(0);

    // Clear chat A's banner while it is hidden, then switch back: the bug was
    // that a stale render source resurrected it. It must stay gone.
    mod.clearBannerCodes("A", ["x"]);
    activeSig.value = { id: "A" };
    flush();
    expect(container.querySelectorAll(".banner")).toHaveLength(0);
  });

  it("reuses the same banner node across A -> B -> A switches", async () => {
    const { container, mod, flush } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "x", "boom", "error", false);
    flush();
    const node1 = container.querySelector(".banner");
    expect(node1).not.toBeNull();

    activeSig.value = { id: "B" };
    flush();
    expect(container.querySelectorAll(".banner")).toHaveLength(0);

    activeSig.value = { id: "A" };
    flush();
    const node2 = container.querySelector(".banner");
    expect(node2).toBe(node1);
  });

  it("ensureBound() is idempotent: a second call does not double-bind", async () => {
    const { container, mod, flush } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.ensureBound();
    mod.showBanner("A", "x", "boom", "error", false);
    flush();
    expect(container.querySelectorAll(".banner")).toHaveLength(1);
  });
});
