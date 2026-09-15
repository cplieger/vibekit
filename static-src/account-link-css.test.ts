// THE STATUS CARD'S ACCOUNT ROW IS THE CARD'S ONE LINK.
//
// It carries the plan and the credit meter, and clicking it opens the account page —
// the destination the sidebar address used to hold, now attached to the number it is
// about. Legal here and impossible on the trigger: the card is the trigger's SIBLING,
// so a real `<a>` nests inside no button.
//
// FOUR CLAIMS, and none of them is checkable by a style read alone.
//
//   THE TARGET, by a real `elementFromPoint`. A hit test is the only thing that sees
//   a clip, and this row is deliberately flush with a clipping card.
//
//   THE CONCENTRIC CORNER, measured on BOTH inline edges. `--card-radius` is the card
//   family's derived inner corner (outer − border − inset) and it shares the card's
//   centre only while the row is flush inside that inset on both edges. A
//   leading-edge-only assertion would have passed the shape this replaced, where
//   `width: 100%` plus `align-items: flex-start` on the card left the trailing edge
//   16px short.
//
//   THE LADDER, as two source reads plus one computed read. The surface half is the
//   app's aligned row recipe; the INK LIFT is required rather than decorative,
//   because the card is a status-TINTED composite no rule can see and the plan ink
//   fails 4.5:1 over it in three of four interaction states. The computed read is the
//   one that fails if the deleted `.pill-account-plan` colour declaration comes back.
//
//   THE CARD'S NEW BLOCK POSITION, with the popup opened through `makeExpandable`
//   rather than by clearing `hidden` — the position is written by `clampToViewport`
//   on open, so a fixture that never opens the popup measures the closed card.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { allRules, loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { makeExpandable } from "./pill-expand.js";

const input = loadCSS("15-input.css");

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  delete document.documentElement.dataset["pointer"];
});

interface Card {
  card: HTMLElement;
  link: HTMLAnchorElement;
  plan: HTMLElement;
  footer: HTMLElement;
}

/** The whole footer, with the card as the trigger's sibling inside `.popup-anchor` —
 *  which is what `.pill-status-content`'s `bottom: calc(100% + var(--sp-1))` is
 *  anchored against, so the block-position case needs the real structure rather than
 *  a card on its own. */
function mountFooter(opts: { hidden?: boolean } = {}): Card & { btn: HTMLButtonElement } {
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";
  sidebar.style.cssText = "position:fixed;top:200px;left:0;width:260px;";
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

  const card = document.createElement("span");
  card.id = "status-card";
  card.className = "pill-expand-content pill-status-content hidden";
  const detail = document.createElement("span");
  detail.className = "pill-detail";
  detail.textContent = "connected to vibekit 1.2.3";
  const link = document.createElement("a");
  link.id = "st-account";
  link.className = "pill-account";
  link.href = "https://app.kiro.dev/account/usage";
  link.target = "_blank";
  link.rel = "noopener";
  if (opts.hidden === true) {
    link.hidden = true;
  }
  const lines = document.createElement("span");
  lines.className = "pill-account-lines";
  const plan = document.createElement("span");
  plan.className = "pill-account-plan";
  plan.id = "acct-plan";
  plan.textContent = "KIRO POWER";
  const meter = document.createElement("span");
  meter.className = "pill-account-meter";
  meter.id = "acct-meter";
  meter.textContent = "412 / 1,000 credits";
  lines.append(plan, meter);
  const mark = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  mark.setAttribute("class", "ic-ui");
  mark.setAttribute("aria-hidden", "true");
  mark.setAttribute("viewBox", "0 0 24 24");
  link.append(lines, mark);
  card.append(detail, link);

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
  return { card, link, plan, footer, btn };
}

/** Open the popup the way the app does. `makeExpandable` swaps the authored `.hidden`
 *  class for the `[hidden]` attribute and `clampToViewport` writes the card's block
 *  position on open — so a fixture that clears `hidden` by hand measures the CLOSED
 *  card, which is the assertion's own premise. */
function open(btn: HTMLButtonElement, card: HTMLElement): void {
  makeExpandable(btn, card);
  btn.click();
  // SETTLE the entry transition rather than measuring a frame of it. The card animates
  // `transform: scale(0.4)` -> `scale(1)`, and `getBoundingClientRect` reports the
  // SCALED box — the first version of this file measured 15.8px where the settled row
  // is 39.6px, which is 0.4 of it, and read a 3px radius as 1.2px. Finishing the
  // animations is deterministic where waiting on frames is not (two rAFs advanced the
  // transition's clock by nothing at all here).
  for (const a of card.getAnimations()) {
    a.finish();
  }
}

