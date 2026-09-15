// THE FOOTER AT REAL PHONE VIEWPORT SIZES, and the tier no attribute can reach.
//
// This change DELETED a 60-line `width <= 48rem` block that turned the connection
// dot into a transparent 44px grid with the mark on a `::before`. The target is the
// merged trigger's own band at every tier now, so there is ONE mark rule — which
// means the phone tier is exactly where a regression would land, and a media query
// answers about the VIEWPORT, so no amount of DOM setup can stand in for a resize.
//
// FIVE READINGS, and each catches something the others cannot:
//
//   360x800 BARE and 360x800 COARSE. A real phone carries `data-pointer="coarse"`,
//   so narrow-AND-coarse is the shipped configuration; narrow-BARE is what proves
//   the no-JS fallback still answers. Nothing may differ between them, because
//   `:root[data-pointer="coarse"]` and the no-JS `:root:not([data-pointer="fine"])`
//   inside `width <= 48rem` declare the same four values.
//
//   900x400 BARE and 900x400 COARSE — the SHORT arm, and it is about ABSENCE. The
//   footer carries NO height-keyed rule, so at 900x400 with no attribute it renders
//   the DESKTOP layout at a 24px floor and only the attribute changes anything.
//
//   1024x768 as the negative control, where neither phone arm matches.
//
// THE EVIDENCE for the absence claim, recorded as a grep over `css/` rather than as
// a remembered fact: there are TWO `height <= 30rem` queries in the app, not one —
// `50-mobile.css` (`[id="pointer-mode-btn"] { display: none }`) and `15-input.css`
// (`.pill-model-effort { display: none }`) — and NEITHER body names a footer
// selector, so the short arm reaches no footer rule. That grep is asserted below, so
// a reader who later adds a height arm to the footer fails the first half rather
// than silently invalidating the second.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import { loadCSS, manifestSheets, mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
/** The size every later file in this worker expects to measure at. `page.viewport`
 *  has no getter, so it is READ off the frame rather than copied from the config. */
let entry: { readonly width: number; readonly height: number } | null = null;

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(async () => {
  style.remove();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

afterEach(() => {
  delete document.documentElement.dataset["pointer"];
  delete document.documentElement.dataset["touched"];
});

interface Footer {
  footer: HTMLElement;
  btn: HTMLButtonElement;
  dot: HTMLElement;
  addr: HTMLElement;
  logout: HTMLElement;
}

/** The phone fixture, and `.open` is load-bearing: `50-mobile.css` gives
 *  `[id="sidebar"]` `width: 100vw; transform: translateX(-100%)` under
 *  `width <= 48rem`, and only `[id="sidebar"].open` restores `translateX(0)`. Without
 *  it every `elementFromPoint` probe below answers `null` because the panel is
 *  off-screen. */
function mountFooter(email = "someone@example.invalid"): Footer {
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";
  sidebar.className = "open";

  const footer = document.createElement("div");
  footer.className = "sidebar-footer";
  const anchor = document.createElement("div");
  anchor.className = "popup-anchor";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.id = "account-btn";
  btn.className = "account-btn pill-expandable";
  const dot = document.createElement("span");
  dot.id = "status-dot";
  dot.className = "status-dot connected";
  dot.setAttribute("aria-hidden", "true");
  const addr = document.createElement("span");
  addr.id = "user-email";
  addr.className = "sidebar-email";
  addr.textContent = email;
  const subject = document.createElement("span");
  subject.className = "sr-only";
  subject.textContent = "Account and connection status";
  btn.append(dot, addr, subject);
  const card = document.createElement("span");
  card.id = "status-card";
  card.className = "pill-expand-content pill-status-content hidden";
  anchor.append(btn, card);

  const actions = document.createElement("div");
  actions.className = "sidebar-footer-actions";
  const logout = document.createElement("button");
  logout.type = "button";
  logout.id = "logout-btn";
  logout.className = "icon-btn";
  actions.appendChild(logout);
  footer.append(anchor, actions);
  sidebar.appendChild(footer);
  document.body.replaceChildren(sidebar);
  return { footer, btn, dot, addr, logout };
}

/** Resize AND ASSERT the resize: `page.viewport` has no getter, so a call that
 *  stopped moving the frame would leave every case below reporting about the
 *  project's own 1280px while still naming a phone. */
async function viewport(width: number, height: number): Promise<void> {
  await page.viewport(width, height);
  expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
    width,
    height,
  ]);
}

