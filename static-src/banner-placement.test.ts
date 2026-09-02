// Structural and GEOMETRIC guard for the banner band's placement.
//
// A banner has to be CLICKABLE, and two elements take its clicks away if it is
// put back where it used to be.
//
//   1. `#messages-wrap` is `position: absolute; inset: 0` inside
//      `#messages-wrap-outer` (13-messages.css). A banner inside that wrapper is
//      painted under the full-box scroller, so it reads fine and answers no
//      pointer event. That is how the `agent_config_error` banner shipped an
//      "Open custom instructions" button nobody could press.
//   2. `.chat-toolbar` is anchored to the same top corner, opaque, at z-index 10
//      (12-chat.css). The band now shares that row, so what keeps its dismiss ×
//      and its action button reachable is `inset-inline-end` derived from the
//      toolbar's measured width — and, because the band is out of flow and
//      precedes the transcript in DOM order, an explicit `z-index`.
//
// The band's position is the contract, so most of this reads RENDERED GEOMETRY
// rather than CSS text: a substring assertion cannot tell a band level with the
// toolbar from one 2px under it, and it goes green on a rule the cascade
// overrides. `mountAppCSS` assembles the stylesheet from `css/MANIFEST` in
// declared order, the way `cmd/bundle` concatenates it, because equal-specificity
// ties in this app are decided by that order rather than by the selectors.

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import indexHtml from "../static/index.html?raw";
import messagesCss from "./css/13-messages.css?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { initChatToolbarMetrics } from "./chat-toolbar-metrics.js";

describe("banner band placement (static/index.html)", () => {
  it("puts the banner stack outside the transcript scroller's wrapper", () => {
    const host = document.createElement("div");
    const start = indexHtml.indexOf('<div id="chat-view"');
    const end = indexHtml.indexOf('<form id="prompt-form"', start);
    expect(start, "#chat-view not found").toBeGreaterThan(-1);
    expect(end, "#prompt-form not found").toBeGreaterThan(start);
    host.innerHTML = indexHtml.slice(start, end);

    const stack = host.querySelector<HTMLElement>("#banner-stack");
    expect(stack, "#banner-stack must exist").not.toBeNull();
    expect(
      stack?.closest("#messages-wrap-outer"),
      "#banner-stack must not sit inside #messages-wrap-outer — the scroller is inset:0 over it",
    ).toBeNull();
    expect(stack?.parentElement?.id).toBe("chat-view");
    // Before the wrapper, so the band paints above the transcript rather than under it.
    expect(stack?.nextElementSibling?.id).toBe("messages-wrap-outer");
  });
});