const TIERS = [
  ["fine", 24],
  ["coarse", 44],
] as const;

describe("the link's target", () => {
  it.each(TIERS)("clears the %s tier's floor and answers a hit at its centre", (tier, floor) => {
    document.documentElement.dataset["pointer"] = tier;
    const { card, link, btn } = mountFooter();
    open(btn, card);
    const box = link.getBoundingClientRect();
    expect(box.height, `the link is ${box.height}px at the ${tier} tier`).toBeGreaterThanOrEqual(
      floor,
    );
    // A real hit test, never a style read: only a hit test sees the card's clip.
    const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
    expect(link.contains(hit), `centre answered ${hit?.nodeName ?? "null"}`).toBe(true);
  });

  it("generates no box at all while it is hidden", () => {
    // The ORIGIN defect this change fixes: `.pill-account` declares `display: flex`,
    // an AUTHOR declaration, and the UA sheet's `[hidden] { display: none }` loses to
    // it — so the attribute did nothing and the empty row spent one card gap on
    // nothing before usage loaded. `&[hidden] { display: none }` is required rather
    // than defensive.
    const { link, card, btn } = mountFooter({ hidden: true });
    open(btn, card);
    expect(link.hidden).toBe(true);
    expect(getComputedStyle(link).display).toBe("none");
    expect(link.getBoundingClientRect().height).toBe(0);
  });
});

describe("the link's corner and box", () => {
  it.each(TIERS)("is flush with the card's padding box on BOTH inline edges at %s", (tier) => {
    document.documentElement.dataset["pointer"] = tier;
    const { card, link, btn } = mountFooter();
    open(btn, card);
    const c = card.getBoundingClientRect();
    const cs = getComputedStyle(card);
    const l = link.getBoundingClientRect();
    // The clip box is the card's PADDING box, so the border is what the row is flush
    // WITH — measured per edge, because the shape this replaced was flush on one.
    const left = l.left - (c.left + parseFloat(cs.borderLeftWidth));
    const right = c.right - parseFloat(cs.borderRightWidth) - l.right;
    expect(left, `leading edge is ${left}px inside the padding box`).toBeCloseTo(0, 1);
    expect(right, `trailing edge is ${right}px inside the padding box`).toBeCloseTo(0, 1);
  });

  it("takes the card family's derived inner corner", () => {
    // `--card-radius` = `--r-lg` − 1px − `--card-inset` = 3px, and it is concentric
    // with the card's own corner ONLY while the row is flush inside that inset on both
    // edges, which the case above measures.
    const { card, link, btn } = mountFooter();
    open(btn, card);
    const probe = document.createElement("div");
    card.appendChild(probe);
    probe.style.setProperty("inline-size", "var(--card-radius)");
    const token = probe.getBoundingClientRect().width;
    probe.remove();
    expect(token, "--card-radius resolves inside the card").toBeGreaterThan(0);
    expect(parseFloat(getComputedStyle(link).borderTopLeftRadius)).toBeCloseTo(token, 1);
  });
});

describe("the card's block position after the anchor started stretching", () => {
  it.each(TIERS)("puts the card's bottom one --sp-1 above the footer's band at %s", (tier) => {
    // ACCEPTED CONSEQUENCE, pinned rather than waved through. `.pill-status-content`
    // is `bottom: calc(100% + var(--sp-1))` against `.popup-anchor`, so `100%` is the
    // ANCHOR's height — and `align-self: stretch` changes it from the mark's 8px box
    // to the footer's whole content band. The card RISES ~21.5px on desktop, and it
    // stops overlapping its own trigger by the 17.5px it used to. That was tolerable
    // while the trigger was an 8px disc under the card; it is not once the trigger is
    // a 240px row painting a hover wash and a press wash.
    //
    // A CLAIM ABOUT THE NEW GEOMETRY rather than a snapshot of a number: the card's
    // bottom edge sits `--sp-1` above the footer's own content-box top.
    document.documentElement.dataset["pointer"] = tier;
    const { card, footer, btn } = mountFooter();
    open(btn, card);
    const gapProbe = document.createElement("div");
    footer.appendChild(gapProbe);
    gapProbe.style.setProperty("block-size", "var(--sp-1)");
    const sp1 = gapProbe.getBoundingClientRect().height;
    gapProbe.remove();
    expect(sp1).toBeGreaterThan(0);

    const f = footer.getBoundingClientRect();
    const border = parseFloat(getComputedStyle(footer).borderTopWidth);
    const bandTop = f.top + border;
    expect(
      card.getBoundingClientRect().bottom,
      `the card's bottom against the band top ${bandTop}`,
    ).toBeCloseTo(bandTop - sp1, 0);
  });
});

