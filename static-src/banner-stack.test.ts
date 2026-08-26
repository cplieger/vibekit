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
import { LS_DISMISSED_BANNERS_KEY } from "./ls-keys.js";
import type * as BannerStack from "./banner-stack.js";
import toolCardSource from "./tool-card.ts?raw";

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
let container: HTMLDivElement;

// The three mocks are registered ONCE, at module scope, and reach the per-test
// values through this file's module state rather than closing over per-test
// values. A mocked module is evaluated the first time it is imported and then
// cached, so a per-test `vi.doMock` re-registers a factory that never runs again:
// the second test's banner-stack instance kept reading the FIRST test's signal
// and container. `$` stays a getter because a property read on the exported
// object IS live.
vi.mock("./store.js", () => ({ activeSession: activeSig }));
// No ui-state mock: the dismissals are per-chat localStorage now, which jsdom
// provides for real. That is worth having rather than faking — the shape under
// test IS the stored document, and a fake of it could not catch a chat key
// colliding with another chat's.
vi.mock("./dom.js", () => ({
  $: {
    get bannerStack(): HTMLDivElement {
      return container;
    },
  },
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
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
  localStorage.clear();
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
  // The dismissals are stored per CHAT, so a chat id can no longer be part of a
  // composite storage key and the forging question does not arise for the
  // persisted half at all. Asserted through the public API: a dismissal recorded
  // for one chat suppresses that chat's banner and no other's.
  it("suppresses a banner this reader dismissed, per chat", async () => {
    const { container, mod } = await setup();
    localStorage.setItem(
      LS_DISMISSED_BANNERS_KEY,
      JSON.stringify({ "c-1750000000000-ab12cd": ["rate_limit"] }),
    );

    activeSig.value = { id: "c-1750000000000-ab12cd" };
    mod.ensureBound();
    mod.showBanner("c-1750000000000-ab12cd", "rate_limit", "slow down", "warning", true);
    expect(container.querySelectorAll(".banner")).toHaveLength(0);

    // A different chat's identical code is a different acknowledgement.
    activeSig.value = { id: "c-other" };
    mod.showBanner("c-other", "rate_limit", "slow down", "warning", true);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);
  });

  // The whole reason these moved out of the shared arrangement: an
  // acknowledgement is the VIEWER's, so it must not leave this device. Pinned as
  // a storage-key assertion because that is the only observable difference
  // between a per-device store and the server-owned document it replaced.
  it("records a dismissal in this device's own storage, not the arrangement", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "rate_limit", "slow down", "warning", true);
    container.querySelector<HTMLButtonElement>(".banner-dismiss")?.click();

    const raw = localStorage.getItem(LS_DISMISSED_BANNERS_KEY);
    expect(raw).not.toBeNull();
    expect(JSON.parse(raw ?? "{}")).toEqual({ A: ["rate_limit"] });
    // And nothing was written to the synced arrangement's key.
    expect(localStorage.getItem("vibekit.ui-state")).toBeNull();
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
    // clearBannersForChat keeps a `${chatID}:` prefix scan over the COLLECTION's
    // keys (keyenc exports no prefix primitive). The trailing ":" is what bounds
    // it — this is the regression test named in that function's comment. It no
    // longer touches localStorage at all: the persisted half went with the global
    // list, so the sweep is in-memory only.
    const { container, mod } = await setup();

    activeSig.value = { id: "abc" };
    mod.ensureBound();
    mod.showBanner("abc", "x", "short", "error", true);
    mod.showBanner("abcd", "x", "long", "error", true);
    expect(container.textContent).toContain("short");

    mod.clearBannersForChat("abc");

    // The sibling chat's in-memory banner survived the sweep.
    activeSig.value = { id: "abcd" };
    expect(container.textContent).toContain("long");

    // And a dismissal for the sibling is untouched, because nothing prunes
    // storage here any more.
    localStorage.setItem(LS_DISMISSED_BANNERS_KEY, JSON.stringify({ abcd: ["x"] }));
    mod.clearBannersForChat("abc");
    expect(JSON.parse(localStorage.getItem(LS_DISMISSED_BANNERS_KEY) ?? "{}")).toEqual({
      abcd: ["x"],
    });
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

// ---------------------------------------------------------------------------
// The severity's non-colour channel.
//
// Before the glyph, a banner's level lived in exactly two places —
// `border-left-color` and `color` — both of them colour, which is WCAG 1.4.1 for
// the same reason a bare coloured dot is. It was also the reason the left border
// could not simply be deleted with the other state-carrying edges: a border is
// the only one of the two channels that survives `forced-colors: active` (a
// background-color is flattened there, a border still renders), and
// `40-a11y.css`'s forced-colors block covers `.uip-modal-dialog`, `.popup`,
// `.tool-call` and `.subagent-block` — not banners. So the shape had to land
// before the edge could go, and these cases are what say it did.
// ---------------------------------------------------------------------------

/** `tool-card.ts`'s OUTCOME_BADGE, read as SOURCE.
 *
 *  It is not exported, and this is the check that would otherwise be a comment:
 *  the banner glyphs MIRROR that vocabulary rather than inventing a second one,
 *  so if a future edit changes the cross or the triangle over there, this fails
 *  here instead of the transcript and the banner stack quietly disagreeing. Read
 *  as text rather than imported for real, the same technique
 *  `__test-helpers__/css-rules.ts` uses on the stylesheets — importing tool-card
 *  would drag its whole DOM graph into this file's mock set for one constant. */
function outcomeBadge(state: string): string {
  // `const` anchors the DECLARATION: the first bare `OUTCOME_BADGE` in that file
  // is the lookup inside applyOutcome, whose braces are a subscript.
  const body = /const OUTCOME_BADGE[^{]*\{([^}]*)\}/.exec(toolCardSource)?.[1] ?? "";
  const hit = new RegExp(`${state}:\\s*"([^"]*)"`).exec(body)?.[1];
  if (hit === undefined) {
    throw new Error(`tool-card.ts OUTCOME_BADGE has no ${state} member`);
  }
  // The source spells them as escapes; this is the character they denote.
  return JSON.parse(`"${hit}"`) as string;
}

describe("banner-stack: severity carries a shape, not colour alone", () => {
  it("gives every level a glyph, and mirrors tool-card's outcome vocabulary", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "e", "boom", "error", false);
    mod.showBanner("A", "w", "careful", "warning", false);
    mod.showBanner("A", "i", "note", "info", false);

    const glyphFor = (code: string): string =>
      container.querySelector(
        `.banner-${code === "e" ? "error" : code === "w" ? "warning" : "info"} .banner-glyph`,
      )?.textContent ?? "";

    // error takes OUTCOME_BADGE.fail, warning takes OUTCOME_BADGE.warn.
    expect(glyphFor("e")).toBe(outcomeBadge("fail"));
    expect(glyphFor("w")).toBe(outcomeBadge("warn"));
    // `info` has no member over there (that vocabulary covers settled TOOL
    // outcomes only), so the banner picks U+2139 INFORMATION SOURCE.
    expect(glyphFor("i")).toBe("\u2139");
  });

  it("makes the three levels distinguishable by SHAPE, which is the point", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "e", "boom", "error", false);
    mod.showBanner("A", "w", "careful", "warning", false);
    mod.showBanner("A", "i", "note", "info", false);

    const glyphs = [...container.querySelectorAll(".banner-glyph")].map((g) => g.textContent);
    expect(glyphs).toHaveLength(3);
    // Three banners, three DIFFERENT characters. A shared glyph would leave the
    // level readable only by hue again, which is the failure being fixed.
    expect(new Set(glyphs).size).toBe(3);
  });

  it("hides the glyph from assistive tech, so the message is the whole announcement", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "e", "boom", "error", false);

    const glyph = container.querySelector(".banner-glyph");
    expect(glyph?.getAttribute("aria-hidden")).toBe("true");
    // The glyph is a restatement for the eye. The stack's own live region
    // announces the message text once; a glyph in that name would read as a
    // character before every notice.
    expect(container.querySelector(".banner-msg")?.textContent).toBe("boom");
  });

  it("keeps the glyph when a repeat call replaces the message in place", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "e", "boom", "error", false);
    // Same (chat, code): showBanner rewrites the text node on the entry-owned
    // element rather than rebuilding it, so the channel must survive that path.
    mod.showBanner("A", "e", "still boom", "error", false);

    expect(container.querySelectorAll(".banner")).toHaveLength(1);
    expect(container.querySelectorAll(".banner-glyph")).toHaveLength(1);
    expect(container.querySelector(".banner-msg")?.textContent).toBe("still boom");
  });
});
