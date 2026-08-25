// ---------------------------------------------------------------------------
// Find-in-Chat tests.
//
//   1. formatCount — the aria-live counter string (empty / no-match / N of M).
//   2. FindEngine — match discovery, case-insensitivity, highlight/unwrap,
//      visibility pruning, and next/prev stepping with wraparound.
//   3. Ctrl-F overlay integration — hotkey open, eligibility, search, stepping,
//      close + focus restore, and the "second Ctrl-F -> native find" escape
//      hatch.
//
// ./scroll.js is mocked so importing find-in-chat.ts doesn't trigger the
// ScrollController's eager DOM init (which needs #messages / #scroll-bottom).
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./scroll.js", () => ({ jumpTo: vi.fn() }));
// Spy-wrapped rather than replaced: every export keeps its real implementation
// and becomes observable. `vi.spyOn(namespace, name)` cannot do this in a real
// browser — an ESM module namespace is not configurable, so the assignment
// throws — and this suite needs to see one call that the DOM cannot show.
vi.mock("./chat-search.js", { spy: true });

import { FindEngine, formatCount } from "./find-engine.js";
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
// formatCount
// ---------------------------------------------------------------------------

describe("formatCount", () => {
  it("is empty for an empty query", () => {
    expect(formatCount(0, -1, "")).toBe("");
    expect(formatCount(5, 2, "")).toBe("");
  });

  it("reports no matches for a non-empty query with zero hits", () => {
    expect(formatCount(0, -1, "zzz")).toBe("No matches");
  });

  it("is 1-based for humans", () => {
    expect(formatCount(3, 0, "x")).toBe("1 of 3");
    expect(formatCount(3, 2, "x")).toBe("3 of 3");
    expect(formatCount(12, 6, "x")).toBe("7 of 12");
  });
});

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