describe("the ladder", () => {
  it("declares the surface AND the ink on hover, inside an any-hover at-rule", () => {
    // Two source assertions in one, because the two halves are one decision: the wash
    // alone cannot carry `--c-text-secondary` on this card at any depth.
    const body = ruleContaining(input, ".pill-account", "top").body;
    const hover = /@media\s*\(any-hover:\s*hover\)\s*\{\s*&:hover\s*\{([^}]*)\}/u.exec(body);
    expect(hover, "the hover sits inside an any-hover at-rule").not.toBeNull();
    expect(hover?.[1], "the app's aligned row wash").toMatch(/background:\s*var\(--c-hover\)/u);
    expect(hover?.[1], "and the ink lift, which the measurement requires").toMatch(
      /color:\s*var\(--c-text-primary\)/u,
    );
    expect(body, "never the primary-input query").not.toMatch(/@media\s*\(hover:\s*hover\)/u);
  });

  it("declares the same pair one rung deeper on press", () => {
    // A bare `a[href]` is deliberately OUT of 03-base.css's universal press, so a link
    // styled as a row declares its own — and on touch there is no hover rule at all,
    // so the press is the ONLY feedback a finger gets and has to carry both channels.
    const body = ruleContaining(input, ".pill-account", "top").body;
    const active = /&:active\s*\{([^}]*)\}/u.exec(body);
    expect(active, "the row declares its own press").not.toBeNull();
    expect(active?.[1]).toMatch(/background:\s*var\(--c-press\)/u);
    expect(active?.[1]).toMatch(/color:\s*var\(--c-text-primary\)/u);
  });

  it("lets the plan line INHERIT its resting ink rather than declaring it", () => {
    // The computed read, and the one that fails if the deleted
    // `.pill-account-plan { color: var(--c-text-secondary) }` rule comes back. That
    // declaration restated exactly what the row inherits from `.pill-expand-content`,
    // so deleting it changes nothing at rest — and it is what lets the ink lift above
    // reach the plan line at all, because a declared colour wins against an inherited
    // value and would pin the failing ink.
    const { card, link, plan, btn } = mountFooter();
    open(btn, card);
    expect(getComputedStyle(plan).color, "the plan line reads the row's ink").toBe(
      getComputedStyle(link).color,
    );
    expect(getComputedStyle(link).color, "which is the card's own ink at rest").toBe(
      getComputedStyle(card).color,
    );
    // And as source, because the computed halves would still agree if the rule were
    // reinstated with the same value — the point is that there is ONE writer.
    expect(input, "no .pill-account-plan colour rule").not.toMatch(
      /\.pill-account-plan\s*\{[^}]*color:/u,
    );
  });

  it("gives the external mark no colour of its own", () => {
    // Measured requirement rather than tidiness: `--c-text-tertiary` reads 2.426/3.134
    // on the hover wash and 1.786/2.525 on the press one over the tinted card, so a
    // tertiary mark sits under 1.4.11's 3:1 in three of four states. Inheriting the
    // row is the only value that passes them all, and an ABSENT declaration cannot
    // drift.
    // Read as SELECTORS rather than as text: this stylesheet's own comment explains
    // why the class does not exist, so any substring test matches that comment and
    // fails for the wrong reason. `allRules` parses, so it sees only real selectors.
    const selectors = allRules(input).map((r) => r.selector);
    expect(selectors.length, "the sheet parses").toBeGreaterThan(10);
    expect(
      selectors.filter((sel) => sel.includes("pill-account-out")),
      "no .pill-account-out rule exists",
    ).toEqual([]);
    const { card, link, btn } = mountFooter();
    open(btn, card);
    const mark = link.querySelector("svg");
    expect(mark, "the row carries the external mark").not.toBeNull();
    expect(getComputedStyle(mark as Element).color, "the mark inherits the row's ink").toBe(
      getComputedStyle(link).color,
    );
  });
});
