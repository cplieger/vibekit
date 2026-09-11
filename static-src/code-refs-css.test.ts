// ---------------------------------------------------------------------------
// THE LICENCE-ATTRIBUTION LINK KEEPS A POINTER-SIZED TARGET, and the app-wide
// floor is structurally unable to give it one.
//
// `code-refs.ts` builds every reference as an `<li class="code-refs-item">`, so
// 61-mcp-tools.css's WCAG 2.5.8 inline exception (`:where(p, li, td, dd, …)
// :where(a[href])`) zeroes the floor for this anchor — both rules score zero, and
// the exception sits later in the bundle. Item 9 deleted a width-keyed
// `@media (width <= 40rem)` block that spelled 44px here, on the premise that the
// `<summary>` above keeps the floor and this link is prose. Both halves are true,
// and together they were the defect: nothing replaced the literal, so the link
// measured 20px on a coarse pointer and 17.59px on a fine one at every width —
// under the 44px coarse floor and under 2.5.8's own 24px. `.code-refs-link`
// restates the floor at (0,1,0), and this is what says so.
//
// Numeric and in real layout, because none of it is legible in source: the
// exemption arrives from another stylesheet at zero specificity, so the link's own
// rule reads correct with or without it. The two controls are what keep the claim
// from passing for the wrong reason — a bare anchor OUTSIDE prose gives the tier's
// floor without restating a number, and a bare anchor inside an `<li>` proves the
// exception still reaches this position, so the target below is the link's own
// declaration rather than the floor having quietly started to apply here.
//
// Built by the real `syncCodeReferences`, in the chain `messages.ts` mounts it in
// (`.turn > .turn-body > .msg-wrap > .msg-row`), so the structure cannot drift from
// production behind the test.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { syncCodeReferences } from "./code-refs.js";
import type { Message } from "./types.js";

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

/** A bare anchor's height, in the host directly or nested in an `<li>`. It carries
 *  ONE authored declaration, `display: inline-flex`, because `min-height` does not
 *  apply to a non-replaced inline box and would make both positions read the same
 *  line box — `.code-refs-link` is `inline-flex` for its own layout reasons, so the
 *  probes match the subject on the one property that lets a floor land at all. The
 *  two calls differ only in POSITION, which is what the exemption keys on, and
 *  neither restates a number. */
function bareAnchorPx(inList: boolean): number {
  const probe = document.createElement("a");
  probe.href = "https://example.com/";
  probe.textContent = "x";
  probe.style.display = "inline-flex";
  const mount = document.createElement(inList ? "li" : "div");
  mount.append(probe);
  host.append(mount);
  const h = probe.getBoundingClientRect().height;
  mount.remove();
  return h;
}

/** The footnote as the transcript mounts it, returning the attribution link. */
function attributionLink(): HTMLAnchorElement {
  const turn = document.createElement("div");
  turn.className = "turn";
  const body = document.createElement("div");
  body.className = "turn-body";
  const wrap = document.createElement("div");
  wrap.className = "msg-wrap";
  const row = document.createElement("div");
  row.className = "msg-row";
  wrap.append(row);
  body.append(wrap);
  turn.append(body);
  host.append(turn);

  const m: Message = {
    id: "m1",
    role: "assistant",
    ts: 0,
    content: "code",
    code_references: [
      {
        license_name: "MIT",
        repository: "github.com/foo/bar",
        url: "https://github.com/foo/bar",
      },
    ],
  };
  syncCodeReferences(row, m);
  const link = row.querySelector<HTMLAnchorElement>(".code-refs-link");
  expect(link, "the footnote built its link").not.toBeNull();
  return link as HTMLAnchorElement;
}

describe("the licensed-code attribution link", () => {
  it.each(["fine", "coarse"] as const)(
    "keeps the %s tier's hit-target floor, which the inline exception withholds",
    (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const floor = bareAnchorPx(false);
      const exempt = bareAnchorPx(true);
      const link = attributionLink();

      // The premise: the exception still reaches an anchor nested in an `<li>`, so
      // the floor is genuinely absent at this position and the assertion below is
      // about the link's own rule.
      expect(exempt, "a bare anchor in an <li> is exempt").toBeLessThan(floor);
      // The claim. 20px against a 44px floor before this rule existed.
      expect(
        link.getBoundingClientRect().height,
        "the attribution link's box",
      ).toBeGreaterThanOrEqual(floor);
    },
  );

  it("is a 44px target under a finger, which is the number the exception cost", () => {
    // The coarse tier stated absolutely, because it is the WCAG 2.5.5 / Apple HIG
    // figure this whole rule exists for and a floor probe alone would pass at any
    // value the two happened to share.
    document.documentElement.dataset["pointer"] = "coarse";
    expect(attributionLink().getBoundingClientRect().height).toBe(44);
  });
});
