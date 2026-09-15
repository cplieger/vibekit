// THE MERGED TRIGGER'S ACCESSIBILITY, as far as a committed test can reach.
//
// WHY THESE ARE PROXIES AND NOT AN AXE RUN: `axe-core` is NOT a dependency of
// `static-src`, and neither is `@testing-library/*` nor `dom-accessibility-api` — so
// NO committed test in this repo can compute an accessible name or run a rule
// engine. That is a capability absence, not an oversight, and the honest response is
// to assert the STRUCTURE the name is computed from and leave the axe pass to the
// `ui-qa` sidecar over the live footer and the OPEN popup.
//
// Three claims, each of which the restructure could have broken:
//
//   NO NESTED INTERACTIVE CONTENT. axe's `nested-interactive` is SERIOUS and
//   `aria-hidden` plus `tabindex="-1"` does not clear it, because a `tabindex="-1"`
//   element is still focusable by click and by script. The mark and the address are
//   non-interactive spans, and the card's real `<a>` is the trigger's SIBLING rather
//   than its descendant. `html-validate`'s `element-permitted-content` is the
//   MECHANICAL half of this (verified by planting an `<a href>`, which fails the
//   build); this is the second line of defence.
//
//   THE NAME IS FROM CONTENTS AND IS STABLE ACROSS STATES. Name = the address plus
//   the `.sr-only` subject, whitespace-joined, so it reads
//   "<address> Account and connection status" — and just the subject before whoami
//   answers or on the two arms that name nobody. `aria-expanded` is the only state
//   channel, which is the APG rule the pointer-mode toggle already follows.
//
//   THE STATE IS IN THE DESCRIPTION. `setStatus` used to write a FLIPPING
//   `aria-label` on the dot ("Connection: connecting"), which is a changing NAME on
//   what is now a disclosure trigger. It writes `data-tooltip` on the trigger
//   instead, which `tooltip.ts` republishes as `aria-describedby` on show.
import { describe, it, expect, beforeEach } from "vitest";

import indexHtml from "../static/index.html?raw";
import statusSource from "./status.ts?raw";
import { setStatus } from "./status.js";
import type { ConnectionStatus } from "./types.js";

/** The three phrases `STATUS_STYLES` publishes as the trigger's description. That
 *  table is module-private in `status.ts`, so they are spelled here AND asserted
 *  against the source below — a spelled value with no source check would drift the
 *  moment the table did. */
const TIPS: Readonly<Record<ConnectionStatus, string>> = {
  connected: "Connected",
  disconnected: "Disconnected",
  connecting: "Connecting…",
};

const SUBJECT = "Account and connection status";

/** Write the address the way `renderIdentity` does — `textContent`, empty for the two
 *  arms that name nobody. Deliberately NOT an import of `renderIdentity`: `settings.ts`
 *  reaches `shell.ts`, `files.ts` and `tools.ts`, one of which resolves `#messages` at
 *  module scope, so importing it here fails collection on a fixture that has no chat
 *  view. That writer's own behaviour is `auth-line.test.ts`'s subject; what this file
 *  needs is an address in the DOM. */
function setAddress(text: string): void {
  (document.getElementById("user-email") as HTMLElement).textContent = text;
}

/** The footer as `static/index.html` authors it, seeded so every writer this file
 *  drives can resolve its element. */
function mountFooter(): { btn: HTMLButtonElement; dot: HTMLElement; addr: HTMLElement } {
  document.body.innerHTML = `
    <div class="sidebar-footer">
      <div class="popup-anchor">
        <button type="button" id="account-btn" class="account-btn pill-expandable">
          <span id="status-dot" class="status-dot" aria-hidden="true"></span>
          <span id="user-email" class="sidebar-email"></span>
          <span class="sr-only">${SUBJECT}</span>
        </button>
        <span id="status-card" class="pill-expand-content pill-status-content hidden">
          <span class="pill-detail" id="st-ws">-</span>
          <span class="pill-sep"></span>
          <span class="pill-detail" id="st-kiro">-</span>
          <span class="pill-sep" id="st-auth-sep" hidden></span>
          <span class="pill-detail" id="st-auth" hidden></span>
          <a class="pill-account" id="st-account" hidden
             href="https://app.kiro.dev/account/usage" target="_blank" rel="noopener">
            <span class="pill-account-lines">
              <span class="pill-account-plan" id="acct-plan"></span>
              <span class="pill-account-meter" id="acct-meter"></span>
            </span>
          </a>
        </span>
      </div>
      <div class="sidebar-footer-actions">
        <button type="button" id="logout-btn" class="icon-btn" aria-label="Log out"></button>
      </div>
    </div>`;
  return {
    btn: document.getElementById("account-btn") as HTMLButtonElement,
    dot: document.getElementById("status-dot") as HTMLElement,
    addr: document.getElementById("user-email") as HTMLElement,
  };
}

beforeEach(() => {
  mountFooter();
});

