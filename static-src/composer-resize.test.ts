// @vitest-environment happy-dom
// The composer's drag-to-resize handle: what it clamps to, what it persists,
// and what a reset restores.
//
// It moves a CEILING (--composer-h feeds the textarea's max-height), not a
// height, because the box auto-grows with its content underneath. The clamp is
// measured against the chat area rather than the viewport, so a tall composer
// cannot push the transcript to nothing — copying the shell panel's
// `innerHeight * 0.8` would not have held, since the composer shares the bottom
// bar with the dock and the pill rows.
import { describe, it, expect, beforeEach, vi } from "vitest";

import { LS_UI_STATE_KEY } from "./ls-keys.js";
import type * as ComposerResize from "./composer-resize.js";

type ComposerResizeModule = typeof ComposerResize;

/** Give an element a measurable box; happy-dom reports 0 for everything. */
function stubHeight(id: string, h: number): void {
  const el = document.getElementById(id);
  if (el === null) {
    throw new Error(`missing #${id}`);
  }
  el.getBoundingClientRect = () => ({ height: h }) as DOMRect;
}

function savedHeight(): number | undefined {
  const raw = localStorage.getItem(LS_UI_STATE_KEY);
  return raw === null ? undefined : (JSON.parse(raw) as { composer_h?: number }).composer_h;
}

function handle(): HTMLElement {
  return document.getElementById("composer-resize") as HTMLElement;
}

function ceiling(): string {
  return (document.getElementById("prompt-box") as HTMLElement).style.getPropertyValue(
    "--composer-h",
  );
}

/** Fresh module per case: the module latches its own init, and the handle's
 *  listeners are wired once.
 *
 *  `areaH` of 0 is the hidden-view-at-boot case: happy-dom reports 0 for an
 *  unstyled element anyway, and a real browser reports it for a view that has not
 *  been laid out, which is what makes the ceiling UNKNOWN rather than small. */
async function mount(areaH = 600): Promise<ComposerResizeModule> {
  vi.resetModules();
  document.body.innerHTML = `
    <div id="chat-area">
      <form id="prompt-form">
        <div id="prompt-box">
          <div id="composer-resize"></div>
          <textarea id="prompt-input"></textarea>
        </div>
      </form>
    </div>`;
  // 600px of chat area, a 40px box and 100px of bar chrome (the pill rows), so
  // the ceiling can reach 600 - 100 - 128 = 372px.
  stubHeight("chat-area", areaH);
  stubHeight("prompt-form", 140);
  stubHeight("prompt-input", 40);
  const mod = await import("./composer-resize.js");
  mod.initComposerResize();
  return mod;
}

/** A ResizeObserver the test can fire, because happy-dom's own is a documented
 *  no-op in all three methods — so the module's watcher would never run under it,
 *  and "the chat area got layout later" is exactly the case that needs driving. */
let fireResize: (() => void) | undefined;
let observerDisconnected = false;

class TestResizeObserver implements ResizeObserver {
  constructor(cb: ResizeObserverCallback) {
    fireResize = (): void => {
      cb([], this);
    };
  }
  observe(): void {
    // Nothing to watch: the test decides when a resize happened.
  }
  unobserve(): void {
    // Unused by the module.
  }
  disconnect(): void {
    observerDisconnected = true;
  }
}

beforeEach(() => {
  localStorage.clear();
  fireResize = undefined;
  observerDisconnected = false;
  globalThis.ResizeObserver = TestResizeObserver;
});

describe("the a11y contract, set in code rather than markup", () => {
  it("makes the bare div a focusable horizontal separator", async () => {
    await mount();
    expect(handle().getAttribute("role")).toBe("separator");
    expect(handle().getAttribute("aria-orientation")).toBe("horizontal");
    expect(handle().getAttribute("aria-label")).toBe("Resize the message box");
    expect(handle().tabIndex).toBe(0);
  });
});

