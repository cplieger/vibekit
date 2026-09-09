// ---------------------------------------------------------------------------
// `geometrySkipped` names two CSS rules, and this is what keeps it honest.
//
// The predicate answers "would reading this element's box force the browser to
// render a subtree it chose to skip", and it answers by matching two selectors
// against the DOM. Those selectors are a claim ABOUT the stylesheets: that
// `.turn[data-folded] > .turn-body` and `.transcript-view:not(.is-active)` are
// where `content-visibility: hidden` lives. Nothing links the two halves, so a
// rule renamed in CSS leaves the predicate matching nothing and every geometry
// read it guards silently starts forcing a render again — which is invisible to
// the type checker, to the linter, and to every behavioural test, because the
// wrong answer is a performance fault rather than a wrong number.
//
// A SOURCE guard plus a DOM guard, deliberately, because they fail for different
// reasons: the first catches the CSS moving out from under the predicate, the
// second catches the predicate's own matching breaking. A computed-style check
// cannot replace either — `content-visibility` resolves to `hidden` on a parked
// view whether or not the predicate knows the selector.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

// `messages-blocks.ts`'s graph reaches `scroll.ts`, a self-initialising singleton
// that resolves `#messages` at module load, and `byId` throws on a missing
// element — so the hosts exist before the import resolves and the scroll
// subsystem is the canonical mock every suite in this graph uses.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

const { geometrySkipped } = await import("./messages-blocks.js");

/** The two subtree shapes the transcript skips, each with the sheet that declares
 *  it. The selector strings are the predicate's own, so a drift on either side of
 *  the correspondence fails here. */
const SKIPPED = [
  { sheet: "29-turns.css", selector: ".turn[data-folded] > .turn-body" },
  { sheet: "13-messages.css", selector: ".transcript-view:not(.is-active)" },
] as const;

/** Build `selector`'s shape for real and return the descendant to measure, so the
 *  predicate walks a genuine ancestor chain rather than a hand-set attribute. */
function mountSkipped(selector: string): HTMLElement {
  const host = document.createElement("div");
  if (selector.startsWith(".turn[")) {
    host.className = "turn";
    host.setAttribute("data-folded", "");
    host.innerHTML = `<div class="turn-body"><div class="message assistant">prose</div></div>`;
  } else {
    host.className = "transcript-view";
    host.innerHTML = `<div class="msg-wrap"><div class="message assistant">prose</div></div>`;
  }
  document.body.appendChild(host);
  const leaf = host.querySelector<HTMLElement>(".message");
  if (leaf === null) {
    throw new Error(`no leaf mounted for ${selector}`);
  }
  return leaf;
}

describe("geometrySkipped tracks the stylesheets it speaks for", () => {
  for (const { sheet, selector } of SKIPPED) {
    it(`${selector} is still where ${sheet} skips rendering`, () => {
      const rule = ruleContaining(loadCSS(sheet), selector, "top");
      expect(rule.body).toContain("content-visibility: hidden");
    });

    it(`answers true inside ${selector}`, () => {
      expect(geometrySkipped(mountSkipped(selector))).toBe(true);
    });
  }

  it("answers false for a block the page is rendering", () => {
    // The control. Without it every case above is satisfied by a predicate that
    // returns true unconditionally, which would skip every measurement the
    // spacers depend on.
    const host = document.createElement("div");
    host.className = "transcript-view is-active";
    host.innerHTML = `<div class="turn"><div class="turn-body"><div class="message assistant">prose</div></div></div>`;
    document.body.appendChild(host);
    const leaf = host.querySelector<HTMLElement>(".message");
    expect(leaf).not.toBeNull();
    expect(geometrySkipped(leaf as HTMLElement)).toBe(false);
  });
});