describe("no nested interactive content", () => {
  it("puts nothing focusable inside the trigger", () => {
    const { btn } = mountFooter();
    const nested = btn.querySelectorAll(
      'button, a[href], input, select, textarea, [tabindex], [role="button"]',
    );
    expect(
      [...nested].map((n) => n.nodeName),
      "the trigger's children are non-interactive spans",
    ).toEqual([]);
  });

  it("keeps the card's link a SIBLING of the trigger, not a descendant", () => {
    const { btn } = mountFooter();
    const link = document.getElementById("st-account");
    expect(link, "the card carries the account link").not.toBeNull();
    expect(btn.contains(link), "the link must not nest inside the button").toBe(false);
    // And the card itself is the trigger's next sibling, which is the pattern's own
    // contract (`pill-expand.test.ts` asserts that adjacency both ways).
    expect(btn.nextElementSibling?.id).toBe("status-card");
  });

  it("authors the same structure in static/index.html", () => {
    // The fixture above could agree with itself while the shipped page did not, so the
    // page is read too. Scoped to the button's own markup.
    const at = indexHtml.indexOf('id="account-btn"');
    expect(at, "the page has the merged trigger").toBeGreaterThan(-1);
    const end = indexHtml.indexOf("</button>", at);
    const inner = indexHtml.slice(at, end);
    expect(inner, "no anchor inside the trigger").not.toMatch(/<a\b/u);
    expect(inner, "no nested button either").not.toMatch(/<button\b/u);
    expect(inner, "and no tabindex escape hatch").not.toMatch(/tabindex/u);
  });
});

describe("the name comes from contents and is stable", () => {
  it("carries no aria-label of its own", () => {
    // An `aria-label` would WIN over the button's own text, so the address would never
    // reach a screen reader. The dot's old `aria-label` is what this change removed.
    const { btn } = mountFooter();
    expect(btn.hasAttribute("aria-label")).toBe(false);
    expect(btn.hasAttribute("aria-labelledby")).toBe(false);
  });

  it("contains both the address and the subject phrase", () => {
    // CONTAINS rather than an exact phrase: the join is whitespace, and asserting the
    // exact string would pin the markup's own indentation.
    const { btn } = mountFooter();
    setAddress("someone@example.invalid");
    const text = btn.textContent ?? "";
    expect(text).toContain("someone@example.invalid");
    expect(text).toContain(SUBJECT);
  });

  it("names the SUBJECT even before whoami answers", () => {
    // The two arms that name nobody leave the address empty, so the subject is the
    // whole name — which is why it exists rather than being left to the address.
    const { btn } = mountFooter();
    setAddress(""); // what `renderIdentity` writes for `unavailable` and `signed_out`
    expect((btn.textContent ?? "").trim()).toBe(SUBJECT);
  });

  it("is byte-identical before and after aria-expanded flips", () => {
    // `aria-expanded` is the ONLY state channel. A name that changed with the state
    // would be the forbidden case; the name changing when the ADDRESS lands is not,
    // because that is the control's subject changing, like a tab's title.
    const { btn } = mountFooter();
    setAddress("someone@example.invalid");
    const before = btn.textContent;
    btn.setAttribute("aria-expanded", "true");
    expect(btn.textContent).toBe(before);
    btn.setAttribute("aria-expanded", "false");
    expect(btn.textContent).toBe(before);
  });

  it("never leaks a connection STATE into the name", () => {
    // Asserted against the three state VALUES rather than against the word "connect":
    // the subject phrase is "Account and CONNECTION status", so a substring test for
    // "connect" fails on the intended markup. None of "connected" / "disconnected" /
    // "connecting…" is a substring of the subject, so this passes on what ships and
    // still fails if `setStatus`'s string ever reaches the name.
    const { btn } = mountFooter();
    setAddress("someone@example.invalid");
    for (const status of Object.keys(TIPS) as ConnectionStatus[]) {
      setStatus(status);
      const name = (btn.textContent ?? "").toLowerCase();
      expect(name, `the name must not carry "${TIPS[status]}"`).not.toContain(
        TIPS[status].toLowerCase(),
      );
    }
  });
});

describe("the state is in the description", () => {
  it("writes a distinct data-tooltip for all three statuses", () => {
    const { btn } = mountFooter();
    const seen = new Set<string>();
    for (const status of Object.keys(TIPS) as ConnectionStatus[]) {
      setStatus(status);
      const tip = btn.dataset["tooltip"] ?? "";
      expect(tip, `${status} publishes its phrase`).toBe(TIPS[status]);
      seen.add(tip);
    }
    expect(seen.size, "three statuses, three descriptions").toBe(3);
  });

  it("leaves the mark decorative and unnamed", () => {
    // The mark is `aria-hidden` decoration whose meaning is carried by the tooltip,
    // by `#st-ws` one row inside the card, and by the live region on every settled
    // change. Its old flipping `aria-label` is what moved.
    const { dot } = mountFooter();
    for (const status of Object.keys(TIPS) as ConnectionStatus[]) {
      setStatus(status);
      expect(dot.hasAttribute("aria-label"), `${status}: the mark names nothing`).toBe(false);
    }
    expect(dot.getAttribute("aria-hidden")).toBe("true");
  });

  it("spells those three phrases in status.ts, so the table above cannot drift", () => {
    // `STATUS_STYLES` is module-private, so the values are spelled in this file — and
    // a spelled value with no source check is a copy that goes stale silently.
    for (const tip of Object.values(TIPS)) {
      expect(statusSource, `status.ts declares "${tip}"`).toContain(`"${tip}"`);
    }
    // And the retired channel is gone rather than merely unused.
    expect(statusSource, "the dot's aria-label write is deleted").not.toMatch(
      /dot\.setAttribute\(\s*"aria-label"/u,
    );
  });
});
