import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { setBusy, setControlBusy } from "./dom.js";
import { allRules, mountAppCSS } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// A control has THREE states below "active", and each one has one owner.
//
// UNAVAILABLE (`:disabled`) recedes: dimmed, `not-allowed`. Nine families each
// spelled that themselves and no two agreed — six opacities and four cursors
// between them, while `--opacity-disabled` sat unread.
//
// BUSY (`:disabled[aria-busy="true"]`) does NOT recede: a control whose own work
// is in flight grows a spinner and its label becomes "Suggesting…", so dimming
// fades the very words reporting the work. Full opacity, `cursor: progress` —
// `wait` would claim the whole app is blocked.
//
// READOUT is the state that looks like the first and is not: a disabled
// `.send-btn` is a state the app never enters, so dimming it would fade a control
// nobody is being refused. It opts out explicitly.
//
// It used to have a second member, and losing one is what this paragraph now
// records: `.turn-ledger-summary` was `disabled` on a turn with no files to
// disclose, which made it a readout wearing a control's markup. That state is gone
// — the summary is always a disclosure now, because it opens the turn's info panel
// rather than a file list — so its opt-out rule left `29-turns.css` and its entry
// left the allow-list below. Leaving the entry would have made the sweep pass
// VACUOUSLY: an allow-list naming a selector no stylesheet declares excuses
// nothing and reads as though it does.
//
// The sweep is what keeps it one owner: any rule re-declaring the face fails
// here, and a control that must differ has to say so in the allow-list below,
// where the reason is reviewable.
// ---------------------------------------------------------------------------

/** Every shipped stylesheet, so the sweep cannot miss a file. */
const sheets = import.meta.glob<string>("./css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

/** Selectors allowed to declare a disabled face of their own, and why. */
const OPT_OUTS = new Map([
  ["&:disabled", "the send button's readout opt-out, nested in .send-btn"],
]);

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  stage = document.createElement("div");
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

afterEach(() => {
  stage.replaceChildren();
});

function mount(cls: string): HTMLButtonElement {
  const b = document.createElement("button");
  b.className = cls;
  b.textContent = "Go";
  stage.appendChild(b);
  return b;
}

describe("the disabled face", () => {
  it("dims an unavailable control and refuses the pointer, from the token", () => {
    const b = mount("btn");
    b.disabled = true;

    const cs = getComputedStyle(b);
    expect(cs.opacity).toBe("0.4");
    expect(cs.cursor).toBe("not-allowed");
  });

  it("does NOT dim a busy control, and shows progress rather than refusal", () => {
    const b = mount("btn");
    setControlBusy(b, true);

    const cs = getComputedStyle(b);
    expect(b.disabled).toBe(true);
    expect(cs.opacity).toBe("1");
    expect(cs.cursor).toBe("progress");
  });

  it("returns a control to the unavailable face when its work ends", () => {
    const b = mount("btn");
    setControlBusy(b, true);
    setControlBusy(b, false);

    expect(b.disabled).toBe(false);
    expect(b.hasAttribute("aria-busy")).toBe(false);
    // Enabled, so neither face applies.
    expect(getComputedStyle(b).opacity).toBe("1");
  });

  it("leaves a disabled READOUT undimmed", () => {
    const send = mount("send-btn");
    send.disabled = true;

    expect(getComputedStyle(send).opacity).toBe("1");
    // It is not refusing anything.
    expect(getComputedStyle(send).cursor).toBe("default");
  });

  it("is declared in one place: no family re-spells it", () => {
    const offenders: string[] = [];
    for (const [path, css] of Object.entries(sheets)) {
      for (const { selector, body } of allRules(css)) {
        if (!/:disabled/.test(selector)) {
          continue;
        }
        // A hover/active twin exists only to suppress the enabled state's paint;
        // it is not a face of its own.
        if (/:hover|:active/.test(selector)) {
          continue;
        }
        if (!/(?<![-\w])(?:opacity|cursor):/.test(body)) {
          continue;
        }
        // The two shared rules in 40-a11y.css ARE the owner.
        if (path.endsWith("40-a11y.css")) {
          continue;
        }
        if (OPT_OUTS.has(selector)) {
          continue;
        }
        offenders.push(`${path} { ${selector} }`);
      }
    }
    expect(offenders, "only 40-a11y.css and the declared opt-outs style :disabled").toEqual([]);
  });
});

describe("setBusy", () => {
  it("writes the literal ARIA value, so the attribute is readable and matchable", () => {
    const b = mount("btn");
    setBusy(b, true);

    // `toggleAttribute` would write "", which Chromium's accessibility tree
    // reports as NO busy property — indistinguishable from the attribute being
    // absent — and which fails the selector the busy face is keyed on.
    expect(b.getAttribute("aria-busy")).toBe("true");
    expect(b.matches('[aria-busy="true"]')).toBe(true);
    expect(b.ariaBusy).toBe("true");

    setBusy(b, false);
    expect(b.getAttribute("aria-busy")).toBeNull();
  });
});
