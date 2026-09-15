// THE FOOTER'S DIVIDER, in both themes.
//
// With many tabs the strip runs right up to the footer and the two read as one
// column. `.sidebar-footer` carries a `border-block-start` that separates them, and
// three properties are worth pinning because each could regress independently:
//
//   IT IS SOLID, matching the header's own hairline and the panel's trailing edge,
//   so the panel's internal boundaries are ONE treatment (user call, 2026-09-12).
//   It shipped dotted for a day on the reasoning that a second solid rule would
//   read as a repeated one; the repetition is the point.
//
//   IT COSTS THE BAND NOTHING. `box-sizing: border-box` (02-reset.css) means the
//   border is spent OUT of `--sidebar-band-h` exactly as the header's is, so both of
//   the panel's ends still render the same box while both content bands become
//   box − 1. `sidebar-band-css.test.ts` owns the equality; this file owns the
//   divider's own three declarations and the per-theme colour.
//
//   IT RE-RESOLVES PER THEME FROM ONE DECLARATION. `--c-border` is a `color-mix`
//   over `--c-text-primary` declared once in `01-tokens.css`, and the light block
//   redeclares that ink — so a per-theme literal on either end is the drift to catch,
//   and it is caught as SOURCE because a computed read cannot tell a token from a
//   literal that currently agrees with it.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { loadCSS, manifestSheets, mountAppCSS } from "./__test-helpers__/css-rules.js";

const shell = loadCSS("10-shell-app.css");

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  delete document.documentElement.dataset["theme"];
  delete document.documentElement.dataset["pointer"];
  delete document.documentElement.dataset["touched"];
});

/** The panel's two ends, each with its own identity element and trailing button —
 *  the shape the shared band is about. */
function mountSidebar(): { header: HTMLElement; footer: HTMLElement } {
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";

  const header = document.createElement("div");
  header.className = "sidebar-header";
  const logo = document.createElement("div");
  logo.className = "logo";
  logo.textContent = "vibekit";
  const headerActions = document.createElement("div");
  headerActions.className = "sidebar-header-actions";
  const settings = document.createElement("button");
  settings.type = "button";
  settings.className = "icon-btn";
  headerActions.appendChild(settings);
  header.append(logo, headerActions);

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
  addr.textContent = "someone@example.invalid";
  btn.append(dot, addr);
  anchor.appendChild(btn);
  const footerActions = document.createElement("div");
  footerActions.className = "sidebar-footer-actions";
  const logout = document.createElement("button");
  logout.type = "button";
  logout.id = "logout-btn";
  logout.className = "icon-btn";
  footerActions.appendChild(logout);
  footer.append(anchor, footerActions);

  sidebar.append(header, footer);
  document.body.replaceChildren(sidebar);
  return { header, footer };
}

const THEMES = ["dark", "light"] as const;
const TIERS = ["fine", "coarse"] as const;

describe("the footer's divider", () => {
  it.each(THEMES)("is a 1px SOLID line in the %s theme", (theme) => {
    if (theme === "light") {
      document.documentElement.dataset["theme"] = "light";
    }
    const { footer } = mountSidebar();
    const cs = getComputedStyle(footer);
    expect(parseFloat(cs.borderTopWidth)).toBeCloseTo(1, 1);
    expect(cs.borderTopStyle, "solid, matching the panel's other boundaries").toBe("solid");
    expect(cs.borderTopColor, "and it paints something").not.toBe("rgba(0, 0, 0, 0)");
  });

  it.each(THEMES)("takes the header's own hairline colour in the %s theme", (theme) => {
    // ONE token at both ends: the divider and the header's hairline are the same kind
    // of edge, so a reader learns one colour.
    if (theme === "light") {
      document.documentElement.dataset["theme"] = "light";
    }
    const { header, footer } = mountSidebar();
    expect(getComputedStyle(footer).borderTopColor).toBe(
      getComputedStyle(header).borderBottomColor,
    );
  });

  it.each(THEMES)("costs the band nothing in the %s theme", (theme) => {
    // The boxes still agree AND both content bands are box − 1, which is the symmetry
    // the border-box model buys. Asserted here per THEME, because a per-theme colour
    // literal is the drift the source case below catches and a per-theme WIDTH would
    // show up right here.
    if (theme === "light") {
      document.documentElement.dataset["theme"] = "light";
    }
    const { header, footer } = mountSidebar();
    const hBox = header.getBoundingClientRect().height;
    const fBox = footer.getBoundingClientRect().height;
    expect(hBox, `header ${hBox}px against footer ${fBox}px`).toBeCloseTo(fBox, 1);
    expect(header.clientHeight).toBeCloseTo(hBox - 1, 1);
    expect(footer.clientHeight).toBeCloseTo(fBox - 1, 1);
  });

  it.each(TIERS)("keeps all of that at the %s pointer tier", (tier) => {
    // The band's height is tier-dependent through the hit floor lifting its buttons,
    // so the border being spent out of it is worth checking at both.
    document.documentElement.dataset["pointer"] = tier;
    const { header, footer } = mountSidebar();
    expect(parseFloat(getComputedStyle(footer).borderTopWidth)).toBeCloseTo(1, 1);
    expect(header.getBoundingClientRect().height).toBeCloseTo(
      footer.getBoundingClientRect().height,
      1,
    );
  });
});

describe("read as source: neither end spells a colour", () => {
  it("declares the divider from a token, so it re-resolves per theme", () => {
    // A computed read cannot answer this: a per-theme literal that happens to equal
    // the token today passes every assertion above and drifts on the next retune.
    // `--c-border` is declared once and the light block redeclares the ink it mixes,
    // so nothing is added to the light block at all.
    const footerRule = /\n\.sidebar-footer \{([^}]*)\}/u.exec(shell);
    expect(footerRule, ".sidebar-footer's rule is in this sheet").not.toBeNull();
    const body = footerRule?.[1] ?? "";
    expect(body, "the divider reads the shared border token").toMatch(
      /border-block-start:[^;]*var\(--c-border\)/u,
    );
    expect(body, "and the fleet's hairline width token").toMatch(
      /border-block-start:\s*var\(--hairline\)/u,
    );
    expect(body, "no colour literal").not.toMatch(
      /border-block-start:[^;]*(#[0-9a-f]{3,8}|oklch|rgb)/iu,
    );
  });

  it("adds nothing to ANY light theme block for it", () => {
    // The whole point of deriving the colour: a `:root[data-theme="light"]` rule
    // naming `.sidebar-footer` would be a second writer of one edge.
    //
    // Swept over EVERY sheet rather than this one, and that is not pedantry — the
    // first version of this case searched `10-shell-app.css` alone, which carries no
    // light block at all, so it asserted nothing and passed over nothing. A light
    // override could legitimately be written in any slice.
    const sheets = manifestSheets();
    expect(sheets.length, "the manifest resolves").toBeGreaterThan(1);
    let lightBlocks = 0;
    for (const { name, css } of sheets) {
      for (const block of css.match(/:root\[data-theme="light"\][^{]*\{[\s\S]*?\n\}/gu) ?? []) {
        lightBlocks++;
        expect(block, `${name} restates the footer in a light block`).not.toMatch(
          /sidebar-footer/u,
        );
      }
    }
    // The premise: there ARE light blocks, so the sweep above ran over something.
    expect(lightBlocks, "the app has a light theme block to sweep").toBeGreaterThan(0);
  });
});
