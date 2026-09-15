// ---------------------------------------------------------------------------
// Find-in-Chat tests.
//
//   1. FindEngine — match discovery, case-insensitivity, highlight/unwrap,
//      visibility pruning, and next/prev stepping with wraparound.
//   2. Ctrl-F overlay integration — hotkey open, eligibility, search, stepping,
//      close + focus restore, and the "second Ctrl-F -> native find" escape
//      hatch.
//
// ./scroll.js is mocked so importing find-in-chat.ts doesn't trigger the
// ScrollController's eager DOM init (which needs #messages / #scroll-bottom).
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./scroll.js", () => ({
  jumpTo: vi.fn(),
  // Inert registration: these tests drive re-runs by calling the module's own
  // paths, not by mutating a transcript nothing here renders.
  onTranscriptMutate: vi.fn(() => () => undefined),
}));
// Spy-wrapped rather than replaced: every export keeps its real implementation
// and becomes observable. `vi.spyOn(namespace, name)` cannot do this in a real
// browser — an ESM module namespace is not configurable, so the assignment
// throws — and this suite needs to see one call that the DOM cannot show.
vi.mock("./chat-search.js", { spy: true });
// Same treatment for the store and the pagination entry point: the navigation
// tests hand them a fixture chat, and everything else calls through to the
// real implementations (whose defaults — no active chat — are what the
// overlay tests always ran against).
vi.mock("./store.js", { spy: true });
vi.mock("./store-load.js", { spy: true });
// A one-export factory, unlike the three above: this module is NOT in
// find-in-chat's own import graph (it is reached by a lazy `await import`), so
// nothing here needs its real implementation, and replacing it keeps the
// exec-view chunk out of this suite entirely.
vi.mock("./run-view.js", () => ({ openRunView: vi.fn() }));
// Same shape and same reason for the delegate tab: reached by a lazy `await import`, so
// replacing it keeps `exec-view/**` out of this suite.
vi.mock("./subagent-view.js", () => ({ openSubagentView: vi.fn() }));
// The renderer's per-block map, which is how a hit's element is resolved now: this
// file builds transcript DOM by hand, so no render would ever register one. A
// REPLACING factory rather than a spy — the real dispatcher's graph reaches
// `preserveReadingPosition` on the `./scroll.js` stubbed above, so loading it fails
// linking for the whole file.
vi.mock("./messages-blocks.js", () => ({ blockElement: vi.fn() }));

import { FindEngine } from "./find-engine.js";
import type * as ModFindInChat from "./find-in-chat.js";

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

function root(html: string): HTMLElement {
  const d = document.createElement("div");
  d.innerHTML = html;
  document.body.replaceChildren(d);
  return d;
}

function marks(el: HTMLElement): HTMLElement[] {
  return [...el.querySelectorAll<HTMLElement>("mark.find-hit")];
}

// ---------------------------------------------------------------------------
// FindEngine: matching + highlighting
// ---------------------------------------------------------------------------

describe("FindEngine matching", () => {
  it("wraps every match across multiple nodes and marks the first current", () => {
    const el = root(
      `<div class="message">alpha TODO beta</div>` +
        `<div class="message">gamma <b>TODO</b> delta</div>` +
        `<div class="message">nothing here</div>`,
    );
    const eng = new FindEngine(el);
    const n = eng.search("TODO");
    expect(n).toBe(2);
    expect(eng.total).toBe(2);
    expect(marks(el)).toHaveLength(2);
    expect(eng.currentIndex).toBe(0);
    expect(eng.currentMark()).toBe(marks(el)[0]);
    expect(marks(el)[0]?.classList.contains("find-hit-current")).toBe(true);
    expect(marks(el)[1]?.classList.contains("find-hit-current")).toBe(false);
  });

  it("matches case-insensitively but preserves the original casing in the mark", () => {
    const el = root(`<p>Todo todo TODO tODo</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("todo")).toBe(4);
    const texts = marks(el).map((m) => m.textContent);
    expect(texts).toEqual(["Todo", "todo", "TODO", "tODo"]);
  });

  it("matches only the exact casing when case sensitivity is asked for", () => {
    const el = root(`<p>Todo todo TODO tODo</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("todo", true)).toBe(1);
    expect(marks(el).map((m) => m.textContent)).toEqual(["todo"]);
  });

  it("matches an upper-case needle exactly under case sensitivity", () => {
    const el = root(`<p>Todo todo TODO</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("TODO", true)).toBe(1);
    expect(marks(el).map((m) => m.textContent)).toEqual(["TODO"]);
  });

  it("defaults to insensitive when the flag is omitted", () => {
    const el = root(`<p>Alpha alpha</p>`);
    expect(new FindEngine(el).search("ALPHA")).toBe(2);
  });

  it("finds every occurrence within one node under case sensitivity", () => {
    const el = root(`<p>Err err Err err</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("err", true)).toBe(2);
    expect(el.textContent).toBe("Err err Err err");
  });

  it("finds multiple matches within a single text node and preserves surrounding text", () => {
    const el = root(`<p>a TODO b TODO c</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("TODO")).toBe(2);
    expect(el.textContent).toBe("a TODO b TODO c");
  });

  it("returns zero and a null current mark when nothing matches", () => {
    const el = root(`<p>the quick brown fox</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("zzz")).toBe(0);
    expect(eng.currentIndex).toBe(-1);
    expect(eng.currentMark()).toBeNull();
    expect(marks(el)).toHaveLength(0);
  });

  it("treats an empty query as a clear (no marks)", () => {
    const el = root(`<p>TODO TODO</p>`);
    const eng = new FindEngine(el);
    eng.search("TODO");
    expect(marks(el)).toHaveLength(2);
    expect(eng.search("")).toBe(0);
    expect(marks(el)).toHaveLength(0);
  });

  it("clear() removes all marks and restores the original text; re-search still works", () => {
    const el = root(`<div>x TODO y</div><div>z TODO w</div>`);
    const original = el.textContent;
    const eng = new FindEngine(el);
    eng.search("TODO");
    expect(marks(el)).toHaveLength(2);
    eng.clear();
    expect(marks(el)).toHaveLength(0);
    expect(el.textContent).toBe(original);
    // Re-search after clear finds the same matches (text nodes were merged back).
    expect(eng.search("TODO")).toBe(2);
  });

  it("re-searching replaces the previous highlight (no stale marks)", () => {
    const el = root(`<p>foo bar foo baz</p>`);
    const eng = new FindEngine(el);
    eng.search("foo");
    expect(marks(el)).toHaveLength(2);
    eng.search("ba");
    expect(marks(el)).toHaveLength(2);
    expect(marks(el).map((m) => m.textContent)).toEqual(["ba", "ba"]);
  });
});

// ---------------------------------------------------------------------------
// FindEngine: visibility pruning
// ---------------------------------------------------------------------------

