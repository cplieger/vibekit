// ---------------------------------------------------------------------------
// Tests for account-usage.ts: renders the account/subscription usage into the
// status-popup footer elements. api-client + the generated decoder are mocked
// so we control the fetched payload and assert the rendered DOM.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { AccountUsage } from "./types.js";

const mockApiGetTyped = vi.fn();
vi.mock("./api-client.js", () => ({
  apiGetTyped: (...args: unknown[]) => mockApiGetTyped(...args),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGet: vi.fn(),
}));
vi.mock("./wire/decoders.gen.js", () => ({ decodeAccountUsage: vi.fn() }));

import { $ } from "./dom.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
const { loadAccountUsage } = await import("./account-usage.js");

/** Flush the fetch().then(render).finally() microtask chain. */
async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

/** The card's account row as `static/index.html` authors it. `#st-account` is the
 *  `<a>` ITSELF now — the card's one link, carrying the destination the sidebar
 *  address used to hold — with the plan and the meter in a `.pill-account-lines`
 *  stack beside the external mark. `account-usage.ts` needed no change for that:
 *  `$.stAccount` stays `HTMLElement`, its one `box.hidden = false` and all four
 *  render arms are untouched, and `data-tooltip` is written on the METER rather than
 *  on the box, so the link inherits no tooltip it should not have. */
function seedDom(): void {
  document.body.innerHTML = `
    <a id="st-account" class="pill-account" hidden
       href="https://app.kiro.dev/account/usage" target="_blank" rel="noopener">
      <span class="pill-account-lines">
        <span class="pill-account-plan" id="acct-plan"></span>
        <span class="pill-account-meter" id="acct-meter"></span>
        <span id="acct-overage"></span>
      </span>
    </a>`;
}

beforeEach(() => {
  vi.clearAllMocks();
  seedDom();
});

describe("loadAccountUsage", () => {
  it("renders plan name + credit meter with a percentage", async () => {
    const usage: AccountUsage = {
      plan_name: "KIRO POWER",
      billing_cycle_reset: "2026-08-01",
      breakdowns: [
        {
          resource_type: "CREDIT",
          display_name: "Credits",
          used: 133705,
          limit: 10000,
          percentage: 1337,
          currency: "USD",
          has_limit: true,
        },
      ],
      overages_enabled: true,
    };
    mockApiGetTyped.mockResolvedValue(usage);
    loadAccountUsage(true);
    await flush();

    expect($.stAccount.hidden).toBe(false);
    expect($.acctPlan.textContent).toBe("KIRO POWER");
    expect($.acctMeter.textContent).toContain("(1337%)");
    expect($.acctMeter.textContent).toContain("cr");
    // The reset date appears on no other surface, so the tooltip is its only
    // home — and the styled controller republishes it as aria-describedby,
    // which a native title never did.
    expect($.acctMeter.dataset["tooltip"]).toContain("2026-08-01");
  });

  it("shows 'Usage unavailable' when the fetch fails", async () => {
    mockApiGetTyped.mockResolvedValue(null);
    loadAccountUsage(true);
    await flush();
    expect($.acctPlan.textContent).toBe("Usage unavailable");
    expect($.acctMeter.textContent).toBe("");
  });

  it("marks a cached (stale) snapshot", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "KIRO POWER",
      stale: true,
      breakdowns: [],
      overages_enabled: false,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctPlan.textContent).toBe("KIRO POWER (cached)");
  });

  it("renders the note for an admin-managed plan with no breakdowns", async () => {
    mockApiGetTyped.mockResolvedValue({
      note: "Your plan is managed by admin",
      breakdowns: [],
      overages_enabled: false,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctPlan.textContent).toBe("Your plan is managed by admin");
    expect($.acctMeter.textContent).toBe("");
  });

  // The overage state is READ-ONLY (nothing in kiro-cli sets it), so the row states
  // it and its own link routes the reader to the page that can change it. Both
  // states are asserted rather than only the interesting one: `overages_enabled`
  // carries no `omitempty` precisely so a false is a STATEMENT, and a render that
  // only spoke up for `true` would be indistinguishable from the omitted field this
  // change removed.
  it("reports that overages are on", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "KIRO POWER",
      breakdowns: [],
      overages_enabled: true,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctOverage.textContent).toBe("Overages on");
  });

  it("reports that overages are off", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "KIRO POWER",
      breakdowns: [],
      overages_enabled: false,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctOverage.textContent).toBe("Overages off");
  });

  // Sequenced deliberately: the row is never re-hidden, so a failure has to CLEAR a
  // state an earlier render left behind. Asserting a blank line on a fresh failed
  // fetch passes even with the clear deleted, because the element starts empty.
  it("clears the line when a later fetch fails, rather than leaving a stale state", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "KIRO POWER",
      breakdowns: [],
      overages_enabled: true,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctOverage.textContent).toBe("Overages on");

    mockApiGetTyped.mockResolvedValue(null);
    loadAccountUsage(true);
    await flush();
    expect($.acctOverage.textContent).toBe("");
  });

  it("throttles repeat calls within the client TTL (force bypasses)", async () => {
    mockApiGetTyped.mockResolvedValue({
      plan_name: "P",
      breakdowns: [],
      overages_enabled: false,
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);
    loadAccountUsage(); // not forced, within TTL → skipped
    await flush();
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);
  });
});

// An UNRENDERED row must generate no box, and that was a live defect until this
// change. `.pill-account` declares `display: flex`, which is an AUTHOR declaration,
// and the UA sheet's `[hidden] { display: none }` loses to it on ORIGIN — so the
// `hidden` attribute this fixture and `static/index.html` both ship did nothing here
// and the empty row spent one `--sp-2` card gap on nothing before usage loaded.
// `&[hidden] { display: none }` on the rule itself is the fix, and this is what
// fails if it is removed.
//
// A real cascade rather than a style read: the question is which of two `display`
// declarations wins, which only the assembled sheet answers.
describe("the row's hidden state", () => {
  it("generates no box while it is hidden", async () => {
    const style = mountAppCSS();
    try {
      // Inside a card, because that is where the row lives and where its
      // `align-self: stretch` and negative margins mean anything.
      const card = document.createElement("span");
      card.className = "pill-expand-content pill-status-content";
      card.append(...Array.from(document.body.children));
      document.body.replaceChildren(card);

      const row = $.stAccount;
      expect(row.hidden, "the fixture ships hidden, like index.html").toBe(true);
      expect(getComputedStyle(row).display, "the attribute has to beat the author rule").toBe(
        "none",
      );
      expect(row.getBoundingClientRect().height, "no box at all").toBe(0);

      // And the other direction, so the case cannot pass by the row never rendering:
      // once `account-usage.ts` reveals it, it IS a flex row.
      mockApiGetTyped.mockResolvedValue({
        plan_name: "P",
        breakdowns: [],
        overages_enabled: false,
      } satisfies AccountUsage);
      loadAccountUsage(true);
      await flush();
      expect(row.hidden).toBe(false);
      expect(getComputedStyle(row).display).toBe("flex");
      expect(row.getBoundingClientRect().height).toBeGreaterThan(0);
    } finally {
      style.remove();
    }
  });
});
