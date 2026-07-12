// @vitest-environment happy-dom
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
}));
vi.mock("./wire/decoders.gen.js", () => ({ decodeAccountUsage: vi.fn() }));

import { $ } from "./dom.js";
const { loadAccountUsage } = await import("./account-usage.js");

/** Flush the fetch().then(render).finally() microtask chain. */
async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

function seedDom(): void {
  document.body.innerHTML = `
    <span id="st-account" hidden>
      <span id="acct-plan"></span>
      <span id="acct-meter"></span>
    </span>`;
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
    };
    mockApiGetTyped.mockResolvedValue(usage);
    loadAccountUsage(true);
    await flush();

    expect($.stAccount.hidden).toBe(false);
    expect($.acctPlan.textContent).toBe("KIRO POWER");
    expect($.acctMeter.textContent).toContain("(1337%)");
    expect($.acctMeter.textContent).toContain("cr");
    expect($.acctMeter.title).toContain("2026-08-01");
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
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctPlan.textContent).toBe("KIRO POWER (cached)");
  });

  it("renders the note for an admin-managed plan with no breakdowns", async () => {
    mockApiGetTyped.mockResolvedValue({
      note: "Your plan is managed by admin",
      breakdowns: [],
    } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect($.acctPlan.textContent).toBe("Your plan is managed by admin");
    expect($.acctMeter.textContent).toBe("");
  });

  it("throttles repeat calls within the client TTL (force bypasses)", async () => {
    mockApiGetTyped.mockResolvedValue({ plan_name: "P", breakdowns: [] } satisfies AccountUsage);
    loadAccountUsage(true);
    await flush();
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);
    loadAccountUsage(); // not forced, within TTL → skipped
    await flush();
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);
  });
});
