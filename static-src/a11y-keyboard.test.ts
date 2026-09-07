// Accessibility tests: tool-group keyboard nav, file-picker labels, upload progress.
//
// EVERY CASE HERE GIVES ITS SHELL TWO MEMBERS, and that is not scaffolding. A
// one-member group is BARE: `14-tools.css` gives it `display: none`, so its header
// is out of the accessibility tree and out of tab order and no reader can drive it.
// These cases mount no stylesheet, so they would keep passing on a zero-member
// shell — exercising a control that cannot be reached, which is the false-confidence
// shape the testing rules call out. The last case is the other half: it mounts the
// real stylesheet and asserts the bare header is genuinely unfocusable.
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

/** A settled tool card, as the group's DOM sees it. */
function memberCard(i: number): HTMLElement {
  const call = document.createElement("div");
  call.className = "tool-call";
  call.dataset["kind"] = "read";
  call.dataset["title"] = `read${String(i)}`;
  call.dataset["filename"] = `f${String(i)}.ts`;
  call.dataset["mcpServer"] = "";
  return call;
}

/** A shell with `n` settled members, mounted, with its header refreshed — which is
 *  what drives the bare class. Two is the minimum for a reachable header. */
async function shellWith(n: number): Promise<HTMLDivElement> {
  const { buildToolGroupShell, groupBody, refreshGroupHeader } = await import("./tool-group.js");
  const group = buildToolGroupShell();
  for (let i = 0; i < n; i++) {
    groupBody(group).appendChild(memberCard(i));
  }
  document.body.appendChild(group);
  refreshGroupHeader(group);
  return group;
}

describe("a11y: tool-group header keyboard and aria", () => {
  it("header has role=button, tabindex=0, and aria-expanded=true", async () => {
    const group = await shellWith(2);
    const header = group.querySelector(".tool-group-header")!;
    expect(header.getAttribute("role")).toBe("button");
    expect(header.getAttribute("tabindex")).toBe("0");
    expect(header.getAttribute("aria-expanded")).toBe("true");
    document.body.removeChild(group);
  });

  it("header toggles aria-expanded on Enter key", async () => {
    const group = await shellWith(2);
    const header = group.querySelector(".tool-group-header")!;

    header.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("false");
    expect(group.classList.contains("tool-group-collapsed")).toBe(true);

    header.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("true");
    expect(group.classList.contains("tool-group-collapsed")).toBe(false);

    document.body.removeChild(group);
  });

  it("header toggles aria-expanded on Space key", async () => {
    const group = await shellWith(2);
    const header = group.querySelector(".tool-group-header")!;

    header.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("false");

    document.body.removeChild(group);
  });

  it("autoCollapseGroup sets aria-expanded=false", async () => {
    const { autoCollapseGroup } = await import("./tool-group.js");
    const group = await shellWith(3);

    const header = group.querySelector(".tool-group-header")!;
    expect(header.getAttribute("aria-expanded")).toBe("true");

    autoCollapseGroup(group);
    expect(group.classList.contains("tool-group-auto-collapsed")).toBe(true);
    expect(header.getAttribute("aria-expanded")).toBe("false");

    document.body.removeChild(group);
  });
});

describe("a11y: a bare group's header is not a tab stop", () => {
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  it("cannot be focused at one member, and can be at two", async () => {
    const { refreshGroupHeader, groupBody } = await import("./tool-group.js");
    const group = await shellWith(1);
    const header = group.querySelector<HTMLElement>(".tool-group-header")!;

    // `display: none` is what removes it from the accessibility tree AND from tab
    // order; `visibility: hidden` and `aria-hidden` each leave one of the two.
    expect(getComputedStyle(header).display).toBe("none");
    const before = document.activeElement;
    header.focus();
    expect(document.activeElement).toBe(before);

    // The same header becomes reachable the moment a second member lands: nothing
    // is rebuilt, so no focus or disclosure state could have been lost on the way.
    groupBody(group).appendChild(memberCard(1));
    refreshGroupHeader(group);
    expect(getComputedStyle(header).display).toBe("flex");
    header.focus();
    expect(document.activeElement).toBe(header);

    document.body.removeChild(group);
  });
});