function tokenPx(name: string): number {
  const probe = document.createElement("div");
  probe.style.setProperty("inline-size", `var(${name})`);
  document.body.appendChild(probe);
  const v = probe.getBoundingClientRect().width;
  probe.remove();
  return v;
}

/** Every claim the footer makes, at whatever viewport and tier is in force. */
function assertFooter(label: string, floor: number): Footer {
  const f = mountFooter();
  const footerBox = f.footer.getBoundingClientRect();
  const btnBox = f.btn.getBoundingClientRect();
  const border = parseFloat(getComputedStyle(f.footer).borderTopWidth);

  expect(border, `${label}: the dotted divider survives`).toBeCloseTo(1, 1);
  expect(btnBox.height, `${label}: the trigger fills the content band`).toBeCloseTo(
    footerBox.height - border,
    0,
  );
  expect(btnBox.height, `${label}: and clears the tier's floor`).toBeGreaterThanOrEqual(floor);

  // FOUR-EDGE HIT TEST, off the corners. The band's edges vertically, the trigger's
  // own edges horizontally.
  const midX = btnBox.left + btnBox.width / 2;
  const midY = footerBox.top + border + (footerBox.height - border) / 2;
  for (const [edge, x, y] of [
    ["top", midX, footerBox.top + border + 1],
    ["bottom", midX, footerBox.bottom - 1],
    ["leading", btnBox.left + 1, midY],
    ["trailing", btnBox.right - 1, midY],
  ] as const) {
    const hit = document.elementFromPoint(x, y);
    expect(f.btn.contains(hit), `${label}: ${edge} edge answered ${hit?.nodeName ?? "null"}`).toBe(
      true,
    );
  }

  // The mark is ONE size at every tier, which is what the deleted phone block bought.
  const dotBox = f.dot.getBoundingClientRect();
  const size = tokenPx("--dot-size");
  expect(dotBox.width, `${label}: the mark is --dot-size wide`).toBeCloseTo(size, 1);
  expect(dotBox.height, `${label}: and --dot-size tall`).toBeCloseTo(size, 1);

  // The logout button keeps its own box.
  expect(
    f.logout.getBoundingClientRect().left,
    `${label}: the trigger does not reach over the logout button`,
  ).toBeGreaterThanOrEqual(btnBox.right);

  return f;
}

describe("360x800 — a phone in portrait", () => {
  it.each([
    ["bare, the no-JS fallback", undefined],
    ["coarse, the shipped configuration", "coarse"],
  ])("renders the footer correctly %s", async (_label, tier) => {
    await viewport(360, 800);
    if (tier !== undefined) {
      document.documentElement.dataset["pointer"] = tier;
    }
    // 44px either way: the no-JS arm inside `width <= 48rem` declares the same floor
    // the coarse attribute does, which is why nothing may differ between the two.
    assertFooter(`360x800 ${tier ?? "bare"}`, 44);
  });

  it("ellipsises a long address rather than overflowing the trigger", async () => {
    await viewport(360, 800);
    document.documentElement.dataset["pointer"] = "coarse";
    const { btn, addr } = mountFooter(
      "a-very-long-address-that-cannot-possibly-fit-in-a-phone-row@example.invalid",
    );
    expect(getComputedStyle(addr).textOverflow).toBe("ellipsis");
    expect(getComputedStyle(addr).overflowX).toBe("hidden");
    expect(addr.scrollWidth, "the text is wider than its box").toBeGreaterThan(addr.clientWidth);
    // The clip is INSIDE the trigger's content box, so nothing spills past it. Read
    // BEFORE the second mount below, which replaces the document and leaves these
    // elements detached — a detached box reports zeros and a detached
    // `getComputedStyle` reports empty strings, so the assertion would compare 0
    // against NaN and pass or fail for the wrong reason.
    const long = addr.getBoundingClientRect().height;
    const inner =
      btn.getBoundingClientRect().right - parseFloat(getComputedStyle(btn).paddingRight);
    expect(addr.getBoundingClientRect().right).toBeLessThanOrEqual(inner + 0.5);
    // ONE LINE, so the clip is horizontal: a wrap would make the box taller.
    const { addr: short } = mountFooter("a@b.invalid");
    expect(long, "no wrap").toBeCloseTo(short.getBoundingClientRect().height, 0);
  });

  it("widens the anchor-to-actions gap to --sp-4, the one phone rule left", async () => {
    // The whole surviving body of the `width <= 48rem` block. The mark-to-address gap
    // goes the other way (16px -> 12px) because that space belongs to the CONTROL now
    // at every tier, which is the deliberate on-screen change.
    await viewport(360, 800);
    const { footer } = mountFooter();
    expect(getComputedStyle(footer).columnGap).toBe(`${String(tokenPx("--sp-4"))}px`);
  });
});

