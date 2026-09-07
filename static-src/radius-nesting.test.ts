import { describe, it, expect, afterEach, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// Nested corners: a child sitting against its container's corner is concentric
// only at `inner = outer - outer-border - inset` (web.md "A nested rounded
// corner"). These are the pairs where getting it wrong is visible, pinned
// because the arithmetic lives in a `calc()` on a custom property and a missing
// declaration fails SILENTLY — `var(--menu-radius)` with no declaration resolves
// to an invalid value and the corner renders SQUARE, which is what one of these
// three shipped as for a few minutes while it was being written.
// ---------------------------------------------------------------------------

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:-9999px;left:0;";
document.body.appendChild(host);

// These assertions read COMPUTED radii, so the app's own stylesheet has to be in
// the document — a text-level check could not catch the silent failure this file
// exists for, because `var(--menu-radius)` with no declaration is valid CSS text
// and only resolves to nothing at computed-value time.
let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
});

afterEach(() => {
  host.replaceChildren();
});

function radiusOf(el: Element): number {
  return parseFloat(getComputedStyle(el).borderRadius);
}

describe("the three menu surfaces derive one corner from one shape", () => {
  // All three are a 1px border with a --sp-1 list inset, so one derivation serves
  // them. The container is --r-lg (12px): at the --r (6px) it used to carry, the
  // concentric inner radius is 6 - 1 - 4 = 1px, which is square to the eye — and
  // the items shipped 6px (identical to their container) or a `calc(var(--r) / 2)`
  // halving that landed 2px over. It also closes the pair the user named, since
  // the composer is --r-lg and the top-right menu was --r.
  it.each([
    ["popup", "popup-list", "popup-item"],
    ["tab-context-menu", "", "tab-context-item"],
    ["chip-menu", "", "chip-menu-item"],
  ])("%s holds a concentric %s", (outerCls, midCls, innerCls) => {
    const outer = document.createElement("div");
    outer.className = outerCls;
    let mount: HTMLElement = outer;
    if (midCls !== "") {
      const mid = document.createElement("div");
      mid.className = midCls;
      outer.appendChild(mid);
      mount = mid;
    }
    const inner = document.createElement("button");
    inner.className = innerCls;
    mount.appendChild(inner);
    host.appendChild(outer);

    const o = radiusOf(outer);
    const i = radiusOf(inner);
    expect(o, "the container takes the 12px container rung").toBeCloseTo(12, 1);
    // 12 outer - 1px border - 4px inset. A missing custom property renders 0.
    expect(i, `${innerCls} must be concentric, and non-zero`).toBeCloseTo(7, 1);
  });
});

describe("a 24px icon button takes the ladder's small rung", () => {
  // The ladder reserves --r-sm for a 16-24px box. Each of these shipped --r
  // (6px), and for `.tab-close` that was its own container's radius, so the
  // button's arc was identical to the tab holding it.
  it.each(["tab-close", "shell-header-btn", "turn-action-btn", "native-rule-remove"])(
    "%s is 4px, not 6px",
    (cls) => {
      const b = document.createElement("button");
      b.className = cls;
      host.appendChild(b);
      expect(radiusOf(b)).toBeCloseTo(4, 1);
    },
  );
});

describe("an inner box is never rounder than the box holding it", () => {
  it("makes the pill's count badge a stadium rather than a rounder rung", () => {
    // It declared --r-md (8px) inside a 7px --pill-radius pill. At 14px tall, 8px
    // already exceeds half the height, so the browser clamped it to a stadium and
    // the declared rung never rendered — the fix is to say what it renders as.
    const badge = document.createElement("span");
    badge.className = "pill-badge";
    badge.textContent = "3";
    host.appendChild(badge);
    const r = radiusOf(badge);
    const h = badge.getBoundingClientRect().height;
    expect(r, "a stadium's radius is at least half the box's height").toBeGreaterThanOrEqual(h / 2);
  });

  it("derives the chat-options input from the form it sits in", () => {
    // It shipped --r (6px) inside a --card-radius (3px) form: twice as round as
    // its container.
    const form = document.createElement("form");
    form.className = "chat-opt-form";
    const input = document.createElement("input");
    input.className = "chat-opt-input";
    form.appendChild(input);
    host.appendChild(form);
    expect(radiusOf(input)).toBeLessThanOrEqual(radiusOf(form) + 0.5);
  });
});

describe("a box with a header at its top has ONE radius owner", () => {
  it("leaves a code block's pre square so its wrapper draws every corner", () => {
    // The prose skin gave the pre `--r` on all four, and the wrapper's
    // `overflow: hidden` does not square a child's own corners — it removes only
    // what falls outside — so the top pair painted a notch of card background at
    // each end of the head's seam. An override on `.code-wrap > pre` cannot fix
    // it: `.message.assistant :where(pre)` scores (0,2,0) against its (0,1,1),
    // which is why the radius is deleted at the prose skin instead.
    const msg = document.createElement("div");
    msg.className = "message assistant";
    const wrap = document.createElement("div");
    wrap.className = "code-wrap";
    const head = document.createElement("div");
    head.className = "code-head";
    const pre = document.createElement("pre");
    wrap.append(head, pre);
    msg.appendChild(wrap);
    host.appendChild(msg);

    expect(radiusOf(wrap), "the wrapper draws the box").toBeCloseTo(6, 1);
    const s = getComputedStyle(pre);
    for (const [corner, value] of Object.entries({
      "top-left": s.borderTopLeftRadius,
      "top-right": s.borderTopRightRadius,
      "bottom-right": s.borderBottomRightRadius,
      "bottom-left": s.borderBottomLeftRadius,
    })) {
      expect(parseFloat(value), `pre ${corner} must be square`).toBe(0);
    }
  });
});