describe("the banner band shares the chat toolbar's row", () => {
  let styleEl: HTMLStyleElement;
  let app: HTMLElement;
  let toolbar: HTMLElement;
  let band: HTMLElement;
  let wrapOuter: HTMLElement;
  let banner: HTMLElement;

  /** The real ancestor chain, because every rule under test is scoped to it: the
   *  band's containing block is `#chat-area` (`position: relative`; `#chat-view`
   *  sets none), and `[data-tab-view]` is what makes `#chat-view` a flex column. */
  beforeAll(() => {
    styleEl = mountAppCSS();

    app = document.createElement("div");
    app.id = "app";
    app.innerHTML = `
      <main id="chat-area">
        <div class="chat-toolbar">
          <button type="button" id="find-btn" class="icon-btn" aria-label="Search"></button>
          <button type="button" id="files-btn" class="icon-btn" aria-label="Files"></button>
          <button type="button" id="git-btn" class="icon-btn" aria-label="Git"></button>
        </div>
        <div id="chat-view" data-tab-view>
          <div id="banner-stack" class="banner-stack"></div>
          <div id="messages-wrap-outer"><div id="messages-wrap"></div></div>
        </div>
      </main>`;
    document.body.appendChild(app);

    toolbar = app.querySelector<HTMLElement>(".chat-toolbar")!;
    band = app.querySelector<HTMLElement>("#banner-stack")!;
    wrapOuter = app.querySelector<HTMLElement>("#messages-wrap-outer")!;

    // The real publisher, so the wiring this geometry depends on is pinned too:
    // without `--chat-toolbar-w` the band's fallback runs it under the toolbar.
    initChatToolbarMetrics();

    banner = document.createElement("div");
    banner.className = "banner banner-error";
    banner.innerHTML = `<span class="banner-glyph" aria-hidden="true">\u2717</span><span class="banner-msg">A message.</span>`;
    band.appendChild(banner);
  });

  afterAll(() => {
    styleEl?.remove();
    app?.remove();
    document.documentElement.style.removeProperty("--chat-toolbar-w");
  });

  it("puts the band's top edge level with the toolbar's", () => {
    expect(band.getBoundingClientRect().top).toBeCloseTo(toolbar.getBoundingClientRect().top, 1);
  });

  it("keeps the band clear of the toolbar rather than under it", () => {
    const gap = toolbar.getBoundingClientRect().left - band.getBoundingClientRect().right;
    // One `--sp-2`. Below zero the dismiss × and the action button sit under six
    // navigation buttons at z-index 10, which is defect 2 above.
    expect(gap).toBeGreaterThanOrEqual(8);
  });

  it("gives a banner row at least the toolbar's full box height", () => {
    expect(banner.getBoundingClientRect().height).toBeGreaterThanOrEqual(
      toolbar.getBoundingClientRect().height,
    );
  });

  it("reserves no flow space, so a banner reveals no canvas above the transcript", () => {
    // THE Q2 CONTRACT. In flow the band's `margin-block-start` shrank the
    // transcript by 60px plus its own height, and the region it vacated painted
    // the `body` canvas — read as a hard box from the top of the viewport down
    // past the banner. An overlay moves nothing.
    const outerTop = wrapOuter.getBoundingClientRect().top;
    const viewTop = app.querySelector<HTMLElement>("#chat-view")!.getBoundingClientRect().top;
    expect(outerTop).toBeCloseTo(viewTop, 1);
  });

  it("declares no background of its own, on the band or on the stack", () => {
    // Nothing painted the reported box, so a `background` appearing here would be
    // a new one rather than a fix.
    expect(getComputedStyle(band).backgroundColor).toBe("rgba(0, 0, 0, 0)");
  });

  it("stacks above the transcript and below the toolbar", () => {
    // Out of flow it precedes `#messages-wrap-outer` in DOM order, and two
    // positioned `z-index: auto` boxes paint in that order — so an explicit
    // z-index is what stops the transcript covering it.
    const bandZ = Number(getComputedStyle(band).zIndex);
    expect(Number.isNaN(bandZ), "the band needs an explicit z-index, not `auto`").toBe(false);
    expect(bandZ).toBeGreaterThan(0);
    expect(bandZ).toBeLessThan(Number(getComputedStyle(toolbar).zIndex));
  });
});

describe("--chat-toolbar-h is the toolbar's real box height", () => {
  let styleEl: HTMLStyleElement;
  let app: HTMLElement;

  beforeAll(() => {
    styleEl = mountAppCSS();
    app = document.createElement("div");
    app.id = "app";
    app.innerHTML = `
      <main id="chat-area">
        <div class="chat-toolbar">
          <button type="button" class="icon-btn" aria-label="Search"></button>
        </div>
      </main>`;
    document.body.appendChild(app);
  });

  afterAll(() => {
    styleEl?.remove();
    app?.remove();
  });

  it("resolves to what the toolbar actually measures", () => {
    // MEASURED rather than string-matched, which is what makes this catch the
    // defect the old assertion could not see: the offset that reads this token
    // omitted the toolbar's 1px borders, so the `--sp-2` gap it claims was 6px.
    // A dropped term, a stale `--btn-h` or a padding change all fail here.
    const probe = document.createElement("div");
    probe.style.blockSize = "var(--chat-toolbar-h)";
    app.appendChild(probe);
    const toolbar = app.querySelector<HTMLElement>(".chat-toolbar")!;
    expect(probe.getBoundingClientRect().height).toBeCloseTo(
      toolbar.getBoundingClientRect().height,
      1,
    );
    probe.remove();
  });
});

describe("the banner band on a phone", () => {
  it("returns to the flow under the toolbar", () => {
    // The one rule the fixed 1280px test viewport cannot reach, so it is read off
    // the CSSOM rather than rendered: at this width `.chat-toolbar` is in flow and
    // paints the top band itself, so the band belongs below it and in flow — an
    // overlay would sit on top of the live turn on a short viewport.
    const media = messagesCss.match(/@media \(width <= 48rem\) \{\s*\.banner-stack \{([^}]*)\}/);
    expect(media, "13-messages.css must carry the band's own <=48rem block").not.toBeNull();
    expect(media?.[1]).toContain("position: static");
  });
});