describe("FindEngine visibility", () => {
  it("skips text inside .hidden, [hidden], and aria-hidden subtrees", () => {
    const el = root(
      `<div class="message">visible TODO</div>` +
        `<div class="hidden">hidden TODO</div>` +
        `<div hidden>attr TODO</div>` +
        `<div aria-hidden="true">aria TODO</div>`,
    );
    const eng = new FindEngine(el);
    expect(eng.search("TODO")).toBe(1);
  });

  it("skips a closed <details> but searches an open one", () => {
    const closed = root(`<details><summary>Reasoning</summary><p>TODO inside</p></details>`);
    expect(new FindEngine(closed).search("TODO")).toBe(0);

    const open = root(`<details open><summary>Reasoning</summary><p>TODO inside</p></details>`);
    expect(new FindEngine(open).search("TODO")).toBe(1);
  });

  it("skips the live-streaming bubble", () => {
    const el = root(
      `<div class="message assistant streaming">streaming TODO</div>` +
        `<div class="message assistant">settled TODO</div>`,
    );
    expect(new FindEngine(el).search("TODO")).toBe(1);
  });

  it("skips script and style content", () => {
    const el = root(`<p>real TODO</p><script>var TODO=1;</script><style>.TODO{}</style>`);
    expect(new FindEngine(el).search("TODO")).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// FindEngine: stepping
// ---------------------------------------------------------------------------

describe("FindEngine stepping", () => {
  function threeHits(): { el: HTMLElement; eng: FindEngine } {
    const el = root(`<p>hit hit hit</p>`);
    const eng = new FindEngine(el);
    eng.search("hit");
    return { el, eng };
  }

  it("next() advances and wraps around", () => {
    const { eng } = threeHits();
    expect(eng.currentIndex).toBe(0);
    eng.next();
    expect(eng.currentIndex).toBe(1);
    eng.next();
    expect(eng.currentIndex).toBe(2);
    eng.next();
    expect(eng.currentIndex).toBe(0);
  });

  it("prev() retreats and wraps around", () => {
    const { eng } = threeHits();
    eng.prev();
    expect(eng.currentIndex).toBe(2);
    eng.prev();
    expect(eng.currentIndex).toBe(1);
  });

  it("moves the current class to the active mark", () => {
    const { el, eng } = threeHits();
    eng.next();
    const ms = marks(el);
    expect(ms[0]?.classList.contains("find-hit-current")).toBe(false);
    expect(ms[1]?.classList.contains("find-hit-current")).toBe(true);
    expect(eng.currentMark()).toBe(ms[1]);
  });

  it("next()/prev() are no-ops with zero matches", () => {
    const el = root(`<p>nada</p>`);
    const eng = new FindEngine(el);
    eng.search("xyz");
    eng.next();
    eng.prev();
    expect(eng.currentIndex).toBe(-1);
  });

  it("setCurrent() clamps to the valid range", () => {
    const { eng } = threeHits();
    eng.setCurrent(2);
    expect(eng.currentIndex).toBe(2);
    eng.setCurrent(99); // out of range -> ignored
    expect(eng.currentIndex).toBe(2);
    eng.setCurrent(-5); // out of range -> ignored
    expect(eng.currentIndex).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// Ctrl-F overlay integration
// ---------------------------------------------------------------------------

describe("Ctrl-F overlay", () => {
  // The overlay controller keeps module-level singleton state (the built
  // overlay, the popup). beforeEach wipes the DOM, so we re-import a fresh
  // module graph each test to avoid reusing a now-detached overlay. The
  // ./scroll.js mock persists across resetModules.
  let onHotkey: (e: KeyboardEvent) => void;
  let toggle: () => void;
  let close: () => void;
  let isOpenFn: () => boolean;
  /** The tab-change emitter from THIS test's module graph. `vi.resetModules()`
   *  gives find-in-chat.js a fresh bus, so a top-level import here would write
   *  to a different instance than the one it subscribed to. */
  let switchTab: () => void;

  beforeEach(async () => {
    vi.resetModules();
    bootSeq++;
    document.body.innerHTML = `
      <div id="chat-view" data-tab-view>
        <button type="button" id="find-btn" class="icon-btn" aria-pressed="false"></button>
        <div id="messages-wrap-outer">
          <div id="messages-wrap">
            <div id="messages">
              <div class="message assistant">first TODO here</div>
              <div class="message assistant">second TODO and TODO again</div>
            </div>
          </div>
        </div>
        <textarea id="prompt-input"></textarea>
      </div>
      <div id="shell-panel" class="hidden"><textarea id="term-input"></textarea></div>`;
    const mod = (await import(
      /* @vite-ignore */ `./find-in-chat.ts?boot=${bootSeq}`
    )) as typeof ModFindInChat;
    onHotkey = mod.handleFindHotkey;
    toggle = mod.toggleChatFind;
    close = mod.closeChatFind;
    isOpenFn = mod._isChatFindOpen;
    const bus = await import("./bus.js");
    switchTab = (): void => {
      bus.emitBus(bus.BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
    };
  });

  /** The box is revealed through the popup primitive's `[hidden]` attribute plus
   *  the `is-open` state class, NOT the `.hidden` utility. That swap is the whole
   *  point of item 6: `.hidden` is `display: none !important` (40-a11y.css) and
   *  `display` is discrete, so the close could never animate. Asserting on
   *  `is-open` also reads the state SYNCHRONOUSLY, where `[hidden]` lands only
   *  after the leave transition settles. */
  function boxIsOpen(): boolean {
    return document.getElementById("chat-find")?.classList.contains("is-open") === true;
  }

  function findBtn(): HTMLElement | null {
    return document.getElementById("find-btn");
  }

  function ctrlF(): KeyboardEvent {
    return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
  }

  function input(): HTMLInputElement | null {
    return document.getElementById("chat-find-input") as HTMLInputElement | null;
  }

  function typeAndEnter(value: string, shift = false): void {
    const el = input();
    if (el === null) {
      throw new Error("find input not built");
    }
    el.value = value;
    el.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        shiftKey: shift,
        bubbles: true,
        cancelable: true,
      }),
    );
  }

  it("takes no clicks while closed or fading, in the stylesheet", async () => {
    // A SOURCE fact, because the test page loads no app stylesheet and the pre-open
    // instant is not observable from outside `ensureBuilt`.
    //
    // The primitive writes `[hidden]` only at the END of a leave, so between
    // `is-leaving` and that moment the box is still in the layout — and this box
    // is position:absolute at z-index 60 over the transcript. A fully transparent
    // rectangle taking clicks meant for the messages under it is the worst
    // combination available, so the resting state disables pointer events and
    // `.is-open` restores them.
    const { loadCSS, ruleContaining } = await import("./__test-helpers__/css-rules.js");
    const css = loadCSS("24-find.css");
    // On `.search-pop`, the skin class this box shares with the four page search
    // popups: they are positioned over their own content and inherit the same
    // hazard, so the pair belongs to the shared layer rather than to this box.
    expect(ruleContaining(css, ".search-pop", "top").body).toMatch(/pointer-events:\s*none/);
    expect(ruleContaining(css, ".search-pop.is-open", "top").body).toMatch(
      /pointer-events:\s*auto/,
    );
  });

  it("opens on Ctrl-F when the chat view is active and preventDefaults the browser find", () => {
    const ev = ctrlF();
    onHotkey(ev);
    expect(ev.defaultPrevented).toBe(true);
    const overlay = document.getElementById("chat-find");
    expect(overlay).not.toBeNull();
    expect(boxIsOpen()).toBe(true);
    expect(overlay?.hidden).toBe(false);
    expect(document.activeElement).toBe(input());
  });

  it("does not hijack Ctrl-F when the chat view is hidden", () => {
    document.getElementById("chat-view")?.classList.add("hidden");
    const ev = ctrlF();
    onHotkey(ev);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("does not hijack Ctrl-F while focus is inside the shell panel", () => {
    (document.getElementById("term-input") as HTMLTextAreaElement).focus();
    const ev = ctrlF();
    onHotkey(ev);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("searches, updates the counter, steps with Enter, and closes on Escape", () => {
    onHotkey(ctrlF());
    const count = document.getElementById("chat-find-count");

    // Type "TODO" + Enter: lands on the first of three matches.
    typeAndEnter("TODO");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(3);
    expect(count?.textContent).toBe("1 of 3");

    // Enter again steps forward; Shift+Enter steps back.
    typeAndEnter("TODO");
    expect(count?.textContent).toBe("2 of 3");
    typeAndEnter("TODO", true);
    expect(count?.textContent).toBe("1 of 3");

    // Escape closes, clears highlights, and restores focus.
    const el = input();
    el?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    expect(boxIsOpen()).toBe(false);
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);
  });

  it("re-runs the search when the match-case toggle flips, without retyping", () => {
    // step() decides whether to re-search by comparing the query STRING, and
    // the toggle changes neither the string nor the input event — so the toggle
    // has to force the search itself or nothing at all would happen.
    onHotkey(ctrlF());
    const count = document.getElementById("chat-find-count");
    const toggle = document.querySelector<HTMLButtonElement>(".chat-find-case");
    expect(toggle?.getAttribute("aria-pressed")).toBe("false");

    typeAndEnter("todo");
    expect(count?.textContent).toBe("1 of 3");

    toggle?.click();
    expect(toggle?.getAttribute("aria-pressed")).toBe("true");
    // The transcript says "TODO" three times and "todo" never.
    expect(count?.textContent).toBe("No matches");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    toggle?.click();
    expect(toggle?.getAttribute("aria-pressed")).toBe("false");
    expect(count?.textContent).toBe("1 of 3");
  });

  it("resets to the first match on a toggle rather than keeping a position in the old set", () => {
    onHotkey(ctrlF());
    const count = document.getElementById("chat-find-count");
    typeAndEnter("TODO");
    typeAndEnter("TODO"); // step to 2 of 3
    expect(count?.textContent).toBe("2 of 3");
    document.querySelector<HTMLButtonElement>(".chat-find-case")?.click();
    // The match set changed, so a position in the previous one means nothing.
    expect(count?.textContent).toBe("1 of 3");
  });

  it("carries an accessible name on the toggle", () => {
    onHotkey(ctrlF());
    const toggle = document.querySelector<HTMLButtonElement>(".chat-find-case");
    expect(toggle?.getAttribute("aria-label")).toBe("Match case");
  });

  it("lets a second Ctrl-F fall through to the browser (escape hatch) while the field is focused", () => {
    onHotkey(ctrlF()); // open, input focused
    expect(document.activeElement).toBe(input());
    const second = ctrlF();
    onHotkey(second);
    expect(second.defaultPrevented).toBe(false);
  });

  // -------------------------------------------------------------------------
  // The close, on every path. Items 1, 2 and 5.
  //
  // The teardown is the part that matters and the part that was missing: marks
  // left in the transcript are welded there for the rest of the session, and a
  // skipped fold reset permanently rearranges a transcript as a side effect of
  // having searched it. So every path asserts the SAME three things — the box is
  // closed, no marks survive, and the trigger stops claiming pressed.
  // -------------------------------------------------------------------------

  /** Open, type a query that matches, and confirm the state a teardown has to
   *  undo actually exists. Without this precondition a teardown assertion could
   *  pass over a box that never highlighted anything. */
  function openWithMatches(): void {
    onHotkey(ctrlF());
    typeAndEnter("TODO");
    expect(document.querySelectorAll("mark.find-hit").length).toBeGreaterThan(0);
    expect(isOpenFn()).toBe(true);
  }

  function expectFullyTornDown(path: string): void {
    expect(isOpenFn(), `${path}: the box should be closed`).toBe(false);
    expect(boxIsOpen(), `${path}: is-open should be gone`).toBe(false);
    expect(
      document.querySelectorAll("mark.find-hit"),
      `${path}: every <mark> must be unwrapped, or highlights stay welded into the transcript`,
    ).toHaveLength(0);
    expect(
      findBtn()?.getAttribute("aria-pressed"),
      `${path}: the trigger must stop announcing itself pressed`,
    ).toBe("false");
  }

  it("closes on a click ANYWHERE outside the box, with the full teardown", () => {
    openWithMatches();
    // The primitive installs its outside-click listener one tick after the open,
    // so the click that opened a popup cannot immediately close it.
    return new Promise<void>((resolve) => {
      setTimeout(() => {
        document
          .getElementById("messages")
          ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
        expectFullyTornDown("outside click");
        resolve();
      }, 0);
    });
  });

  it("closes on Escape pressed OUTSIDE the field, not only inside it", () => {
    // Escape used to be bound to the find INPUT, so clicking into the transcript
    // to read a match made the key stop working — the box had no way out but the
    // mouse. It is a document-level listener now (the popup primitive's).
    openWithMatches();
    const transcript = document.getElementById("messages");
    return new Promise<void>((resolve) => {
      setTimeout(() => {
        transcript?.dispatchEvent(
          new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
        );
        expectFullyTornDown("document Escape");
        resolve();
      }, 0);
    });
  });

  it("closes on a TAB SWITCH rather than being hidden with its state intact", () => {
    // The defect this replaces: a tab switch hid the box by hiding its ancestor
    // view, leaving the open flag true, the MutationObserver connected, the marks
    // in the DOM and the search-opened folds open — so returning to the chat
    // re-revealed a search mid-flight.
    openWithMatches();
    switchTab();
    expectFullyTornDown("tab switch");
  });

  it("FORGETS the query on a tab switch, so the next tab's find opens empty", () => {
    // Closing alone was not enough. The box kept its text, and the open path runs
    // the search — so the next chat's find opened holding the previous chat's
    // query and immediately searched a transcript that query was never typed
    // against. Reported as the search state being global rather than per tab.
    openWithMatches();
    expect(input()?.value).toBe("TODO");
    switchTab();
    expect(input()?.value, "a retained query is inherited by whatever tab is opened next").toBe("");
  });

  it("KEEPS the query across an ordinary close, the way the browser's find does", () => {
    // The split is deliberate: only a tab switch is a change of subject. Reopening
    // on the same chat should still remember what you were looking for.
    openWithMatches();
    close();
    expect(input()?.value).toBe("TODO");
  });

  it("closes on the toolbar toggle, and a second toggle re-opens", () => {
    // The trigger was not a toggle at all: it called the OPEN path, so a second
    // click re-focused, re-selected and re-ran the search of an already-open box.
    openWithMatches();
    toggle();
    expectFullyTornDown("trigger toggle");
    toggle();
    expect(isOpenFn()).toBe(true);
    expect(findBtn()?.getAttribute("aria-pressed")).toBe("true");
  });

  it("announces the open state on the trigger with aria-pressed", () => {
    // aria-pressed, not `.active`: find is a toggle, while `.active` in this app
    // means "this singleton tab is active" (tabs.ts syncSidebarButtons owns it).
    // 70-selection.css already styles `.icon-btn[aria-pressed="true"]`, so this
    // is the announced state AND the visual with no new rule.
    expect(findBtn()?.getAttribute("aria-pressed")).toBe("false");
    onHotkey(ctrlF());
    expect(findBtn()?.getAttribute("aria-pressed")).toBe("true");
    close();
    expect(findBtn()?.getAttribute("aria-pressed")).toBe("false");
  });

  it("re-folds the turns the search opened, on every close path", async () => {
    // The OTHER half of the teardown, and the one the DOM cannot show: the server
    // pre-pass opens folded turns so the walker can see their hits, and a close
    // that skipped the reset would leave a transcript permanently rearranged as a
    // side effect of having been searched. `getActiveId()` is "" in this fixture,
    // so the reset early-returns and cannot be observed through the DOM — the call
    // itself is the assertion.
    const chatSearch = await import("./chat-search.js");
    const reset = vi.mocked(chatSearch.resetServerSearch);
    for (const path of ["escape", "toggle", "tab-switch"] as const) {
      reset.mockClear();
      onHotkey(ctrlF());
      expect(isOpenFn(), path).toBe(true);
      if (path === "escape") {
        close();
      } else if (path === "toggle") {
        toggle();
      } else {
        switchTab();
      }
      expect(
        reset,
        `${path}: the fold reset must run, or search-opened turns stay open forever`,
      ).toHaveBeenCalledTimes(1);
    }
    reset.mockClear();
  });

  it("is idempotent: closing an already-closed box changes nothing", () => {
    close();
    expect(isOpenFn()).toBe(false);
    onHotkey(ctrlF());
    close();
    close();
    expectFullyTornDown("double close");
  });
});

// ---------------------------------------------------------------------------
// Server-hit navigation.
//
// When the DOM walker marked NOTHING and the server found the text anyway —
// every occurrence inside collapsed delegate bodies, on a non-resident page,
// or in markdown the renderer never paints — stepping navigates the server
// hits instead of going dead under a counter that says "N in chat". Each case
// asserts the navigated-to state AND that failure states something on the
// aria-live counter, never a silent no-op.
// ---------------------------------------------------------------------------

import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import type { Session } from "./types.js";
import type { Tally } from "./textsearch/copy.js";
import type { Hit, SearchResult } from "./wire/types.gen.js";
import type * as ModChatSearch from "./chat-search.js";
import type * as ModStore from "./store.js";
import type * as ModStoreLoad from "./store-load.js";
import type * as ModScroll from "./scroll.js";
import type * as ModRunView from "./run-view.js";
import type * as ModSubagentView from "./subagent-view.js";
import type * as ModBlocks from "./messages-blocks.js";

describe("server-hit navigation", () => {
  let onHotkey: (e: KeyboardEvent) => void;
  let openAt: typeof ModFindInChat.openChatFindAt;
  let closeFind: () => void;
  let chatSearch: typeof ModChatSearch;
  let store: typeof ModStore;
  let storeLoad: typeof ModStoreLoad;
  let scroll: typeof ModScroll;
  let runView: typeof ModRunView;
  let subagentView: typeof ModSubagentView;
  let blocks: typeof ModBlocks;
  /** The tab-change emitter from THIS test's module graph, for the cross-tab
   *  jump's return trip. A top-level import would write to a different bus
   *  instance than the freshly-imported module subscribed to. */
  let switchTab: () => void;

  beforeEach(async () => {
    vi.resetModules();
    bootSeq++;
    document.body.innerHTML = `
      <div id="chat-view" data-tab-view>
        <button type="button" id="find-btn" class="icon-btn" aria-pressed="false"></button>
        <div id="messages-wrap-outer">
          <div id="messages-wrap"><div id="messages"></div></div>
        </div>
        <textarea id="prompt-input"></textarea>
      </div>
      <div id="shell-panel" class="hidden"></div>`;
    const mod = (await import(
      /* @vite-ignore */ `./find-in-chat.ts?boot=${bootSeq}`
    )) as typeof ModFindInChat;
    onHotkey = mod.handleFindHotkey;
    openAt = mod.openChatFindAt;
    closeFind = mod.closeChatFind;
    chatSearch = await import("./chat-search.js");
    store = await import("./store.js");
    storeLoad = await import("./store-load.js");
    scroll = await import("./scroll.js");
    runView = await import("./run-view.js");
    subagentView = await import("./subagent-view.js");
    blocks = await import("./messages-blocks.js");
    // The renderer's map, stood in for by the fixtures' own stamps: each carries the
    // two coordinates `stampBlock` writes, read DOCUMENT-WIDE because the real map is
    // keyed by (message, index), not by the row. Per call: a reveal mounts DOM later.
    vi.mocked(blocks.blockElement).mockImplementation(
      (messageID, blockIndex) =>
        document.querySelector<HTMLElement>(
          `[data-block-msg="${messageID}"][data-block-index="${String(blockIndex)}"]`,
        ) ?? undefined,
    );
    const bus = await import("./bus.js");
    switchTab = (): void => {
      bus.emitBus(bus.BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
    };
  });

  function ctrlF(): KeyboardEvent {
    return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
  }

  function typeAndEnter(value: string): void {
    const input = document.getElementById("chat-find-input") as HTMLInputElement;
    input.value = value;
    input.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
    );
  }

  function countText(): string {
    return document.getElementById("chat-find-count")?.textContent ?? "";
  }

  function noteText(): string {
    return document.getElementById("chat-find-note")?.textContent ?? "";
  }

  function serverHit(over: Partial<Hit> = {}): Hit {
    return {
      message_id: "a1",
      turn_message_id: "u1",
      excerpt: "…retry…",
      role: "assistant",
      segment_kind: "content",
      turn: 1,
      offset: 0,
      segment_len: 24,
      ...over,
    };
  }

  /** Stage a chat the navigation can read: an ACTIVE id and a live session
   *  object (tests mutate it to simulate pagination). */
  function stageChat(messages: unknown[], hasMore = false): Session {
    const session = { id: "c1", messages, has_more: hasMore } as unknown as Session;
    vi.mocked(store.getActiveId).mockReturnValue("c1");
    vi.mocked(store.getActive).mockReturnValue(session);
    return session;
  }

  /** Arm the search half: the server answer as the ENVELOPE the overlay adopts
   *  whole, and an inert reveal (each test that needs a building reveal overrides
   *  it). `matched` defaults to the list's own length, the uncut answer; a case
   *  staging a cut sets it higher and names the messages it read, since those are
   *  the two figures the note renders. */
  function stageHits(hits: Hit[], tally: Partial<Tally> = {}): void {
    vi.mocked(chatSearch.runServerSearch).mockResolvedValue({
      matches: hits,
      scanned: 1,
      matched: hits.length,
      truncated: false,
      ...tally,
    });
    vi.mocked(chatSearch.revealHitTurn).mockResolvedValue(undefined);
  }

  /** A production-shaped turn card. `bodyHTML === null` builds a STUB:
   *  header only, no `.turn-body` — what pagination prepends and what the
   *  reveal has to build before anything inside it can be marked. */
  function mountTurnCard(turnID: string, bodyHTML: string | null): HTMLElement {
    const card = document.createElement("div");
    // `turn` as well as the key, because that is what production builds
    // (`buildTurn` makes `el("div", { className: "turn" })` and the outer
    // reconcile keys it by the turn's opening message id) and what `turnCardEl`
    // selects. Without it the two turn-level kinds resolve nothing here while
    // resolving correctly in the app.
    card.className = "turn";
    card.setAttribute("data-reconcile-key", turnID);
    card.innerHTML = `<div class="turn-header">turn</div>`;
    if (bodyHTML !== null) {
      const body = document.createElement("div");
      body.className = "turn-body";
      body.innerHTML = bodyHTML;
      card.appendChild(body);
    }
    document.getElementById("messages")?.appendChild(card);
    return card;
  }

  /** Wire a fixture delegate box through the REAL disclosure primitive, the
   *  way messages-blocks.ts does: collapsed, `aria-hidden` + `inert` on the
   *  body, opened by activating the header. */
  function wireSubagentBox(box: HTMLElement): void {
    const header = box.querySelector<HTMLElement>(".subagent-header");
    const body = box.querySelector<HTMLElement>(".subagent-body");
    if (header === null || body === null) {
      throw new Error("fixture box missing header/body");
    }
    createDisclosure(header, body, {
      open: false,
      onToggle: (open) => {
        box.classList.toggle("collapsed", !open);
      },
    });
  }

  /** Wire a fixture tool card through the REAL disclosure primitive, the way
   *  tool-card.ts wires it: the output region collapsed, so it carries the
   *  `aria-hidden` + `inert` the walker prunes until the chevron is activated. */
  function wireToolCard(card: HTMLElement): void {
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
    const details = card.querySelector<HTMLElement>(".tool-details");
    if (toggle === null || details === null) {
      throw new Error("fixture card missing toggle/details");
    }
    createDisclosure(toggle, details, { open: false });
  }

  /** One macrotask turn, which drains the microtask chain between the shell's
   *  `query` callback and its `render` — the hop that records the standing answer. */
  function settle(): Promise<void> {
    return new Promise((resolve) => {
      setTimeout(resolve, 0);
    });
  }

  /** Open, run one query, and wait for the answer to be ADOPTED. Unlike
   *  `openAndSearch` it waits on no counter text, so it serves a fixture whose
   *  resident marks make the two figures agree (which prints no "in chat" at all). */
  async function openAndAdopt(query: string): Promise<void> {
    onHotkey(ctrlF());
    typeAndEnter(query);
    await settle();
  }

  async function openAndSearch(query: string): Promise<void> {
    onHotkey(ctrlF());
    typeAndEnter(query);
    // The server answer landed and the counter carries its figure — beside the
    // marks when there are some, as the empty state when the walker has nothing —
    // which is the state navigation starts from.
    await vi.waitFor(() => {
      expect(countText()).toMatch(/in chat|matched, not shown here/);
    });
    // The counter is painted by the QUERY callback synchronously; the shell's
    // render — which records the navigable hits — lands a few microtask hops
    // later (the async query's promise adoption). One macrotask turn drains
    // them all before the test steps.
    await new Promise((resolve) => {
      setTimeout(resolve, 0);
    });
  }

  // The "steps into a collapsed delegate" case was here. Its subject is gone: the transcript
  // renders none of a delegate's output, so there is no collapsed body to step into and no
  // disclosure chain to open. A hit inside that output now has no DOM segment at all — the
  // same position a workflow step's hit is in — and `chat-search.ts` still COUNTS it,
  // because the server searches the chat file. Routing such a hit to the delegate's own tab
  // the way a step's goes to the run tab is the obvious follow-up and is deliberately not
  // done here.

  // The cut, on the two surfaces that report it: the counter carries the
  // whole-chat COUNT and the note carries the SENTENCE. The list is cut exactly
  // when `matched` exceeds it; no flag stands in for that comparison.

  it("reports a cut answer as the whole-chat count and the note's sentence", async () => {
    stageChat([{ id: "u1", role: "user", content: "q" }]);
    stageHits([serverHit()], { scanned: 24, matched: 347 });
    await openAndSearch("retry");
    // Nothing marked locally, so the counter is the empty state the whole-chat
    // count decides — matched, and not shown here — rather than a flat total.
    expect(countText()).toBe("347 matched, not shown here");
    expect(noteText()).toBe("1 of 347 matches shown; 24 messages scanned");
  });

  it("stays silent on an answer the list holds whole", async () => {
    // A sentence restating the counter is noise: with every occurrence in the
    // list, the counter already says everything the note could.
    stageChat([{ id: "u1", role: "user", content: "q" }]);
    stageHits([serverHit()], { scanned: 24 });
    await openAndSearch("retry");
    expect(countText()).toBe("1 matched, not shown here");
    expect(noteText()).toBe("");
  });

  it("does not read the scan's own reach as a cut", async () => {
    // `truncated` says the scan did not read everything; a cut is a different fact,
    // carried by `matched`. One list of one hit, matched once, is whole however far
    // the scan reached, so the note has no cut to report.
    stageChat([{ id: "u1", role: "user", content: "q" }]);
    stageHits([serverHit()], { truncated: true });
    await openAndSearch("retry");
    expect(noteText()).toBe("");
  });

  it("says the answer was cut, and unsays it when the next one is not", async () => {
    stageChat([{ id: "u1", role: "user", content: "q" }]);
    stageHits([serverHit()], { scanned: 24, matched: 347 });
    await openAndSearch("retry");
    expect(noteText()).toBe("1 of 347 matches shown; 24 messages scanned");

    stageHits([serverHit()]);
    typeAndEnter("retry budget");
    await vi.waitFor(() => {
      expect(noteText()).toBe("");
    });
  });

  it("clears the note when the query is refined to zero hits", async () => {
    // Refining is exactly the gesture a cut invites, so the sentence has to go
    // even on the path that renders nothing else — which is why the note is set
    // ABOVE render's early return rather than after it.
    stageChat([{ id: "u1", role: "user", content: "q" }]);
    stageHits([serverHit()], { scanned: 24, matched: 347 });
    await openAndSearch("retry");
    expect(noteText()).not.toBe("");

    stageHits([]);
    typeAndEnter("nothing matches this");
    await vi.waitFor(() => {
      expect(noteText()).toBe("");
    });
  });

  it("resolves the block a hit names when the mounted window starts above index 0", async () => {
    // The window holds 2..7 of eight blocks, which is what per-block residency makes
    // ordinary. A same-kind ORDINAL counted from index 0 in the STORE names the FIFTH
    // mounted trace for a hit in the third, and every consumer downstream then opens,
    // walks, marks and reports success on the wrong block.
    const traces = Array.from(
      { length: 8 },
      (_, i) => `paragraph ${String(i)} weighs the retry budget`,
    );
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: traces.map((t) => ({ type: "thinking", thinking: t })),
      },
    ]);
    const mounted = [2, 3, 4, 5, 6, 7];
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row"><div class="assistant-blocks">${mounted
        .map(
          (i) =>
            `<details class="reasoning-block msg-reasoning" data-block-msg="a1" data-block-index="${String(i)}">
               <summary class="reasoning-summary">Reasoning</summary>
               <blockquote class="reasoning-body">${traces[i] ?? ""}</blockquote>
             </details>`,
        )
        .join("")}</div></div>`,
    );
    const want = traces[4] ?? "";
    stageHits([
      serverHit({
        excerpt: want,
        segment_kind: "reasoning",
        block_index: 4,
        offset: want.indexOf("retry"),
        segment_len: want.length,
      }),
    ]);

    await openAndSearch("retry");
    // Every trace is a CLOSED <details>, so the walker marked nothing and stepping
    // navigates the server hit — which is the only path that resolves an element.
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    });
    const current = document.querySelector("mark.find-hit-current");
    expect(current?.closest("[data-block-index]")?.getAttribute("data-block-index")).toBe("4");
    // And the one it opened is the one it marked: no other trace was touched.
    expect(document.querySelectorAll("details.reasoning-block[open]")).toHaveLength(1);
  });

  it("declines an element the map still names after it left the document", async () => {
    // A registry can answer with a node the document no longer holds, where the subtree
    // query this replaced could not. `navigateToHit` exits on a disconnected target with
    // nothing selected and nothing said, so declining HERE is what makes the message-row
    // fallback happen at all.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: [{ type: "text", text: "see the retry backoff here" }],
      },
    ]);
    const row = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks" style="content-visibility: hidden">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">see the retry backoff here</div>
           </div>
         </div>
       </div>`,
    ).querySelector('[data-reconcile-key="a1"]') as HTMLElement;
    // What a stale entry looks like: the block's element, built and never inserted.
    const gone = document.createElement("div");
    gone.setAttribute("data-block-index", "0");
    gone.innerHTML = `<div class="message assistant">see the retry backoff here</div>`;
    vi.mocked(blocks.blockElement).mockReturnValue(gone);
    stageHits([
      serverHit({
        excerpt: "see the retry backoff here",
        block_index: 0,
        offset: 8,
        segment_len: 26,
      }),
    ]);
    await openAndSearch("retry");

    typeAndEnter("retry");

    // The hidden subtree is why no mark can be placed either way; what the fallback
    // buys is that the reader is told, and taken to the row they can act on.
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not in rendered text");
    });
    expect(row.classList.contains("find-target-flash")).toBe(true);
    expect(vi.mocked(scroll.jumpTo).mock.lastCall?.[0]).toBe(row);
  });

  it("counts the server's hits with the no-results skin absent when the DOM holds none", async () => {
    // The matches are all in blocks the window does not hold, so the walker can mark
    // nothing. Painting the box as a miss would contradict the count beside it and
    // read as data loss — the reason enumeration moved server-side at all.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: [
          { type: "text", text: "the retry backoff" },
          { type: "text", text: "retry again" },
          { type: "text", text: "and retry once more" },
        ],
      },
    ]);
    // A body holding the LAST block only: the two the hits name are unmounted.
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="2">
             <div class="message assistant">and once more</div>
           </div>
         </div>
       </div>`,
    );
    stageHits([
      serverHit({ excerpt: "the retry backoff", block_index: 0, offset: 4, segment_len: 17 }),
      serverHit({ excerpt: "retry again", block_index: 1, offset: 0, segment_len: 11 }),
    ]);

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);
    expect(countText()).toBe("2 matched, not shown here");
    expect(document.getElementById("chat-find")?.classList.contains("chat-find-no-results")).toBe(
      false,
    );
  });

  it("selects the block and says so for a syntax-only hit (match not in rendered text)", async () => {
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: [{ type: "text", text: "see [docs](https://retry.example) for more" }],
      },
    ]);
    mountTurnCard(
      "u1",
      // A TOP-LEVEL text block is stamped on its avatar row, not on the bubble, so
      // this shape is what the renderer registers and what the normalization has to
      // step through to reach the element the flash belongs on.
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">see docs for more</div>
           </div>
         </div>
       </div>`,
    );
    // The hit is the link TARGET: real markdown, never rendered as text.
    stageHits([
      serverHit({
        excerpt: "see [docs](https://retry.example) for more",
        block_index: 0,
        offset: 19,
        segment_len: 43,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    const bubble = document.querySelector(".message.assistant");
    await vi.waitFor(() => {
      expect(bubble?.classList.contains("find-target-flash")).toBe(true);
    });
    // Stated on the live-region counter, never a silent no-op.
    expect(countText()).toBe("1 of 1 \u00b7 not in rendered text");
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenLastCalledWith(bubble, expect.anything());
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);
  });

  it("walks again once the jump has rendered a skipped card, rather than reporting a miss", async () => {
    // The transcript's cards carry `content-visibility: auto` (css/14-tools.css),
    // so an OFF-SCREEN card holds no walkable text at all — the walker prunes at
    // `checkVisibility({contentVisibilityAuto: true})`. Stepping onto a hit inside
    // one therefore finds nothing on the first walk, and the thing that renders
    // the card is the navigation itself.
    //
    // Real containment and a real off-screen box: Chromium decides relevancy from
    // the viewport, which is what makes the first walk genuinely blind here.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: [{ type: "text", text: "see the retry backoff here" }],
      },
    ]);
    const wrap = document.getElementById("messages-wrap") as HTMLElement;
    wrap.style.cssText = "height: 400px; overflow-y: auto";
    const spacer = document.createElement("div");
    spacer.style.height = "6000px";
    document.getElementById("messages")?.appendChild(spacer);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row" style="content-visibility: auto; contain-intrinsic-size: auto 2.5rem">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">see the retry backoff here</div>
           </div>
         </div>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: "see the retry backoff here",
        block_index: 0,
        offset: 8,
        segment_len: 26,
      }),
    ]);
    // The premise, and it is the platform's answer rather than the harness's:
    // relevancy is decided in a rendering update, so it holds once the browser has
    // laid this fixture out.
    await vi.waitFor(() => {
      const blocks = document.querySelector<HTMLElement>(".assistant-blocks");
      expect(blocks?.checkVisibility({ contentVisibilityAuto: true })).toBe(false);
    });
    // The platform's half of the contract: a jump scrolls its target into view.
    vi.mocked(scroll.jumpTo).mockImplementation((el: Element) => {
      el.scrollIntoView();
    });

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".message.assistant mark.find-hit-current")).not.toBeNull();
    });
    // Not the miss notice: the text was there, it just had not rendered yet. The
    // position is the SESSION list's, which is the grammar a step through the
    // server's own list reads in; WHICH mark it landed on is asserted above.
    expect(countText()).toBe("1 of 1");
  });

  it("releases next/prev when the frame the re-walk waits for is never delivered", async () => {
    // A HIDDEN PAGE IS DELIVERED NO ANIMATION FRAMES. The re-walk above awaits two
    // `requestAnimationFrame` hops inside `stepServerHit`'s `navBusy` latch, so
    // backgrounding the tab between the jump and the re-walk left find's next/prev
    // inert until the tab came forward. A rAF that never calls back is that page.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: [{ type: "text", text: "see the retry backoff here" }],
      },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks" style="content-visibility: hidden">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">see the retry backoff here</div>
           </div>
         </div>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: "see the retry backoff here",
        block_index: 0,
        offset: 8,
        segment_len: 26,
      }),
    ]);
    await openAndSearch("retry");

    vi.stubGlobal("requestAnimationFrame", () => 0);
    typeAndEnter("retry");

    // The ceiling released the wait, and the verdict says which kind of miss it
    // is: nothing rendered, so the text may well be there. Without the ceiling
    // this promise never settles and the counter keeps its pre-navigation text;
    // with a ceiling that does not report how it settled, the notice reads
    // "not in rendered text" — a definite absence the walk cannot have observed.
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not rendered yet");
    });
  });

  it("steps into a tool card mounted in ANOTHER message's row", async () => {
    // Run-card hosting: `runCardFor` routes every later message's step blocks into the
    // FIRST message's card, so a2's tool card is mounted inside a1's row.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "tool_use", tool_call_id: "t-launch" }] },
      { id: "a2", role: "assistant", blocks: [{ type: "tool_use", tool_call_id: "t-step" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="run-card" data-block-msg="a1" data-block-index="0">
             <div class="run-steps">
               <div class="tool-call" data-tool-id="t-step" data-block-msg="a2" data-block-index="0">
                 <div class="tool-summary">
                   <span class="tool-title">Read notes</span>
                   <button type="button" class="tool-disclosure"></button>
                 </div>
                 <div class="tool-details">
                   <div class="tool-output">bump the retry backoff to 30s</div>
                 </div>
               </div>
             </div>
           </div>
         </div>
       </div>
       <div data-reconcile-key="a2" class="msg-row"><div class="assistant-blocks"></div></div>`,
    );
    const card = document.querySelector<HTMLElement>('[data-tool-id="t-step"]');
    wireToolCard(card as HTMLElement);
    stageHits([
      serverHit({
        message_id: "a2",
        excerpt: "bump the retry backoff to 30s",
        segment_kind: "tool_output",
        block_index: 0,
        offset: 9,
        segment_len: 29,
      }),
    ]);

    // The output is behind the card's own disclosure, so the walker marks nothing
    // and the counter carries the server's figure: navigation is the only path
    // that resolves an element.
    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(card?.querySelector("mark.find-hit-current")).not.toBeNull();
    });
    // The walk now reaches what the server matched, so the two figures agree and
    // the counter carries one.
    expect(countText()).toBe("1 of 1");
  });

  // ---------------------------------------------------------------------------
  // tool_diff: the card is the target, the mini-diff is the walk, and the notice
  // has THREE states rather than two.
  // ---------------------------------------------------------------------------

  /** The mini-diff `insertDiffPreview` builds, as the two fixtures below need it:
   *  one mounts it with the card and the other lands it after an open. */
  function previewHTML(row: string): string {
    return `<div class="tool-diff-preview">
              <div class="diff-pane tool-diff-mini"><div class="diff-row">${row}</div></div>
            </div>`;
  }

  /** A production-shaped `edit` card: the mini-diff sits BEFORE `.tool-details`
   *  (`insertDiffPreview` inserts it there), so it is in the card's resting state
   *  and needs no disclosure opened — and must not have one opened for it, which
   *  is what `wireDeferredDiff`'s request count pins. `previewRow === null` builds
   *  the majority case instead — a call whose diff exceeded the preview budget, so
   *  the card renders NO diff in that slot at all and LOADS one from the bulk when
   *  the card is opened. Since that is what find now does for such a hit, the two
   *  endings are its own: the diff arrives and the walk lands on it, or nothing
   *  arrives and the third notice state below says so. */
  function editCard(previewRow: string | null): string {
    const preview = previewRow === null ? "" : previewHTML(previewRow);
    return `<div class="tool-call" data-tool-id="t1" data-block-msg="a1" data-block-index="0">
              <div class="tool-summary">
                <span class="tool-title">Replace in File</span>
                <button type="button" class="tool-disclosure"></button>
              </div>
              ${preview}
              <div class="tool-details"><div class="tool-output">wrote fetch.go</div></div>
            </div>`;
  }

  /** Wire the DEFERRED half of a card the way `tool-card.ts` does: the first open
   *  requests the call's bulk and inserts the mini-diff when it lands, before
   *  `.tool-details`, exactly where `insertDiffPreview` puts it.
   *
   *  Stood in for here rather than mocked at the network, because this suite mounts
   *  no real card: the fetch a card issues on open is not otherwise observable, and
   *  "issues NO bulk" is a property two of these cases have to assert. `lands`
   *  chooses between the two endings — a bulk that answers a diff, and one that
   *  answers nothing (no chat id, a null bulk, a zero-change diff), which is what
   *  the wait's ceiling exists for. Async on a macrotask, like the real `then`, so
   *  the observer is already watching when it fires.
   *
   *  `into` lands the preview inside that element instead of as a DIRECT child of the
   *  card, which is the shape a nested insert added later would have: the resting-state
   *  guard and the observer's callback both ask the card a DESCENDANT question, so the
   *  observer has to watch the subtree or such an insert is invisible to it. */
  function wireDeferredDiff(
    card: HTMLElement,
    previewRow: string,
    lands: boolean,
    into?: HTMLElement,
  ): { requests: () => number } {
    let requests = 0;
    // PAST `RENDER_WAIT_CEILING_MS` (64ms), deliberately: `landOrNotice` already
    // recovers a first-walk miss by jumping and waiting one rendered frame, so a
    // diff landing inside that window is found whether or not anything awaited the
    // bulk — which would make the landing case below pass with the await deleted.
    // A real bulk is a round trip, so this is also the honest cadence.
    const bulkMs = 120;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
    if (toggle === null) {
      throw new Error("fixture card missing toggle");
    }
    toggle.addEventListener("click", () => {
      requests += 1;
      if (!lands || requests > 1) {
        return;
      }
      setTimeout(() => {
        const wrap = document.createElement("div");
        wrap.innerHTML = previewHTML(previewRow);
        const preview = wrap.firstElementChild;
        if (preview === null) {
          return;
        }
        if (into !== undefined) {
          into.appendChild(preview);
          return;
        }
        card.insertBefore(preview, card.querySelector(".tool-details"));
      }, bulkMs);
    });
    return { requests: () => requests };
  }

  /** Mount that card in the transcript and wire its own disclosure the way
   *  `tool-card.ts` does, so `.tool-details` carries the `aria-hidden` + `inert`
   *  the walker prunes. `rowStyle` is the one lever the diff cases need: the
   *  mini-diff sits OUTSIDE that region, so hiding it from the first walk takes a
   *  skipped-rendering ROW rather than a closed disclosure. */
  function mountEditCard(previewRow: string | null, rowStyle = ""): HTMLElement {
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row" style="${rowStyle}">
         <div class="assistant-blocks">${editCard(previewRow)}</div>
       </div>`,
    );
    const card = document.querySelector<HTMLElement>(".tool-call");
    if (card === null) {
      throw new Error("fixture card missing");
    }
    wireToolCard(card);
    return card;
  }

  /** The chat behind those fixtures: one tool block, which is what
   *  `resolveSegmentEl` reads before it consults the renderer's stamp map. */
  function stageEditChat(): void {
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "tool_use", tool_call_id: "t1" }] },
    ]);
  }

  const DIFF_ROW = "return retry(ctx, fetchOnce)";

  /** The elapsed budget the two "the ceiling was not paid" cases assert against.
   *
   *  `DEFERRED_DIFF_WAIT_MS` is 1500 and the fast path is the fixture's 120ms bulk plus
   *  one rendered frame plus this poll's interval, so any threshold in roughly
   *  (400, 1500) discriminates. 1200 sits near the top of that band DELIBERATELY: it
   *  still proves the ceiling was not paid — which is the whole assertion — while
   *  leaving ~1080ms of headroom for a loaded worker instead of ~680ms, at no cost to
   *  what it can detect. `testing-ts.md` records that a cold `npm test` under full load
   *  is where this repo's wall-clock assertions fail, and elapsed time is the only
   *  channel that can see these two defects (an existence assertion is green with
   *  `childList` alone, measured at 2284ms). */
  const CEILING_NOT_PAID_MS = 1200;

  it("lands a tool_diff hit on the rendered mini-diff row", async () => {
    // Real containment and a real off-screen box, the same shape the skipped-card
    // case above uses: the preview is in the card's RESTING state, so the only
    // thing that can hide it from the first walk is rendering the walker skips —
    // which is also the ordinary case, a card the reader has not scrolled to.
    stageEditChat();
    const wrap = document.getElementById("messages-wrap") as HTMLElement;
    wrap.style.cssText = "height: 400px; overflow-y: auto";
    const spacer = document.createElement("div");
    spacer.style.height = "6000px";
    document.getElementById("messages")?.appendChild(spacer);
    mountEditCard(DIFF_ROW, "content-visibility: auto; contain-intrinsic-size: auto 2.5rem");
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        block_index: 0,
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: DIFF_ROW.length,
      }),
    ]);
    await vi.waitFor(() => {
      const blocks = document.querySelector<HTMLElement>(".assistant-blocks");
      expect(blocks?.checkVisibility({ contentVisibilityAuto: true })).toBe(false);
    });
    vi.mocked(scroll.jumpTo).mockImplementation((el: Element) => {
      el.scrollIntoView();
    });

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector(".tool-diff-preview mark.find-hit-current")).not.toBeNull();
    });
    // A card whose diff is ALREADY on screen opens nothing: `insertDiffPreview`
    // inserts the mini-diff BEFORE `.tool-details`, so the card's own disclosure
    // stays shut.
    expect(document.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("false");
    expect(countText()).toBe("1 of 1");
  });

  it("opens a preview-less card and lands on the diff its bulk brings back", async () => {
    // The MAJORITY case — 4,625 of 7,285 diff-bearing calls exceed the preview
    // budget — and the new capability: the card renders no diff at rest, opening it
    // is what loads one, so find opens it and waits before deciding the text is not
    // there. The hit is inside the hunks the diff shows, so it becomes reachable.
    //
    // Red check: drop the `await arriving` in `navigateToHit` (keep the open) and
    // this reads "1 of 1 · only in this call's diff, which did not load" — the walk
    // ran before the bulk landed, which is exactly why widening
    // `OPENS_TOOL_DETAILS` alone would not have worked.
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, true);
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        block_index: 0,
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: DIFF_ROW.length,
      }),
    ]);

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector(".tool-diff-preview mark.find-hit-current")).not.toBeNull();
    });
    // The open is what made the diff exist, so it stays open — and it cost exactly
    // ONE bulk, for the one hit the reader stepped onto.
    expect(card.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("true");
    expect(bulk.requests()).toBe(1);
    expect(countText()).toBe("1 of 1");
  });

  it("says the match is not in the SHOWN hunks when the card renders a preview", async () => {
    // `windowHunks` keeps 24 rows, so a diff longer than that renders a preview
    // whose text does not carry the match. The text IS in the diff, so "not in
    // rendered text" would be wrong and "open the diff" is the action.
    stageEditChat();
    mountEditCard("return fetchOnce(ctx)");
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        block_index: 0,
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: 4096,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    const preview = document.querySelector(".tool-diff-preview");
    await vi.waitFor(() => {
      expect(preview?.classList.contains("find-target-flash")).toBe(true);
    });
    // The NARROWED region is what the flash and the jump land on, so the reader's
    // eye goes to the diff rather than to the whole card.
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenLastCalledWith(preview, expect.anything());
    expect(countText()).toBe("1 of 1 \u00b7 not in the shown hunks \u2014 open the diff");
  });

  it("opens no disclosure and requests no bulk when the card already renders its diff", async () => {
    // THE REGRESSION THIS CHANGE RISKED. A card whose diff is on screen is in the
    // state that has always been walked, so it must stay exactly there: nothing
    // opened, nothing fetched, no wait. Async by construction — the shown hunks do
    // not carry the match, so `landInPlace` finds no credible mark and the whole
    // pipeline runs, which is what makes these two assertions reachable at all.
    //
    // Red check: remove the resting-preview guard from `awaitDeferredDiff` and the
    // disclosure reads "true" with one bulk requested.
    stageEditChat();
    const card = mountEditCard("return fetchOnce(ctx)");
    const bulk = wireDeferredDiff(card, DIFF_ROW, true);
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        block_index: 0,
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: 4096,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not in the shown hunks \u2014 open the diff");
    });
    expect(card.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("false");
    expect(bulk.requests()).toBe(0);
    expect(card.querySelectorAll(".tool-diff-preview")).toHaveLength(1);
  });

  it("says the diff did not load when the open brings nothing back", async () => {
    // The third state, reworded: opening the card IS what find just did, so
    // "open it from the card" would be advice for a state the reader is not in.
    // Three ordinary endings arrive here — a card with no chat id requests nothing,
    // a null bulk applies nothing, a zero-change diff inserts nothing — and each
    // has to reach this sentence rather than hang the walk, which is what the
    // wait's ceiling is for.
    //
    // Red check: delete the ceiling from `waitForDiffPreview` and this case never
    // paints a notice at all (the walk is still waiting when the timeout fires).
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, false);
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        block_index: 0,
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: 4096,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(
      () => {
        expect(countText()).toBe("1 of 1 \u00b7 only in this call's diff, which did not load");
      },
      // Past the module's own ceiling, which this case is here to reach.
      { timeout: 4000 },
    );
    // It TRIED: the card is open and one bulk was asked for, which is what the
    // sentence reports as having failed.
    expect(card.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("true");
    expect(bulk.requests()).toBe(1);
    expect(card.querySelector(".tool-diff-preview")).toBeNull();
  });

  /** The two hits of the dead-diff fixture, so a second Enter is a second VISIT to
   *  the same card rather than a wrap onto the same hit — which is what makes the
   *  counter's own text distinguish the two landings. */
  function deadDiffHits(): Hit[] {
    return [0, 40].map((offset) =>
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        block_index: 0,
        offset,
        segment_len: 4096,
      }),
    );
  }

  it("answers a second visit to a dead diff hit AT ONCE rather than waiting again", async () => {
    // A hit whose bulk brings nothing back leaves the preview absent and the
    // disclosure present, and `detailsBody`'s builder has already run and will not run
    // again — so before the per-card mark every later visit re-entered and paid the
    // whole 1500ms ceiling for an answer nothing was going to send. `toolCallBulk`'s
    // memo cannot cover it: only the SUCCESS path is free.
    //
    // ELAPSED time is the assertion, not the notice text, which is the same either
    // way; the open count cannot separate them either, since an already-open card is
    // never clicked a second time.
    //
    // Red check: drop the `openBroughtNoDiff` arm from `awaitDeferredDiff`'s guard and
    // the second landing takes ~1550ms.
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, false);
    stageHits(deadDiffHits());

    await openAndSearch("retry");
    typeAndEnter("retry");
    await vi.waitFor(
      () => {
        expect(countText()).toBe("1 of 2 \u00b7 only in this call's diff, which did not load");
      },
      { timeout: 4000 },
    );

    const started = performance.now();
    typeAndEnter("retry");
    await vi.waitFor(
      () => {
        expect(countText()).toBe("2 of 2 \u00b7 only in this call's diff, which did not load");
      },
      { timeout: 4000 },
    );
    // Under the 1500ms ceiling: what is left is the walk's own rendered-frame wait
    // plus this poll's interval.
    expect(performance.now() - started).toBeLessThan(CEILING_NOT_PAID_MS);
    // And it re-opened nothing and re-fetched nothing on the way.
    expect(bulk.requests()).toBe(1);
  });

  it("paints NOTHING when the reader has retyped away under the wait", async () => {
    // The window a deferred diff opened: the landing is up to 1500ms after the press,
    // where before this change it was bounded by one rendered frame (64ms). So a
    // position or a miss notice for a hit the reader has typed away from could be
    // painted over a counter that already describes their new query.
    //
    // The retype is a direct write to the box, deliberately: `shell.value` IS "the
    // query the reader has", and dispatching an input event would start a debounced
    // re-run whose mocked answer re-adopts ownership mid-flight — which is the case
    // BELOW, with its own clause and its own red check.
    //
    // Red check: drop the `serverHitsQuery === query` term from `stepsOwnedList` and
    // the counter reads "1 of 1 · not in the shown hunks — open the diff" while the
    // preview carries the flash. Remove `landOrNotice`'s two ownership guards INSTEAD,
    // leaving the counter's gate intact, and the flash alone goes red — the silent
    // motion that guard exists to refuse, which the counter cannot see.
    //
    // THE BULK LANDS HERE, and that is what makes the negative observable: the diff's
    // ARRIVAL is what ends `waitForDiffPreview`, so waiting for the preview is waiting
    // for the landing to have resolved — where the flash cannot say it, being one of
    // the writes `landOrNotice`'s ownership guard withholds (a wait for it demands the
    // very term the red check says must be present). The dead-diff ending has its own
    // two cases above; this one needs an END to the wait, not a ceiling.
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, true);
    stageHits(deadDiffHits().slice(0, 1));

    await openAndSearch("retry");
    const before = countText();
    typeAndEnter("retry");
    const box = document.getElementById("chat-find-input") as HTMLInputElement;
    box.value = "budget";

    await vi.waitFor(() => {
      expect(card.querySelector(".tool-diff-preview")).not.toBeNull();
    });
    // One macrotask past the arrival, so the walk this press would have run has had
    // its turn: the wait resolves on a microtask off the observer's callback.
    await settle();
    // There WAS a landing to refuse: the card was opened and its bulk asked for.
    expect(bulk.requests()).toBe(1);
    // NOTHING — neither the counter nor the landing's own flash, anywhere.
    expect(countText()).toBe(before);
    expect(document.querySelectorAll(".find-target-flash")).toHaveLength(0);
  });

  it("paints NOTHING when a fresh answer has landed under the wait", async () => {
    // The other half of the same window, and the reason the gate is the whole
    // stepped-branch condition rather than ownership alone: the reader retypes AND the
    // new answer arrives, so the box and the standing answer agree again while the
    // cursor this press was walking belongs to the list that has just been replaced.
    // `render` resets the cursor to -1 for a fresh answer, which is exactly what
    // `updateCounter`'s stepped branch already refuses to print a position for.
    //
    // Red check: drop the `hitCursor >= 0` term from `stepsOwnedList` and this goes red
    // in `openAndSearch` before it reaches its own assertion — with that term gone the
    // stepped grammar prints for a cursor of -1, so the counter reads "0 of 1" where the
    // helper waits for the tally form. `landOrNotice`'s own guards are the sharper
    // check, as above: remove those two and the flash alone goes red. The bulk LANDS
    // here for the reason the case above states.
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, true);
    stageHits(deadDiffHits().slice(0, 1));

    await openAndSearch("retry");
    typeAndEnter("retry");
    // A real retype-and-search inside the wait: Enter on a changed box runs the query
    // rather than stepping, so the answer for the new text is adopted while the first
    // press is still waiting for its diff.
    typeAndEnter("budget");
    await settle();
    const afterRetype = countText();

    await vi.waitFor(() => {
      expect(card.querySelector(".tool-diff-preview")).not.toBeNull();
    });
    await settle();
    expect(bulk.requests()).toBe(1);
    expect(countText()).toBe(afterRetype);
    expect(document.querySelectorAll(".find-target-flash")).toHaveLength(0);
  });

  it("sees a diff the bulk inserts NESTED inside the card, on arrival", async () => {
    // The observer's scope has to match the question its own callback asks: both it and
    // `awaitDeferredDiff`'s resting-state guard read
    // `card.querySelector(".tool-diff-preview")`, a DESCENDANT query, so watching direct
    // children only makes a nested insert invisible to the subscription.
    //
    // ELAPSED time is the assertion, because the failure is SILENT: the walk still runs
    // when the ceiling fires and still finds a preview that landed inside it, so the
    // landing is merely 1500ms late rather than wrong — which is the worst failure shape
    // available here and the reason the option is preferred over pinning the
    // direct-child coincidence.
    //
    // Red check: put `waitForDiffPreview`'s observer back on `{ childList: true }` and
    // the mark appears after ~1550ms instead of the bulk's own 120ms.
    stageEditChat();
    const card = mountEditCard(null);
    const slot = document.createElement("div");
    slot.className = "tool-diff-slot";
    card.insertBefore(slot, card.querySelector(".tool-details"));
    wireDeferredDiff(card, DIFF_ROW, true, slot);
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        block_index: 0,
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: DIFF_ROW.length,
      }),
    ]);

    await openAndSearch("retry");
    const started = performance.now();
    typeAndEnter("retry");

    await vi.waitFor(
      () => {
        expect(document.querySelector(".tool-diff-preview mark.find-hit-current")).not.toBeNull();
      },
      { timeout: 4000 },
    );
    expect(performance.now() - started).toBeLessThan(CEILING_NOT_PAID_MS);
    expect(countText()).toBe("1 of 1");
  });

  it("says only NOT RENDERED YET for a diff miss with no frame delivered", async () => {
    // The precedence's first arm, and it outranks the kind: a diff on a hidden
    // tab is UNPAINTED rather than windowed out, so "open the diff" would send
    // the reader to a control for a problem they do not have. A rAF that never
    // calls back is that page.
    //
    // Red check: key the diff sentences on the preview alone (drop the
    // `!rendered` arm from `missNotice`) and this case reads "not in the shown
    // hunks" while the two cases above stay green.
    stageEditChat();
    mountEditCard("return fetchOnce(ctx)");
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        block_index: 0,
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: 4096,
      }),
    ]);
    await openAndSearch("retry");

    vi.stubGlobal("requestAnimationFrame", () => 0);
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not rendered yet");
    });
    expect(countText()).not.toContain("shown hunks");
    expect(countText()).not.toContain("this call's diff");
  });

  // ---------------------------------------------------------------------------
  // tool_input: the `<pre>` lives INSIDE `.tool-details`, so reaching it opens
  // the card, and the narrowing is what keeps the output's mark from winning.
  // ---------------------------------------------------------------------------

  /** A production-shaped card carrying both an input and an output. `detailsBody`
   *  inserts the input `<pre>` at the START of `.tool-details` and appends the
   *  output after it, so both regions are behind the card's own disclosure. */
  function inputCard(input: unknown, output: string): string {
    const pretty = JSON.stringify(input, null, 2);
    return `<div class="tool-call" data-tool-id="t1" data-block-msg="a1" data-block-index="0">
              <div class="tool-summary">
                <span class="tool-title">Run Command</span>
                <button type="button" class="tool-disclosure"></button>
              </div>
              <div class="tool-details">
                <pre class="tool-input">${pretty}</pre>
                <div class="tool-output">${output}</div>
              </div>
            </div>`;
  }

  function mountInputCard(input: unknown, output: string): HTMLElement {
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">${inputCard(input, output)}</div>
       </div>`,
    );
    const card = document.querySelector<HTMLElement>(".tool-call");
    if (card === null) {
      throw new Error("fixture card missing");
    }
    wireToolCard(card);
    return card;
  }

  it("opens the card's disclosure for a tool_input hit and lands on the <pre>", async () => {
    stageEditChat();
    mountInputCard({ command: "go test ./... -run Retry" }, "ok 1.013s");
    const leaves = "go test ./... -run Retry";
    stageHits([
      serverHit({
        excerpt: leaves,
        segment_kind: "tool_input",
        block_index: 0,
        offset: leaves.indexOf("Retry"),
        segment_len: leaves.length,
      }),
    ]);

    // Behind the closed disclosure the walker prunes the region, so the first
    // walk marks nothing and the counter carries the server's figure alone.
    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector(".tool-input mark.find-hit-current")).not.toBeNull();
    });
    expect(document.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("true");
    expect(countText()).toBe("1 of 1");
  });

  it("keeps a tool_input hit inside .tool-input when the output matches too", async () => {
    // TWO properties in one card, because one fixture carries both honestly. The
    // output ALSO matches, so the whole card holds two credible marks and the
    // narrowing is what decides between them. And the input is several SHORT
    // leaves — a path, a pattern, a flag — which is where token overlap between
    // the server's `\n`-joined excerpt and the rendered pretty-printed JSON is
    // thinnest, so it is the similarity floor's own case for this kind. A long
    // payload leaf would clear any floor and would not test the link.
    //
    // Red check: drop the `tool_input` row from `TOOL_TARGET` (so the walk takes
    // the whole card) and the winner moves to the `.tool-output` mark.
    stageEditChat();
    mountInputCard(
      { path: "internal/chat/retry.go", pattern: "retry", flag: "-n" },
      "internal/chat/retry.go:12",
    );
    const leaves = "internal/chat/retry.go retry -n";
    stageHits([
      serverHit({
        excerpt: leaves,
        segment_kind: "tool_input",
        block_index: 0,
        offset: leaves.indexOf("retry"),
        segment_len: leaves.length,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    });
    const current = document.querySelector<HTMLElement>("mark.find-hit-current");
    expect(current?.closest(".tool-input")).not.toBeNull();
    expect(current?.closest(".tool-output")).toBeNull();
    // A mark, not the "not in rendered text" notice: the counter reports the
    // position in the SERVER's list, which is the grammar every stepped landing
    // gets (the sibling case above reads the same). It used to read "1 of 3" — the
    // DOM grammar, over the three marks this card carries — and that was the
    // teardown speaking: find's own disclosure click bubbled to `document`, where
    // the popup's outside-dismissal closed the overlay and reset the stepped state,
    // so the final paint fell through to the DOM branch. `activateQuietly` keeps
    // the click off the document, so the stepped state survives its own landing.
    expect(countText()).toBe("1 of 1");
  });

  // --- The five remaining rendered fields ---
  //
  // The two TURN-LEVEL kinds resolve from the hit's own turn card, ahead of the row
  // lookup; `plan` keeps the row path and is narrowed to the plan card; `tool_denial`
  // is a `.tool-details` member like the input.

  /** Stage the chat both turn-level cases read: one turn, one assistant message,
   *  nothing about the message row that could answer for either kind. */
  function stageTurnLevelChat(): void {
    stageChat([
      { id: "u1", role: "user", content: "look at this" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "partial answer" }] },
    ]);
  }

  it("lands a turn_failure hit on the card-level .turn-notice", async () => {
    stageTurnLevelChat();
    const reason = "the retry budget ran out";
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks"><div class="message assistant">partial answer</div></div>
       </div>`,
    ).insertAdjacentHTML("beforeend", `<div class="turn-notice">${reason}</div>`);
    stageHits([
      serverHit({
        excerpt: reason,
        segment_kind: "turn_failure",
        offset: reason.indexOf("retry"),
        segment_len: reason.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".turn-notice mark.find-hit-current")).not.toBeNull();
    });
    // No block index, so `resolveSegmentEl` could never have answered: the card
    // came from `turn_message_id` alone.
    expect(document.querySelector("mark.find-hit-current")?.textContent).toBe("retry");
  });

  it("lands an attachment hit on a pill in the turn header", async () => {
    stageTurnLevelChat();
    const name = "retry-notes.md";
    // The production shape: the pills are `header > .turn-req > .turn-req-attachments`,
    // so the resolver's `:scope > .turn-header .turn-req-attachments` has to reach
    // through `.turn-req` rather than expecting a direct child.
    mountTurnCard("u1", `<div data-reconcile-key="a1" class="msg-row"></div>`)
      .querySelector<HTMLElement>(".turn-header")
      ?.insertAdjacentHTML(
        "beforeend",
        `<div class="turn-req">
           <div class="turn-req-text">look at this</div>
           <ul class="turn-req-attachments attachment-row">
             <li class="attachment-pill"><span class="attachment-name">${name}</span></li>
           </ul>
         </div>`,
      );
    stageHits([
      serverHit({
        excerpt: name,
        segment_kind: "attachment",
        offset: 0,
        segment_len: name.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".turn-req-attachments mark.find-hit-current")).not.toBeNull();
    });
  });

  it("resolves a turn-level hit on a STUB turn, which has no row at all", async () => {
    // The case the PLACEMENT is for: a tier-3 stub carries a header and a notice
    // and NO `.turn-body`, so the row lookup answers null. With that lookup ahead
    // of the turn-level arm this reads "could not be shown" on exactly the turns
    // where the notice is the whole rendered content.
    //
    // Red check: move `const row = messageRowEl(...)` and its null return back
    // above the turn-level arm and this fails with that sentence.
    stageTurnLevelChat();
    const reason = "the retry budget ran out";
    mountTurnCard("u1", null).insertAdjacentHTML(
      "beforeend",
      `<div class="turn-notice">${reason}</div>`,
    );
    expect(document.querySelector(".turn-body")).toBeNull();
    stageHits([
      serverHit({
        excerpt: reason,
        segment_kind: "turn_failure",
        offset: reason.indexOf("retry"),
        segment_len: reason.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".turn-notice mark.find-hit-current")).not.toBeNull();
    });
  });

  it("selects the turn card and says so when the reason is not rendered", async () => {
    // A CANCELLED turn: `turnFailureText` renders nothing at all for that
    // severity, so the reason is in the record and on no surface. The hit is
    // still counted and still navigable — it selects the TURN CARD and reports
    // what happened rather than claiming the text does not exist.
    stageTurnLevelChat();
    const reason = "the retry budget ran out";
    const card = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks"><div class="message assistant">partial answer</div></div>
       </div>`,
    );
    expect(card.querySelector(".turn-notice")).toBeNull();
    stageHits([
      serverHit({
        excerpt: reason,
        segment_kind: "turn_failure",
        offset: reason.indexOf("retry"),
        segment_len: reason.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not in rendered text");
    });
    expect(card.classList.contains("find-target-flash")).toBe(true);
    expect(document.querySelector("mark.find-hit-current")).toBeNull();
  });

  it("lands a plan hit on the plan card inside the message row", async () => {
    // Red check: move the narrowing into `resolveSegmentEl` (which opens with
    // `if (hit.block_index === undefined) return null`, so a plan hit can never
    // reach an arm there) and the mark lands outside `.plan-message`.
    stageChat([
      { id: "u1", role: "user", content: "plan it" },
      {
        id: "a1",
        role: "assistant",
        blocks: [{ type: "text", text: "here is the plan" }],
        plan: [{ content: "Trace the retry path", status: "pending" }],
      },
    ]);
    const entry = "Trace the retry path";
    mountTurnCard(
      "u1",
      // The plan card is a DIRECT child of the row, which is what production
      // builds: `mountPlan` appends it to the message's own wrap. The prose
      // bubble beside it also holds the needle, so the narrowing is what decides
      // which of the two credible marks wins.
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="message assistant">the retry plan, in prose</div>
         </div>
         <div class="plan-message">
           <div class="plan-header">Plan</div>
           <div class="plan-entries"><div class="plan-entry">${entry}</div></div>
         </div>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: entry,
        segment_kind: "plan",
        offset: entry.indexOf("retry"),
        segment_len: entry.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".plan-message mark.find-hit-current")).not.toBeNull();
    });
    const current = document.querySelector<HTMLElement>("mark.find-hit-current");
    expect(current?.closest(".message")).toBeNull();
  });

  it("opens the card's disclosure for a tool_denial hit and lands on .tool-denial", async () => {
    // `detailsBody` BUILDS the denial block on the card's first open, exactly like
    // the input `<pre>`, so the kind is in `OPENS_TOOL_DETAILS` and the narrowing
    // has to run after that open rather than inside `resolveSegmentEl`.
    //
    // Red check: drop `tool_denial` from `OPENS_TOOL_DETAILS` and the region stays
    // pruned, so the walk misses and the notice replaces the mark.
    stageEditChat();
    const resource = "rm -rf /config/retry";
    mountInputCard({ command: resource }, "");
    const card = document.querySelector<HTMLElement>(".tool-call");
    card?.querySelector<HTMLElement>(".tool-details")?.insertAdjacentHTML(
      "afterbegin",
      `<div class="tool-denial">
         <span class="tool-denial-label">Resource</span>
         <span class="tool-denial-value">${resource}</span>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: resource,
        segment_kind: "tool_denial",
        block_index: 0,
        offset: resource.indexOf("retry"),
        segment_len: resource.length,
      }),
    ]);

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector(".tool-denial mark.find-hit-current")).not.toBeNull();
    });
    expect(document.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("true");
  });

  it("navigates a message hit to the message container (scroll + brief highlight)", async () => {
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "tool_use", tool_call_id: "t1" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">tool card furniture</div>
       </div>`,
    );
    // The filter-only contract: one synthetic hit locating the MESSAGE —
    // offset 0, zero segment length, no block index. Container navigation is
    // what keeps the ranker's segment_len division unreachable for this kind.
    stageHits([
      serverHit({
        excerpt: "List files a.go b.go",
        segment_kind: "message",
        offset: 0,
        segment_len: 0,
      }),
    ]);

    await openAndSearch("role:assistant");
    typeAndEnter("role:assistant");

    const row = document.querySelector('[data-reconcile-key="a1"]');
    await vi.waitFor(() => {
      expect(row?.classList.contains("find-target-flash")).toBe(true);
    });
    expect(countText()).toBe("1 of 1");
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenLastCalledWith(row, expect.anything());
    // Nothing was marked: the hit names no span, so no mark could be honest.
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);
  });

  // The reasoning trace whose markdown the server matched is LONGER than what
  // the walker sees rendered, and the needle occurs twice. The excerpt is the
  // discriminator: its window surrounds the SECOND occurrence, so similarity
  // must pick mark #2 even though mark #1 comes first in document order.
  const TRACE =
    "Enable retry on the uploader so flaky links recover without operator " +
    "attention and keep the queue draining smoothly overnight. When the " +
    "budget empties the retry gives up and files an alert instead.";

  it("nearest-match picks the occurrence whose context matches the excerpt", async () => {
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "thinking", thinking: TRACE }] },
    ]);
    const card = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <details class="reasoning-block msg-reasoning" data-block-msg="a1" data-block-index="0">
             <summary class="reasoning-summary">Reasoning</summary>
             <blockquote class="reasoning-body"></blockquote>
           </details>
         </div>
       </div>`,
    );
    // Text content set programmatically so the fixture cannot drift from the
    // TRACE the hit's coordinates are computed against.
    const quote = card.querySelector(".reasoning-body");
    if (quote !== null) {
      quote.textContent = TRACE;
    }
    const secondAt = TRACE.lastIndexOf("retry");
    stageHits([
      serverHit({
        excerpt: "When the budget empties the retry gives up and files an alert instead.",
        segment_kind: "reasoning",
        block_index: 0,
        offset: secondAt,
        segment_len: TRACE.length,
      }),
    ]);

    await openAndSearch("retry");
    // The closed <details> hid the trace from the walker entirely.
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    });
    // The chain opened the reasoning disclosure…
    expect(document.querySelector<HTMLDetailsElement>("details.reasoning-block")?.open).toBe(true);
    // …and the SECOND occurrence is the one selected.
    const marks = [...document.querySelectorAll("mark.find-hit")];
    expect(marks[1]?.classList.contains("find-hit-current")).toBe(true);
    expect(marks[0]?.classList.contains("find-hit-current")).toBe(false);
    // The mark classes above are what pin the RANKING; the counter is reporting
    // the position in the session list the step is walking, which is one hit long.
    expect(countText()).toBe("1 of 1");
  });

  it("falls back to relative position when the excerpt cannot discriminate", async () => {
    // Both occurrences share one short context (the excerpt window covers the
    // whole trace), so similarity ties and offset/segment_len decides.
    const short = "retry then retry";
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "thinking", thinking: short }] },
    ]);
    const card = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <details class="reasoning-block msg-reasoning" data-block-msg="a1" data-block-index="0">
             <summary class="reasoning-summary">Reasoning</summary>
             <blockquote class="reasoning-body"></blockquote>
           </details>
         </div>
       </div>`,
    );
    const quote = card.querySelector(".reasoning-body");
    if (quote !== null) {
      quote.textContent = short;
    }
    stageHits([
      serverHit({
        excerpt: short,
        segment_kind: "reasoning",
        block_index: 0,
        offset: 11,
        segment_len: short.length,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    });
    const marks = [...document.querySelectorAll("mark.find-hit")];
    expect(marks[1]?.classList.contains("find-hit-current")).toBe(true);
  });

  it("declines a mark below the similarity floor: block selection, stated", async () => {
    // A STALE hit: the trace re-rendered since the search answered, so the
    // needle still occurs but nothing around it matches the excerpt. Selecting
    // that mark would claim a precision the ranker does not have.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: [{ type: "thinking", thinking: "the retry lives here in this trace" }],
      },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <details class="reasoning-block msg-reasoning" data-block-msg="a1" data-block-index="0">
             <summary class="reasoning-summary">Reasoning</summary>
             <blockquote class="reasoning-body">the retry lives here in this trace</blockquote>
           </details>
         </div>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: "completely unrelated words sharing nothing with that trace",
        segment_kind: "reasoning",
        block_index: 0,
        offset: 4,
        segment_len: 34,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    const details = document.querySelector("details.reasoning-block");
    await vi.waitFor(() => {
      expect(details?.classList.contains("find-target-flash")).toBe(true);
    });
    expect(countText()).toBe("1 of 1 \u00b7 not in rendered text");
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenLastCalledWith(details, expect.anything());
  });

  it("pages older history in until the hit's message is resident", async () => {
    const session = stageChat([{ id: "u9", role: "user", content: "recent" }], true);
    stageHits([
      serverHit({
        excerpt: "the old answer",
        segment_kind: "message",
        offset: 0,
        segment_len: 0,
      }),
    ]);
    vi.mocked(storeLoad.loadMessages).mockImplementation((_chatID, _beforeID) => {
      session.messages = [
        { id: "u1", role: "user", content: "old" },
        { id: "a1", role: "assistant", content: "the old answer" },
        ...session.messages,
      ] as Session["messages"];
      session.has_more = false;
      return Promise.resolve(true);
    });
    // The paged-in turn arrives as a stub; the reveal is what mounts it.
    vi.mocked(chatSearch.revealHitTurn).mockImplementation(() => {
      if (document.querySelector('[data-reconcile-key="u1"]') === null) {
        mountTurnCard("u1", `<div data-reconcile-key="a1" class="msg-row">the old answer</div>`);
      }
      return Promise.resolve();
    });

    await openAndSearch("role:assistant");
    typeAndEnter("role:assistant");

    await vi.waitFor(() => {
      expect(
        document
          .querySelector('[data-reconcile-key="a1"]')
          ?.classList.contains("find-target-flash"),
      ).toBe(true);
    });
    // Paged from the resident window's edge, exactly once.
    expect(vi.mocked(storeLoad.loadMessages)).toHaveBeenCalledExactlyOnceWith("c1", "u9");
    expect(countText()).toBe("1 of 1");
  });

  it("states it when the hit's page cannot be loaded", async () => {
    stageChat([{ id: "u9", role: "user", content: "recent" }], false);
    stageHits([serverHit({ segment_kind: "message", offset: 0, segment_len: 0 })]);

    await openAndSearch("role:assistant");
    typeAndEnter("role:assistant");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 could not be loaded");
    });
    expect(vi.mocked(storeLoad.loadMessages)).not.toHaveBeenCalled();
  });

  it("states it when the revealed turn still holds no row for the message", async () => {
    // The reveal resolved but the projection no longer holds the message (a
    // rewind, an eviction race): stepping must SAY so, not shrug.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [] },
    ]);
    mountTurnCard("u1", null);
    stageHits([serverHit({ segment_kind: "message", offset: 0, segment_len: 0 })]);

    await openAndSearch("role:assistant");
    typeAndEnter("role:assistant");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 could not be shown");
    });
  });

  // A WORKFLOW STEP's hit. Its blocks are DROPPED by the transcript's dispatcher
  // (messages-blocks.ts placeBlock), so there is no DOM segment for the chain above
  // to open and the old path ended at "could not be shown" on the launching turn's
  // row. The run TAB renders that step's transcript out of the same blocks, so it
  // is a real destination — the hit navigates there instead of resolving in place.
  it("routes a step hit to the run tab with that node focused", async () => {
    stageChat([
      { id: "u1", role: "user", content: "find it" },
      {
        id: "a1",
        role: "assistant",
        blocks: [
          { type: "text", text: "the step logged a retry", agent_subtask_id: "wf:wf-1:wf-1/build" },
        ],
      },
    ]);
    mountTurnCard(
      "u1",
      // The launching turn's row exists and holds NO step content: that is the
      // whole reason the DOM path has nothing to resolve here.
      `<div data-reconcile-key="a1" class="msg-row"><div class="assistant-blocks"></div></div>`,
    );
    stageHits([
      serverHit({
        excerpt: "the step logged a retry",
        segment_kind: "content",
        agent_subtask_id: "wf:wf-1:wf-1/build",
        block_index: 0,
        offset: 15,
        segment_len: 23,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(vi.mocked(runView.openRunView)).toHaveBeenCalledTimes(1);
    });
    // The run, the node, and this chat as the tab's parent. Name is "" so the tab
    // factory derives the label from the run store.
    expect(vi.mocked(runView.openRunView)).toHaveBeenCalledWith("wf-1", "", "c1", "wf-1/build");
    // No paging and no turn reveal: the destination is another tab, so opening the
    // launching chat's history would be work for a surface nobody is about to look
    // at. And nothing is selected in the transcript, because nothing is there.
    expect(vi.mocked(storeLoad.loadMessages)).not.toHaveBeenCalled();
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();
    expect(document.querySelector("mark.find-hit-current")).toBeNull();
    // The position AND the destination, painted before navigating: on a successful
    // open the tab switch tears the overlay down, so anything said afterwards is
    // said to nobody, and on a refused open this is the accurate position for a
    // reader still looking at the transcript. No boundary sentence — every hit in
    // this answer is cross-tab, so there is no phase 1 to cross out of.
    expect(countText()).toBe("1 of 1 \u00b7 opening the run tab");
  });

  // The predicate is the RENDERER's own `parseStepSubtask`, not a `wf:` prefix
  // test, so a malformed step id falls through to the delegate box exactly as it
  // does in the transcript.
  it("falls through to the DOM path for a malformed wf: id", async () => {
    stageChat([
      { id: "u1", role: "user", content: "find it" },
      {
        id: "a1",
        role: "assistant",
        blocks: [
          { type: "tool_use", tool_call_id: "t-sub", agent_subtask_id: "wf:no-second-colon" },
          {
            type: "text",
            text: "delegate found the retry backoff",
            agent_subtask_id: "wf:no-second-colon",
          },
        ],
      },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="subagent-block collapsed" data-subtask="wf:no-second-colon">
             <div class="subagent-header">Subagent</div>
             <div class="subagent-body"><div class="message assistant" data-block-msg="a1" data-block-index="1">delegate found the retry backoff</div></div>
           </div>
         </div>
       </div>`,
    );
    const box = document.querySelector<HTMLElement>(".subagent-block");
    if (box !== null) {
      wireSubagentBox(box);
    }
    stageHits([
      serverHit({
        excerpt: "delegate found the retry backoff",
        segment_kind: "content",
        agent_subtask_id: "wf:no-second-colon",
        block_index: 1,
        offset: 19,
        segment_len: 32,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    // A malformed id is NOT a step, so it must not reach the run tab — and it is a
    // DELEGATE, so it goes to that delegate's own page, which renders the blocks the
    // transcript drops. The subtask id is carried through verbatim.
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledWith(
        "c1",
        "wf:no-second-colon",
      );
    });
    expect(vi.mocked(runView.openRunView)).not.toHaveBeenCalled();
    expect(document.querySelector("mark.find-hit-current")).toBeNull();
  });

  it("sends an ordinary DELEGATE's hit to that delegate's page", async () => {
    // The counter reads the SERVER's figure, so a hit inside a delegate's output is
    // reported however little of it the transcript renders — which is none. Before the
    // delegate route this ended at "could not be shown" on the launching turn's row, the
    // same dead end the step branch above was written to close.
    stageChat([
      { id: "u1", role: "user", content: "find it" },
      {
        id: "a1",
        role: "assistant",
        blocks: [
          { type: "tool_use", tool_call_id: "t-sub", agent_subtask_id: "sub-9" },
          { type: "text", text: "delegate found the retry backoff", agent_subtask_id: "sub-9" },
        ],
      },
    ]);
    mountTurnCard("u1", `<div data-reconcile-key="a1" class="msg-row"></div>`);
    stageHits([
      serverHit({
        excerpt: "delegate found the retry backoff",
        segment_kind: "content",
        agent_subtask_id: "sub-9",
        block_index: 1,
        offset: 19,
        segment_len: 32,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledWith("c1", "sub-9");
    });
    // The transcript is not paged in or revealed for a destination in another tab.
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();
    expect(vi.mocked(runView.openRunView)).not.toHaveBeenCalled();
    expect(countText()).not.toContain("could not be");
  });

  // ---------------------------------------------------------------------------
  // The SPINE: an owned server answer is the step list, whatever the walker
  // marked. Each case below names the single-point mutation that must fail it.
  // ---------------------------------------------------------------------------

  it("walks the server's list even while resident marks exist for the same query", async () => {
    // The fixture the old gate was wrong on, kept: 2 DOM marks inside `a1` (from
    // `retry retry`) against 2 server hits, one of them in a message the transcript
    // has not mounted. `serverHits.length > engine.total` reads as `2 > 2` = false
    // here, which is why a count comparison could not have detected the divergence
    // and why the gate had to become ownership instead.
    //
    // Red check: restore the `engine.total === 0` conjunct in `step` — Enter then
    // cycles the two marks and the non-resident hit is never reached.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "retry retry" }] },
      { id: "a9", role: "assistant", blocks: [{ type: "text", text: "retry once more" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">retry retry</div>
           </div>
         </div>
       </div>`,
    );
    const resident = serverHit({
      excerpt: "retry retry",
      block_index: 0,
      offset: 0,
      segment_len: 11,
    });
    const elsewhere = serverHit({ message_id: "a9", turn_message_id: "u9" });
    stageHits([resident, elsewhere]);

    onHotkey(ctrlF());
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    });
    await settle();
    vi.mocked(chatSearch.revealHitTurn).mockClear();

    // The first press walks the SERVER's list and lands on the resident mark with
    // no reveal at all — the synchronous landing.
    typeAndEnter("retry");
    expect(countText()).toBe("1 of 2");
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();

    // The second reaches the hit the walker could never mark, which is the whole
    // point: it is REVEALED rather than skipped.
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(vi.mocked(chatSearch.revealHitTurn)).toHaveBeenCalledExactlyOnceWith("c1", elsewhere);
    });
    expect(countText()).toContain("2 of 2");
  });

  it("steps a resident hit synchronously, so held Enter presses are not dropped", async () => {
    // `navBusy` DROPS an Enter arriving while a navigation is in flight, so without
    // the synchronous landing a reader holding Enter would lose presses on hits
    // whose text is already on screen. Two presses in ONE task is the assertion the
    // async pipeline cannot satisfy.
    //
    // Red check: delete the `landInPlace` call from `stepServerHit` — the second
    // press is swallowed and the counter stays at 1.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "retry and retry" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">retry and retry</div>
           </div>
         </div>
       </div>`,
    );
    stageHits([
      serverHit({ excerpt: "retry and retry", block_index: 0, offset: 0, segment_len: 15 }),
      serverHit({ excerpt: "retry and retry", block_index: 0, offset: 10, segment_len: 15 }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");
    typeAndEnter("retry");
    expect(countText()).toBe("2 of 2");
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();
  });

  it("walks all 38 hits behind one resident mark without ever going dead", async () => {
    // The reported shape: one mark on screen, dozens of matches the server found.
    // The old gate spent every press re-selecting that one mark, so the counter
    // admitted 38 matches while Enter reached exactly one of them.
    //
    // Red check: restore the `engine.total === 0` conjunct — the counter never
    // leaves the DOM grammar and no hit past the first is visited.
    const HITS = 38;
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "retry" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">retry</div>
           </div>
         </div>
       </div>`,
    );
    // Every hit names the resident message and the resident block, which is what
    // lets each press land in place; their offsets differ, so they are 38 distinct
    // positions in the conversation rather than one hit counted 38 times.
    stageHits(
      Array.from({ length: HITS }, (_, i) =>
        serverHit({ excerpt: "retry", block_index: 0, offset: i, segment_len: HITS + 8 }),
      ),
    );

    await openAndAdopt("retry");
    const seen: string[] = [];
    for (let i = 0; i < HITS; i++) {
      typeAndEnter("retry");
      seen.push(countText());
    }
    expect(seen[0]).toBe(`1 of ${String(HITS)}`);
    expect(seen[HITS - 1]).toBe(`${String(HITS)} of ${String(HITS)}`);
    // Never a failure notice, on any of the 38 presses.
    expect(seen.filter((line) => line.includes("could not be"))).toEqual([]);
    // And the walk wraps rather than stopping at the end.
    typeAndEnter("retry");
    expect(countText()).toBe(`1 of ${String(HITS)}`);
  });

  it("steps the resident marks while the standing answer belongs to the previous query", async () => {
    // The in-flight window, which is on the PRIMARY path because a cut answer's note
    // invites the reader to refine the query: the shell's `query` callback runs the DOM pass
    // synchronously and RETURNS the fetch, so `engine.query === shell.value` holds
    // for the whole debounce-plus-round-trip while `serverHits` still belongs to the
    // text the reader has replaced.
    //
    // Red check: drop the `serverHitsQuery === shell.value` clause from `step` — the
    // stale hit is navigated and this chat's delegate page opens for a query the
    // reader has already abandoned.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: [
          { type: "text", text: "budget check" },
          { type: "text", text: "delegate retried", agent_subtask_id: "sub-9" },
        ],
      },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">budget check</div>
           </div>
         </div>
       </div>`,
    );
    // The standing answer's one hit lives in a delegate, so navigating it is
    // observable as a tab opening.
    stageHits([serverHit({ agent_subtask_id: "sub-9", block_index: 1, offset: 9 })]);
    await openAndSearch("retry");

    // The next fetch never resolves: this is the window, held open.
    vi.mocked(chatSearch.runServerSearch).mockReturnValue(new Promise(() => undefined));
    typeAndEnter("budget");
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(1);
    });

    typeAndEnter("budget");
    // The DOM mark is stepped, and the counter drops the session figure rather than
    // reporting the previous query's total for this one.
    expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    expect(countText()).toBe("1 of 1");
    // NOT the miss skin: "unknown" and "zero" are different states, and flashing it
    // on every keystroke would be a worse lie than a stale number.
    expect(document.getElementById("chat-find")?.classList.contains("chat-find-no-results")).toBe(
      false,
    );
    expect(vi.mocked(subagentView.openSubagentView)).not.toHaveBeenCalled();
  });

  it("visits hits in transcript order even when the only mark is in the last message", async () => {
    // Order is the server's, so where the marks happen to be cannot decide it. With
    // the DOM gate the walk started (and ended) at the one mark, which sits in the
    // NEWEST message — the reverse of how a reader reads a conversation.
    //
    // Red check: restore the `engine.total === 0` conjunct — nothing is revealed at
    // all, because the mark satisfies the old gate.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "first retry" }] },
      { id: "a2", role: "assistant", blocks: [{ type: "text", text: "second retry" }] },
      { id: "a3", role: "assistant", blocks: [{ type: "text", text: "third retry" }] },
    ]);
    // Only the LAST message's row carries walkable text.
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row"><div class="assistant-blocks"></div></div>
       <div data-reconcile-key="a2" class="msg-row"><div class="assistant-blocks"></div></div>
       <div data-reconcile-key="a3" class="msg-row">
         <div class="assistant-blocks"><div class="message assistant">third retry</div></div>
       </div>`,
    );
    stageHits([
      serverHit({ message_id: "a1", excerpt: "first retry" }),
      serverHit({ message_id: "a2", excerpt: "second retry" }),
      serverHit({ message_id: "a3", excerpt: "third retry" }),
    ]);

    await openAndSearch("retry");
    for (const id of ["a1", "a2", "a3"]) {
      typeAndEnter("retry");
      await vi.waitFor(() => {
        expect(vi.mocked(chatSearch.revealHitTurn).mock.lastCall?.[1].message_id).toBe(id);
      });
    }
    expect(countText()).toContain("3 of 3");
  });

  it("partitions the walk by destination, announcing the boundary once per answer", async () => {
    // A cross-tab step tears the overlay down (BUS_TAB_CHANGED -> closeChatFind), so
    // an interleaved walk would destroy the local one rather than merely interrupt
    // it. The partition is by `agent_subtask_id` — the client's own ROUTING — so a
    // hit EARLIER in the transcript is still visited second when it answers by
    // opening another view.
    //
    // Red check: make `buildStepOrder` return `hits` unpartitioned — the first Enter
    // opens the delegate's page.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: [{ type: "text", text: "delegate retried", agent_subtask_id: "sub-1" }],
      },
      { id: "a2", role: "assistant", blocks: [{ type: "text", text: "local retry" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row"><div class="assistant-blocks"></div></div>
       <div data-reconcile-key="a2" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a2" data-block-index="0">
             <div class="message assistant">local retry</div>
           </div>
         </div>
       </div>`,
    );
    // WIRE order puts the cross-tab hit first, which is where it sits in the
    // conversation; the walk must not.
    stageHits([
      serverHit({ message_id: "a1", agent_subtask_id: "sub-1", excerpt: "delegate retried" }),
      serverHit({ message_id: "a2", excerpt: "local retry", block_index: 0, offset: 6 }),
    ]);

    await openAndSearch("retry");

    typeAndEnter("retry");
    // Phase 1: answered in place, no tab opened, and the denominator is the whole
    // server list — both phases of it — rather than the one resident mark.
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 2");
    });
    expect(vi.mocked(subagentView.openSubagentView)).not.toHaveBeenCalled();

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledExactlyOnceWith(
        "c1",
        "sub-1",
      );
    });
    // The crossing states the rule, on the same live region, before the switch.
    expect(countText()).toBe(
      "2 of 2 \u00b7 the rest are in delegate pages and run tabs \u00b7 opening the delegate's page",
    );

    // Wrapping round says it once per ANSWER, not once per crossing.
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 2");
    });
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(countText()).toBe("2 of 2 \u00b7 opening the delegate's page");
    });
    // Waited on the OPEN rather than the counter alone, because the counter is
    // painted before the lazy import: a test ending in between leaves that import
    // to resolve inside the next one, where the call reads as a stray navigation.
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledTimes(2);
    });
  });

  it("puts a MOUNTED delegate invocation in phase 2, because the routing decides", async () => {
    // `placeBlock` draws exactly one of a delegate's blocks — its invocation, as the
    // card's header — so a `tool_title` hit on it is visible on screen with no reader
    // action. It is still phase 2: `navigateToHit` routes every non-empty subtask id
    // to another view, and the partition uses that same predicate rather than asking
    // what the transcript happens to have mounted.
    //
    // Red check: partition on whether `resolveSegmentEl` answers an element (a
    // "mounted means local" classification) — the invocation is then visited FIRST
    // and answers with a tab switch on the first press.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      {
        id: "a1",
        role: "assistant",
        blocks: [{ type: "tool_use", tool_call_id: "t-sub", agent_subtask_id: "sub-1" }],
      },
      { id: "a2", role: "assistant", blocks: [{ type: "text", text: "local retry" }] },
    ]);
    mountTurnCard(
      "u1",
      // The delegate's card IS in the transcript, stamped like any other block.
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="subagent-block" data-block-msg="a1" data-block-index="0">
             <div class="subagent-header"><span class="tool-title">Sub-agent: retry-sweeper</span></div>
           </div>
         </div>
       </div>
       <div data-reconcile-key="a2" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a2" data-block-index="0">
             <div class="message assistant">local retry</div>
           </div>
         </div>
       </div>`,
    );
    stageHits([
      serverHit({
        message_id: "a1",
        agent_subtask_id: "sub-1",
        segment_kind: "tool_title",
        excerpt: "Sub-agent: retry-sweeper",
        block_index: 0,
        offset: 11,
        segment_len: 24,
      }),
      serverHit({ message_id: "a2", excerpt: "local retry", block_index: 0, offset: 6 }),
    ]);

    // `openAndAdopt` rather than `openAndSearch`: both marks are resident here, so
    // the two figures agree and the counter prints no session figure to wait on.
    await openAndAdopt("retry");

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 2");
    });
    expect(vi.mocked(subagentView.openSubagentView)).not.toHaveBeenCalled();

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledExactlyOnceWith(
        "c1",
        "sub-1",
      );
    });
  });

  it("resumes the walk after a cross-tab jump, keeping the query", async () => {
    // The return trip. Today's subscriber closes the box AND clears the query, so a
    // jump was a dead end: the reader came back to an empty box and had to retype
    // and re-walk. The jump records BOTH resume values immediately before the lazy
    // import, because the switch tears the overlay down in the same turn.
    //
    // Red checks: clear `resumeKey` in `teardown` (the close runs before the
    // subscriber can spend it), or clear the input unconditionally in the
    // BUS_TAB_CHANGED subscriber — either way the walk restarts from the top.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "local retry" }] },
      {
        id: "a2",
        role: "assistant",
        blocks: [{ type: "text", text: "first delegate retry", agent_subtask_id: "sub-1" }],
      },
      {
        id: "a3",
        role: "assistant",
        blocks: [{ type: "text", text: "second delegate retry", agent_subtask_id: "sub-2" }],
      },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">local retry</div>
           </div>
         </div>
       </div>`,
    );
    stageHits([
      serverHit({ message_id: "a1", excerpt: "local retry", block_index: 0, offset: 6 }),
      serverHit({ message_id: "a2", agent_subtask_id: "sub-1", excerpt: "first delegate retry" }),
      serverHit({ message_id: "a3", agent_subtask_id: "sub-2", excerpt: "second delegate retry" }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry"); // phase 1
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 3");
    });
    typeAndEnter("retry"); // the first cross-tab hit
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledWith("c1", "sub-1");
    });

    // The tab switch this client caused: the box closes and the query STAYS, because
    // activating a delegate's page does not change the active chat.
    switchTab();
    const input = document.getElementById("chat-find-input") as HTMLInputElement;
    expect(input.value).toBe("retry");

    // Coming back re-runs the query, and the cursor is restored to the hit the
    // reader left on — so the next press CONTINUES rather than restarting.
    onHotkey(ctrlF());
    await settle();
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView).mock.lastCall).toEqual(["c1", "sub-2"]);
    });
  });

  // The handoff from the History page: its cross-chat search found the
  // conversation and counted its matches, and the row's click opens this box
  // carrying the query and stepped to the hit it named. Before it, the reader
  // landed in the chat with nothing marked and the count unreachable.

  it("opens on a handoff carrying the query, landed on the hit it names rather than the first", async () => {
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "retry and retry" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">retry and retry</div>
           </div>
         </div>
       </div>`,
    );
    const first = serverHit({
      excerpt: "retry and retry",
      block_index: 0,
      offset: 0,
      segment_len: 15,
    });
    const second = serverHit({
      excerpt: "retry and retry",
      block_index: 0,
      offset: 10,
      segment_len: 15,
    });
    stageHits([first, second]);

    openAt("retry", second);
    const input = document.getElementById("chat-find-input") as HTMLInputElement;
    expect(input.value).toBe("retry");
    // The counter is the stepped position IN the answer, at the named hit.
    await vi.waitFor(() => {
      expect(countText()).toBe("2 of 2");
    });
    const hits = [...document.querySelectorAll<HTMLElement>("mark.find-hit")];
    expect(hits).toHaveLength(2);
    expect(hits[1]?.classList.contains("find-hit-current")).toBe(true);
    expect(hits[0]?.classList.contains("find-hit-current")).toBe(false);
    // ONE scroll, to the hit: the open's own reveal of the first mark is withheld
    // while a landing is pending, or the reader watches two jumps. Counted rather
    // than matched against the first mark, because the answer's re-walk replaces
    // every mark element, so the one the open scrolled to is no longer in the DOM.
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenCalledWith(hits[1], expect.anything());
  });

  it("opens on the answer it has when the handoff's hit is not in it", async () => {
    // The chat moved on between the two searches (a rewind, a compaction), so the
    // named hit is nowhere in the fresh answer. Chasing it would page history in
    // for a message that is gone; the box opens as an ordinary open would.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "retry once" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">retry once</div>
           </div>
         </div>
       </div>`,
    );
    stageHits([serverHit({ excerpt: "retry once", block_index: 0, offset: 0, segment_len: 10 })]);

    openAt("retry", serverHit({ message_id: "a9", excerpt: "a retry that is gone", offset: 2 }));
    await settle();
    expect(countText()).toBe("1 of 1");
    const hits = [...document.querySelectorAll<HTMLElement>("mark.find-hit")];
    expect(hits[0]?.classList.contains("find-hit-current")).toBe(true);
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenCalledWith(hits[0], expect.anything());
    // Nothing was paged in or revealed for the missing message.
    expect(vi.mocked(storeLoad.loadMessages)).not.toHaveBeenCalled();
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();
  });

  it("does not carry a handoff's landing into a later open the reader makes", async () => {
    // Closed before its answer landed. The next Ctrl-F keeps what every close keeps
    // — the query, and the cursor restored to the hit — and jumps nowhere: the
    // landing belonged to the open that asked for it, and this one is the reader's.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "retry and retry" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">retry and retry</div>
           </div>
         </div>
       </div>`,
    );
    const first = serverHit({
      excerpt: "retry and retry",
      block_index: 0,
      offset: 0,
      segment_len: 15,
    });
    const second = serverHit({
      excerpt: "retry and retry",
      block_index: 0,
      offset: 10,
      segment_len: 15,
    });
    // An answer that never lands, for as long as the handoff's open is up.
    vi.mocked(chatSearch.runServerSearch).mockReturnValue(new Promise(() => undefined));
    openAt("retry", second);
    closeFind();

    stageHits([first, second]);
    onHotkey(ctrlF());
    await vi.waitFor(() => {
      expect(countText()).toBe("2 of 2");
    });
    const hits = [...document.querySelectorAll<HTMLElement>("mark.find-hit")];
    expect(hits[0]?.classList.contains("find-hit-current")).toBe(true);
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenCalledWith(hits[0], expect.anything());
    expect(vi.mocked(scroll.jumpTo)).not.toHaveBeenCalledWith(hits[1], expect.anything());
  });

  it("steps the marks when there is no server list to walk", async () => {
    // The DOM pass keeps being the WHOLE step list wherever no list is owned, and
    // this is the shape that reaches it in production: a mark the server never
    // matched. Transcript chrome and markdown-joined text (`**work**flow` renders as
    // one word) are marked by the walker and absent from the answer, so an owned
    // answer can legitimately be empty while marks are on screen — the same state a
    // failed FIRST fetch leaves, where nothing is owned at all.
    //
    // Red check: drop the `serverHits.length > 0` conjunct from `step` — Enter then
    // routes into an empty walk and goes dead on content the reader can see.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "workflow and workflow" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="message assistant">workflow and workflow</div>
         </div>
       </div>`,
    );
    stageHits([]);

    await openAndAdopt("workflow");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    expect(countText()).toBe("1 of 2");

    typeAndEnter("workflow");
    expect(countText()).toBe("2 of 2");
    expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();
  });

  it("keeps the standing answer whole when a later fetch fails", async () => {
    // Seven values stand or none does: the hits, their tally, the walk order, the
    // cursor, the cut note, the reveal and the OWNERSHIP. A transient failure must
    // not un-say something true about the answer the reader is looking at.
    //
    // Red check: return an empty envelope instead of `null` on the fetch's null arm
    // — the answer is replaced by nothing, every clause below goes red at once.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "retry once" }] },
      { id: "a2", role: "assistant", blocks: [{ type: "text", text: "retry twice" }] },
    ]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">retry once</div>
           </div>
         </div>
       </div>
       <div data-reconcile-key="a2" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a2" data-block-index="0">
             <div class="message assistant">retry twice</div>
           </div>
         </div>
       </div>`,
    );
    stageHits(
      [
        serverHit({ message_id: "a1", excerpt: "retry once", block_index: 0, offset: 0 }),
        serverHit({ message_id: "a2", excerpt: "retry twice", block_index: 0, offset: 0 }),
      ],
      { scanned: 3, matched: 3 },
    );
    await openAndAdopt("retry");
    expect(noteText()).toBe("2 of 3 matches shown; 3 messages scanned");

    // A forced re-run of the SAME query (the case toggle) whose fetch fails.
    vi.mocked(chatSearch.runServerSearch).mockResolvedValue(null);
    document.querySelector<HTMLElement>(".chat-find-case")?.click();
    await settle();

    expect(noteText()).toBe("2 of 3 matches shown; 3 messages scanned");
    expect(document.getElementById("chat-find")?.classList.contains("chat-find-no-results")).toBe(
      false,
    );
    // Still the SERVER's list, still owned by the text in the box, still cut against
    // the same whole-chat count.
    typeAndEnter("retry");
    expect(countText()).toBe("1 of 2 \u00b7 3 in chat");
    typeAndEnter("retry");
    expect(countText()).toBe("2 of 2 \u00b7 3 in chat");
  });

  it("clears the cut note when a failed fetch leaves an answer for older text", async () => {
    // The note describes ONE answer, so it may only be painted while that answer
    // belongs to the text in the box. A failed fetch leaves the previous answer
    // standing — deliberately — and the reader has meanwhile typed something else,
    // so the sentence would describe a query they have abandoned.
    //
    // Red check: drop the `owned ?` gate from render's `setNote` — the note
    // survives the query change.
    stageChat([{ id: "u1", role: "user", content: "q" }]);
    stageHits([serverHit()], { scanned: 24, matched: 347 });
    await openAndSearch("retry");
    expect(noteText()).not.toBe("");

    vi.mocked(chatSearch.runServerSearch).mockResolvedValue(null);
    typeAndEnter("budget");
    await vi.waitFor(() => {
      expect(noteText()).toBe("");
    });
    // The session FIGURE is gated on the same predicate and drops with it: the
    // standing total describes the earlier query, so reporting it here would put a
    // number beside text nothing has counted.
    expect(countText()).toBe("No matches");
    expect(document.getElementById("chat-find")?.classList.contains("chat-find-no-results")).toBe(
      false,
    );
  });

  it("withholds the no-results skin until an owned answer says the text is nowhere", async () => {
    // "Unknown" and "zero" are different states. The skin claims the text is not in
    // the conversation, which only the server can say, so it waits for the answer
    // rather than flashing on every keystroke of a query still in flight.
    //
    // Red check: drop the `owned` conjunct from `noResults` — the skin paints during
    // the in-flight window, over a query nothing has answered yet.
    stageChat([{ id: "u1", role: "user", content: "q" }]);
    const skinOn = (): boolean =>
      document.getElementById("chat-find")?.classList.contains("chat-find-no-results") === true;

    let answer: (result: SearchResult | null) => void = () => undefined;
    vi.mocked(chatSearch.runServerSearch).mockReturnValue(
      new Promise((resolve) => {
        answer = resolve;
      }),
    );

    onHotkey(ctrlF());
    typeAndEnter("nothing matches this");
    expect(countText()).toBe("No matches");
    expect(skinOn()).toBe(false);

    answer({ matches: [], scanned: 1, matched: 0, truncated: false });
    await vi.waitFor(() => {
      expect(skinOn()).toBe(true);
    });
  });

  it("does not step a mark that appeared after the answer was taken", async () => {
    // The accepted loss, pinned as BEHAVIOUR rather than left as an omission. The
    // standing answer is frozen for the life of one open at one query — the live
    // re-run (`scheduleRerun`) re-runs the DOM engine only and never re-issues the
    // server search — so text that arrives afterwards is highlighted, counted in the
    // LOCAL figure until the reader starts stepping, never steppable, and never in
    // the session figure. Re-opening the box or editing the query recovers it.
    //
    // Red check: restore the `engine.total === 0` conjunct in `step` — Enter then
    // walks the marks and DOES reach the new one, which is the behaviour this case
    // exists to say we do not have.
    stageChat([
      { id: "u1", role: "user", content: "q" },
      { id: "a1", role: "assistant", blocks: [{ type: "text", text: "retry" }] },
    ]);
    const card = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="assistant-blocks">
           <div class="msg-row" data-block-msg="a1" data-block-index="0">
             <div class="message assistant">retry</div>
           </div>
         </div>
       </div>`,
    );
    stageHits([serverHit({ excerpt: "retry", block_index: 0, offset: 0, segment_len: 5 })]);
    await openAndAdopt("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(1);

    // A turn SEALS after the answer was taken: the walker can mark it (find-engine
    // prunes `.streaming`, so an in-flight turn is invisible to both sides — this is
    // the population that is invisible to the SERVER's frozen answer alone).
    const sealed = document.createElement("div");
    sealed.className = "message assistant";
    sealed.textContent = "retry again";
    card.querySelector(".assistant-blocks")?.appendChild(sealed);

    // The live re-run, driven through the transcript observer the module registers.
    const rerun = vi.mocked(scroll.onTranscriptMutate).mock.calls[0]?.[0];
    expect(rerun).toBeTypeOf("function");
    rerun?.();
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    });
    // Counted in the DOM figure, and the session figure does not claim it: the
    // whole-chat count (1) sits below the marks (2), so no second figure is shown.
    expect(countText()).toBe("1 of 2");

    // Stepping walks the ANSWER, so the new mark is never visited however many
    // presses the reader makes.
    const first = document.querySelectorAll<HTMLElement>("mark.find-hit")[0];
    for (let i = 0; i < 3; i++) {
      typeAndEnter("retry");
      expect(countText()).toBe("1 of 1");
      expect(document.querySelector("mark.find-hit-current")).toBe(first);
    }
  });
});