describe("900x400 — the SHORT arm, which is about absence", () => {
  it("renders the DESKTOP layout with no pointer attribute at all", async () => {
    await viewport(900, 400);
    // THE PREMISE, asserted: this case measures the NO-attribute tier, so a leaked
    // attribute from an earlier case would make it report about a different one.
    expect(
      document.documentElement.dataset["pointer"],
      "the short arm measures the NO-attribute tier",
    ).toBeUndefined();
    expect(
      document.documentElement.dataset["touched"],
      "and no hybrid attribute either",
    ).toBeUndefined();
    // 900px is past 48rem, so no width arm matches and nothing height-keyed reaches
    // the footer: the floor is the fine tier's 24px.
    expect(tokenPx("--hit-floor")).toBeCloseTo(24, 0);
    assertFooter("900x400 bare", 24);
  });

  it("takes the coarse floor when the attribute IS present at the same size", async () => {
    // The other half: only the attribute changes anything here. Without this case the
    // one above would pass for a footer that had grown a height arm.
    await viewport(900, 400);
    document.documentElement.dataset["pointer"] = "coarse";
    expect(tokenPx("--hit-floor")).toBeCloseTo(44, 0);
    assertFooter("900x400 coarse", 44);
  });

  it("keeps the DESKTOP anchor-to-actions gap, because no height query reaches it", async () => {
    await viewport(900, 400);
    const { footer } = mountFooter();
    expect(
      getComputedStyle(footer).columnGap,
      "900px is past 48rem, so the phone gap must not apply",
    ).toBe(`${String(tokenPx("--sp-3"))}px`);
  });
});

describe("1024x768 — the negative control", () => {
  it("matches neither phone arm", async () => {
    await viewport(1024, 768);
    const { footer } = mountFooter();
    expect(getComputedStyle(footer).columnGap).toBe(`${String(tokenPx("--sp-3"))}px`);
    assertFooter("1024x768", 24);
  });
});

describe("read as source: the footer carries no height-keyed rule", () => {
  it("finds exactly TWO height <= 30rem queries in css/, neither naming the footer", () => {
    // The EVIDENCE under the short-arm cases, as a grep over every sheet rather than
    // as a remembered fact. An earlier draft of this reasoning claimed ONE such query;
    // there are two, and the CONCLUSION survives because neither body names a footer
    // selector.
    const found: { sheet: string; body: string }[] = [];
    for (const { name, css } of manifestSheets()) {
      // Comments stripped, so a prose mention of the query is not a hit.
      const bare = css.replace(/\/\*[\s\S]*?\*\//gu, " ");
      for (const m of bare.matchAll(/@media[^{]*height\s*<=\s*30rem[^{]*\{/gu)) {
        // Balance the braces from the at-rule's opening one.
        let depth = 1;
        let i = (m.index ?? 0) + m[0].length;
        const start = i;
        while (i < bare.length && depth > 0) {
          if (bare[i] === "{") {
            depth++;
          } else if (bare[i] === "}") {
            depth--;
          }
          i++;
        }
        found.push({ sheet: name, body: bare.slice(start, i - 1) });
      }
    }
    expect(
      found.map((f) => f.sheet),
      "the two height-keyed queries this app has",
    ).toEqual(["15-input.css", "50-mobile.css"]);
    for (const { sheet, body } of found) {
      expect(body, `${sheet}'s height arm names no footer selector`).not.toMatch(
        /sidebar-footer|account-btn|sidebar-email|status-dot|popup-anchor/u,
      );
    }
  });

  it("finds no height-keyed rule anywhere near the footer's own sheet", () => {
    // Narrower and cheaper: the sheet that owns every footer rule declares no
    // height-keyed query at all, so the sweep above is not the only guard.
    const bare = loadCSS("10-shell-app.css").replace(/\/\*[\s\S]*?\*\//gu, " ");
    expect(bare, "10-shell-app.css keys on width alone").not.toMatch(/@media[^{]*height\s*<=/u);
  });
});
