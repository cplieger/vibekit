// The bar is mounted from `static/index.html` rather than a copy of its markup, because what
// the fit measures is the ACTIONS — how many, and how wide the tier makes them — so a
// hand-written fixture would keep passing after the page stopped agreeing with it. Same
// reason `pointer-mode.test.ts` does it.

import { describe, it, expect, afterEach } from "vitest";
import indexHtml from "../static/index.html?raw";

import { setPageTitle, setPageSubtitle, clearPageTitle, initPageTitleFit } from "./page-title.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** The whole `<div class="chat-toolbar">…</div>` out of the page, matched by
 *  counting nested div tags. Nothing formats `static/index.html` (prettier runs
 *  from `static-src/` and the page is its sibling), so its whitespace is not a
 *  contract and a blank-line delimiter would break on a reindent. */
function toolbarMarkup(): string {
  const start = indexHtml.indexOf('<div class="chat-toolbar">');
  expect(start, "static/index.html has no .chat-toolbar").toBeGreaterThan(-1);
  const tags = /<div\b|<\/div\s*>/g;
  tags.lastIndex = start;
  let depth = 0;
  for (let m = tags.exec(indexHtml); m !== null; m = tags.exec(indexHtml)) {
    depth += m[0].startsWith("</") ? -1 : 1;
    if (depth === 0) {
      return indexHtml.slice(start, m.index + m[0].length);
    }
  }
  throw new Error("unbalanced divs after .chat-toolbar");
}

let styleEl: HTMLStyleElement | null = null;
let app: HTMLElement | null = null;

afterEach(() => {
  app?.remove();
  app = null;
  styleEl?.remove();
  styleEl = null;
  document.documentElement.removeAttribute("data-pointer");
});

/** The real bar at a stated chat-area width, on the coarse tier. */
function mountBar(width: string): HTMLElement {
  styleEl = mountAppCSS();
  app = document.createElement("div");
  app.id = "app";
  app.innerHTML = `<main id="chat-area">${toolbarMarkup()}</main>`;
  document.body.appendChild(app);
  const area = app.querySelector<HTMLElement>("#chat-area");
  expect(area).not.toBeNull();
  area?.style.setProperty("inline-size", width);
  document.documentElement.setAttribute("data-pointer", "coarse");
  return app.querySelector<HTMLElement>(".titlebar-heading") as HTMLElement;
}

const LONG = "Fix the mobile top bar menu alignment";