describe("clamping", () => {
  it("refuses to go below one row of text", async () => {
    const { applyComposerH } = await mount();
    expect(applyComposerH(4)).toBe(40);
    expect(applyComposerH(-500)).toBe(40);
  });

  it("leaves a floor of transcript, measured against the chat area", async () => {
    const { applyComposerH } = await mount();
    // 600 area - (140 form - 40 box) chrome - 128 transcript floor.
    expect(applyComposerH(99999)).toBe(372);
  });

  it("accounts for bar chrome, so an open dock lowers the ceiling", async () => {
    const { applyComposerH } = await mount();
    stubHeight("prompt-form", 340); // a 200px decision dock opened
    expect(applyComposerH(99999)).toBe(172);
  });

  it("writes the clamped value to --composer-h, which max-height consumes", async () => {
    const { applyComposerH } = await mount();
    applyComposerH(180);
    expect(ceiling()).toBe("180px");
  });
});

// composerMaxH() answers 0 for a chat area with no layout, and 0 means UNKNOWN.
// The old code read it as "use the CSS default", which turned a restored 500px
// ceiling into 200 on any boot where the chat view was still hidden — silently,
// and for the rest of the session, because every later interaction starts from the
// box's RENDERED height rather than the stored one. Nothing the user could do got
// their number back.
describe("a ceiling applied before the chat area can be measured", () => {
  it("keeps the requested value rather than substituting the CSS default", async () => {
    const { applyComposerH } = await mount(0);
    expect(applyComposerH(500)).toBe(500);
    expect(ceiling()).toBe("500px");
  });

  it("still enforces the floor, which needs no measurement", async () => {
    const { applyComposerH } = await mount(0);
    expect(applyComposerH(4)).toBe(40);
  });

  it("re-clamps the parked ceiling once the area reports a size", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 500 }));
    await mount(0);
    expect(ceiling()).toBe("500px");

    stubHeight("chat-area", 600); // the chat view became visible
    fireResize?.();

    // 372 is this layout's real ceiling, so the restore ends up clamped after all
    // — just against a measurement instead of a guess.
    expect(ceiling()).toBe("372px");
    // One-shot: the module measures at every interaction, so once it has an answer
    // there is nothing left to watch for.
    expect(observerDisconnected).toBe(true);
  });

  it("keeps watching while the area still has no layout", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 500 }));
    await mount(0);

    fireResize?.(); // a resize that still measures nothing
    expect(ceiling()).toBe("500px");
    expect(observerDisconnected).toBe(false);
  });

  it("drops the parked ceiling on a reset, so the re-clamp cannot undo one", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 500 }));
    await mount(0);
    handle().dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    expect(ceiling()).toBe("");

    stubHeight("chat-area", 600);
    fireResize?.();
    expect(ceiling()).toBe("");
    expect(savedHeight()).toBe(0);
  });

  it("lets a measured value supersede a parked one", async () => {
    const { applyComposerH } = await mount(0);
    applyComposerH(500); // parked
    stubHeight("chat-area", 600);
    expect(applyComposerH(300)).toBe(300); // measured: this is the answer now

    fireResize?.();
    expect(ceiling()).toBe("300px");
  });
});

describe("keyboard resize (WCAG 2.1.1)", () => {
  it("grows by 2rem on ArrowUp and persists the result", async () => {
    await mount();
    handle().dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }));
    // Starts from the box's rendered height (40), not from the stored ceiling.
    expect(ceiling()).toBe("72px");
    expect(savedHeight()).toBe(72);
  });

  it("shrinks on ArrowDown, still clamped at the floor", async () => {
    await mount();
    handle().dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    expect(ceiling()).toBe("40px");
    expect(savedHeight()).toBe(40);
  });

  it("ignores keys that are not resize keys", async () => {
    await mount();
    handle().dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true }));
    expect(ceiling()).toBe("");
    expect(savedHeight()).toBeUndefined();
  });
});

