// The find walker against the SHIPPED stylesheet. find-in-chat.test.ts builds its
// transcript DOM with no CSS at all, which is the right shape for the navigation
// criteria and also why a walker that pruned every assistant reply shipped:
// `display: contents` measures as a plain block there.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { FindEngine } from "./find-engine.js";

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = mountAppCSS();
  host = document.createElement("div");
  // IN VIEW, at a real width. `.msg-row` carries `content-visibility: auto`, so an
  // off-screen fixture would be pruned for a reason this file is not about.
  host.style.cssText = "inline-size:640px;";
  document.body.prepend(host);
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

/** One assistant message as `buildBody` seats it: the boxless block region, a row
 *  inside it, the prose bubble inside that. */
function assistantReply(text: string): HTMLElement {
  host.innerHTML =
    `<div class="msg-wrap msg-wrap-assistant">` +
    `<div class="assistant-blocks">` +
    `<div class="msg-row"><div class="message assistant">${text}</div></div>` +
    `</div></div>`;
  return host.firstElementChild as HTMLElement;
}

/** One rendered frame: `content-visibility: auto` relevance is answered from the
 *  last lifecycle update, so a walk in the mount's own tick prunes a row that is
 *  not yet relevant. `navigateToHit` waits the same frame out with `nextRender`. */
function rendered(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

describe("the walker under the shipped stylesheet", () => {
  it("marks prose inside the boxless assistant block region", async () => {
    // The defect: `.assistant-blocks { display: contents }` answers
    // checkVisibility FALSE, the walker pruned the whole subtree at it, and search
    // could select a block and say why but never place a <mark> in a reply.
    const wrap = assistantReply("the retry backoff is documented TODO here");
    await rendered();
    expect(new FindEngine(wrap).search("TODO")).toBe(1);
  });

  it("puts the mark in the prose bubble itself", async () => {
    const wrap = assistantReply("the retry backoff is documented TODO here");
    await rendered();
    new FindEngine(wrap).search("TODO");
    const mark = wrap.querySelector("mark");
    expect(mark?.parentElement?.className).toBe("message assistant");
  });

  // The fixture's own premise, so a stylesheet change that quietly gives the
  // region a box turns the test above into one that cannot fail rather than
  // leaving it green for the wrong reason.
  it("is measuring a region Chromium reports as invisible", () => {
    const wrap = assistantReply("prose");
    const blocks = wrap.querySelector(".assistant-blocks") as HTMLElement;
    expect({
      display: getComputedStyle(blocks).display,
      // find-engine.ts's own option set.
      visible: blocks.checkVisibility({
        contentVisibilityAuto: true,
        visibilityProperty: true,
        opacityProperty: false,
      }),
    }).toEqual({ display: "contents", visible: false });
  });

  it("still prunes a subtree that is boxless because it is HIDDEN", async () => {
    // The other half of the contract: neither of these has client rects either, so
    // an implementation keyed on the absence of a box rather than on the computed
    // `display` passes the cases above and fails this one.
    host.innerHTML =
      `<div class="assistant-blocks">` +
      `<div style="display:none"><div class="message">display TODO</div></div>` +
      `<div style="content-visibility:hidden"><div class="message">skipped TODO</div></div>` +
      `<div class="msg-row"><div class="message">shown TODO</div></div>` +
      `</div>`;
    await rendered();
    expect(new FindEngine(host).search("TODO")).toBe(1);
  });

  it("still prunes an off-screen row that content-visibility SKIPPED", async () => {
    // The `auto` variant, which is the one a long transcript's cost rests on: the
    // walker must stay out of every card below the fold, and a skipped row is
    // boxless to `checkVisibility` exactly as the region above it is.
    host.innerHTML =
      `<div class="assistant-blocks">` +
      `<div class="msg-row"><div class="message">shown TODO</div></div>` +
      `<div style="block-size:2400px"></div>` +
      `<div class="msg-row"><div class="message">skipped TODO</div></div>` +
      `</div>`;
    await rendered();
    expect(new FindEngine(host).search("TODO")).toBe(1);
  });
});