describe("the title bar's measured fit", () => {
  it("clips the heading when the actions leave too little room for the title", () => {
    // A phone: the actions and the bar's padding fill the row, so a title of any
    // real length cannot render and a three-character stub is worth less than
    // nothing. `.sr-only` and not `display: none` — the assertions below are what
    // say the `<h1>` survives.
    const heading = mountBar("390px");
    setPageTitle(LONG, "chat");
    expect(heading.classList.contains("sr-only"), "clipped at 390px").toBe(true);
  });

  it("shows the heading where the title fits whole", () => {
    // A tablet-width chat area: the actions need the same room, and what is left is
    // more than the title wants.
    const heading = mountBar("768px");
    setPageTitle(LONG, "chat");
    expect(heading.classList.contains("sr-only"), "shown at 768px").toBe(false);
    const title = heading.querySelector<HTMLElement>(".titlebar-title");
    expect(title?.scrollWidth, "the title renders untruncated").toBe(title?.clientWidth);
  });

  it("clips a SHORT title once the ACTIONS have wrapped", () => {
    // AMENDMENT §E, and this is the bistable band it rules must go. The title's own
    // overflow is not the whole test: the heading is `flex: 1 1 0`, so it contributes
    // no basis to the line, but it is still a flex item and the bar is still charged
    // its 2px GAP — which the row's 44px targets and their gaps cannot spare. So the
    // bar wrapped, the wrap handed the heading a row of its own, a short title then
    // measured as fitting, and it rendered BECAUSE the actions had spilled onto a
    // second row: a title visible only in the state where the bar had broken for it.
    // Measured on the SERVED page (where the phone row is eight buttons rather than
    // this environment's seven, the hamburger being desktop-hidden at the test
    // viewport's own width): before, 387-391 showed the title over a 91px two-row
    // bar; after, 390 and 391 are a single 52px row and the band is gone.
    const heading = mountBar("320px");
    setPageTitle("Git", "chat");
    expect(heading.classList.contains("sr-only"), "clipped although the title is short").toBe(true);

    // The title genuinely fits, which is what makes this case about the ACTIONS.
    const title = heading.querySelector<HTMLElement>(".titlebar-title");
    heading.classList.remove("sr-only");
    expect(title?.scrollWidth, "the short title is not truncated").toBe(title?.clientWidth);
    heading.classList.add("sr-only");

    // Two rows here is geometry rather than a fallback — the targets do not fit one
    // row at this width and shrinking one is not on the table — and §E's point is
    // that the state is now STABLE rather than emergent.
    const bar = heading.parentElement;
    expect(bar).not.toBeNull();
    const rows = new Set(
      [...(bar?.querySelectorAll<HTMLElement>(":scope > .icon-btn") ?? [])]
        .filter((b) => b.getBoundingClientRect().width > 0)
        .map((b) => Math.round(b.getBoundingClientRect().top)),
    );
    expect(rows.size, "the actions wrapped, which is what clipped the title").toBe(2);
  });

  it("re-decides when the BAR resizes, not only when the heading does", async () => {
    // The clip is `.sr-only`, a 1x1 absolutely positioned box at every width — so an
    // observer watching only the heading goes silent exactly while the clip is in
    // force, and the clip becomes a latch that only a title change breaks. Measured
    // on the served page: a monotonic 320 -> 768 resize sweep kept the title hidden
    // at every width, where a fresh load at 768 showed it. On a phone that is a
    // rotation: portrait clips, landscape has room and never re-asks.
    const heading = mountBar("320px");
    setPageTitle("Git", "chat");
    expect(heading.classList.contains("sr-only"), "clipped while narrow").toBe(true);
    initPageTitleFit();

    /** Two frames plus a tick: the observer delivers after layout and this module
     *  defers its own write by one more frame. */
    const settle = async (): Promise<void> => {
      await new Promise<void>((r) => {
        requestAnimationFrame(() => {
          requestAnimationFrame(() => {
            setTimeout(r, 50);
          });
        });
      });
    };

    // The observation's OWN first delivery is drained here, before the width moves.
    // Without this the case cannot tell the two observers apart: that first callback
    // lands after a synchronous resize and unclips for a reason that has nothing to
    // do with which element is watched.
    await settle();
    expect(heading.classList.contains("sr-only"), "still clipped after the first delivery").toBe(
      true,
    );

    const area = document.getElementById("chat-area");
    expect(area).not.toBeNull();
    area?.style.setProperty("inline-size", "900px");
    await settle();
    expect(heading.classList.contains("sr-only"), "shown once the bar has room").toBe(false);
  });

  it("keeps the h1 and its text in the accessibility tree while clipped", () => {
    // The whole reason the clip is `.sr-only`. A phone is where this bar is the only
    // thing naming the view, so `display: none` there would leave the document with
    // no heading at all.
    const heading = mountBar("390px");
    setPageTitle(LONG, "chat");
    const title = heading.querySelector<HTMLElement>(".titlebar-title");
    expect(heading.getAttribute("aria-hidden"), "not hidden from AT").toBeNull();
    expect(title?.tagName).toBe("H1");
    expect(title?.textContent).toBe(LONG);
  });

  it("re-decides when the title changes, in both directions", () => {
    // The observer watches the heading's SIZE, and a clipped heading is 1px whatever
    // its text says — so a title change has to re-measure explicitly or the verdict
    // sticks at whatever the previous title earned.
    const heading = mountBar("768px");
    setPageTitle(LONG, "chat");
    expect(heading.classList.contains("sr-only")).toBe(false);

    setPageTitle(LONG.repeat(4), "chat");
    expect(heading.classList.contains("sr-only"), "a longer title stops fitting").toBe(true);

    setPageTitle("Git", "chat");
    expect(heading.classList.contains("sr-only"), "a short title fits again").toBe(false);
  });

  it("re-decides when a subtitle lands, because it competes for the same room", () => {
    // `setPageSubtitle` writes half the heading without touching the title, so it owns its
    // own re-measure. Both terms carry margin rather than straddling the fit boundary, which
    // moves with the font stack — 768px is where the sibling above measures it untruncated.
    const heading = mountBar("768px");
    setPageTitle(LONG, "git");
    expect(heading.classList.contains("sr-only"), "the title alone fits at 768px").toBe(false);

    setPageSubtitle("git", "Changes and section names ".repeat(12));
    expect(heading.classList.contains("sr-only"), "the subtitle pushes it over").toBe(true);
  });

  it("writes a title for a caller with no bar mounted", () => {
    // The fit is a refinement over a laid-out bar, so no bar is not an error — unlike the
    // WRITE, which must fail loudly if its target is missing. `initPageTitleFit` is
    // deliberately not in this case: it runs once from the composition root, which owns the
    // bar, so an unwired bar there is a real bug and still throws.
    document.body.innerHTML = `<h1 id="titlebar-title"></h1><span id="titlebar-subtitle"></span>`;
    setPageTitle("Git", "git");
    expect(document.getElementById("titlebar-title")?.textContent).toBe("Git");
    clearPageTitle();
    expect(document.getElementById("titlebar-title")?.textContent).toBe("");
    document.body.innerHTML = "";
  });

  it("fails loudly when the composition root wires a bar that is not there", () => {
    // The other half of that asymmetry. Boot is the one moment the bar's absence is
    // a defect rather than a caller's business.
    document.body.innerHTML = "";
    expect(() => {
      initPageTitleFit();
    }).toThrow(/titlebar-heading/);
  });
});