describe("reset", () => {
  it("drops back to the CSS default on double-click", async () => {
    const { applyComposerH } = await mount();
    applyComposerH(300);
    expect(ceiling()).toBe("300px");

    handle().dispatchEvent(new MouseEvent("dblclick", { bubbles: true, cancelable: true }));
    // The property is REMOVED rather than set to 200: that number has one home,
    // the stylesheet.
    expect(ceiling()).toBe("");
    expect(savedHeight()).toBe(0);
  });

  it("has a keyboard equal, so the reset is not mouse-only", async () => {
    const { applyComposerH } = await mount();
    applyComposerH(300);
    handle().dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    expect(ceiling()).toBe("");
    expect(savedHeight()).toBe(0);
  });
});

describe("persistence across a reload", () => {
  it("re-applies the stored ceiling on mount", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 260 }));
    await mount();
    expect(ceiling()).toBe("260px");
  });

  it("re-clamps a value stored on a taller window", async () => {
    // The whole reason restore does not trust the stored number: it may have been
    // set on a desktop and is being read on a phone.
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 5000 }));
    await mount();
    expect(ceiling()).toBe("372px");
  });

  it("applies nothing for 0, which means 'use the CSS default'", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 0 }));
    await mount();
    expect(ceiling()).toBe("");
  });

  it("ignores a non-finite stored height rather than feeding it to the math", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: "tall" }));
    await mount();
    expect(ceiling()).toBe("");
  });
});

// A rotate or an on-screen keyboard shrinks the area the ceiling was measured
// against while nothing is being dragged, so there is no interaction to
// re-clamp on. Under happy-dom window.visualViewport is undefined, so the
// module's listener lands on window, which is what these cases dispatch.
describe("re-clamping on a viewport change", () => {
  /** Let the module's rAF-coalesced handler run. */
  const frame = (): Promise<void> =>
    new Promise((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });

  it("clamps a saved ceiling down when the area shrinks, and does not persist it", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 360 }));
    await mount(600);
    expect(ceiling()).toBe("360px");

    // 300px of area, 100px of bar chrome, 128px transcript floor.
    stubHeight("chat-area", 300);
    window.dispatchEvent(new Event("resize"));
    await frame();

    expect(ceiling()).toBe("72px");
    // A keyboard opening is not the user choosing a smaller box.
    expect(savedHeight()).toBe(360);
  });

  it("restores the user's number when the area grows back", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 360 }));
    await mount(600);

    stubHeight("chat-area", 300);
    window.dispatchEvent(new Event("resize"));
    await frame();
    expect(ceiling()).toBe("72px");

    // Rotating back must not leave the ratcheted-down value: the re-clamp reads
    // the SAVED ceiling, not the one currently applied.
    stubHeight("chat-area", 600);
    window.dispatchEvent(new Event("resize"));
    await frame();
    expect(ceiling()).toBe("360px");
  });

  it("coalesces a burst into one re-clamp", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 360 }));
    await mount(600);

    stubHeight("chat-area", 300);
    for (let i = 0; i < 5; i++) {
      window.dispatchEvent(new Event("resize"));
    }
    await frame();
    expect(ceiling()).toBe("72px");
  });

  it("leaves a drag in progress alone", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ composer_h: 360 }));
    await mount(600);
    // pointermove is already applying a ceiling against the live layout; a
    // re-clamp underneath it would fight the pointer.
    handle().classList.add("dragging");

    stubHeight("chat-area", 300);
    window.dispatchEvent(new Event("resize"));
    await frame();
    expect(ceiling()).toBe("360px");
  });

  it("does nothing when no ceiling was ever saved", async () => {
    await mount(600);
    expect(ceiling()).toBe("");
    stubHeight("chat-area", 300);
    window.dispatchEvent(new Event("resize"));
    await frame();
    expect(ceiling()).toBe("");
  });
});
