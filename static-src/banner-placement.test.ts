// Structural guard for the banner band's placement (static/index.html).
//
// A banner has to be CLICKABLE, and two elements will take its clicks away if
// it is put back where it used to be.
//
//   1. `#messages-wrap` is `position: absolute; inset: 0` inside
//      `#messages-wrap-outer` (13-messages.css). A banner inside that wrapper is
//      painted under the full-box scroller, so it reads fine and answers no
//      pointer event. That is how the `agent_config_error` banner shipped an
//      "Open custom instructions" button nobody could press.
//   2. `.chat-toolbar` is anchored to the same top corner, opaque, at z-index 10
//      (12-chat.css). A full-width band starting at the top of the view runs its
//      right end — the action button and the dismiss × — under six navigation
//      buttons. `--below-chat-toolbar` is what moves the band clear of it, and it
//      is the same derivation `.chat-find` and `.turn-rail` use.
//
// Neither is visible in a unit test of the banner MODULE, because those build
// their own detached container. The markup and the two CSS rules are the whole
// contract, so this reads all three.

import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";
import chatCss from "./css/12-chat.css?raw";
import messagesCss from "./css/13-messages.css?raw";

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
    // Before the wrapper, so the band reads above the transcript rather than below it.
    expect(stack?.nextElementSibling?.id).toBe("messages-wrap-outer");
  });
});

describe("the chat toolbar's clearance is derived once", () => {
  it("declares --below-chat-toolbar beside the toolbar it measures", () => {
    expect(chatCss).toContain("--below-chat-toolbar: calc(");
    // Every term of the toolbar's own box, so a padding change reaches it.
    for (const term of ["var(--sp-3)", "var(--btn-h)", "0.25rem", "var(--sp-2)"]) {
      expect(chatCss).toContain(term);
    }
  });

  it("has the banner band consume it rather than start at the top of the view", () => {
    expect(messagesCss).toContain("margin-block-start: var(--below-chat-toolbar);");
  });
});
