import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GovernanceStatePayload } from "./types.js";
import type * as ModGovernance from "./governance.js";

/** Cache-buster for the re-imports below.
 *
 * `vi.resetModules()` does not re-evaluate a module in Browser Mode: the module
 * map is URL-keyed, so a following `await import()` hands back the CACHED
 * instance and every test after the first observes stale module state. Busting
 * the specifier per evaluation is what actually mints a fresh instance. The `.ts`
 * extension is load-bearing — written `.js` the suite still passes while coverage
 * silently attributes every evaluation to a file that does not exist.
 *
 * Only the module under test is busted. Its own dependencies keep their plain
 * specifiers, so `vi.mock` still intercepts them and a shared module the test
 * also imports is the same instance the fresh module got.
 */
let bootSeq = 0;

// The REST snapshot path is exercised server-side; here we drive state purely
// through the governance_state SSE, so stub the fetch to a harmless null.
vi.mock("./api-client.js", () => ({
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

function govState(over: Partial<GovernanceStatePayload> = {}): GovernanceStatePayload {
  return {
    known: true,
    is_enterprise: false,
    features: {
      mcp_enabled: true,
      web_tools_enabled: true,
      usage_analytics: false,
      content_collection: true,
      prompt_logging: false,
      code_reference_tracker: false,
      autonomous_agents: true,
    },
    ...over,
  };
}

// Fresh module graph per test so `current`/`listeners` and the SSE bus don't
// leak between cases (mirrors settings-tabs.test.ts). Return type is inferred
// to avoid inline import() type annotations (consistent-type-imports).
async function load() {
  vi.resetModules();
  bootSeq++;
  const gov = (await import(
    /* @vite-ignore */ `./governance.ts?boot=${bootSeq}`
  )) as typeof ModGovernance;
  const bus = await import("./bus.js");
  return { gov, bus };
}

beforeEach(() => {
  document.body.innerHTML =
    '<div class="page-section" id="general-governance-section" hidden></div>';
});

describe("governance state", () => {
  it("featureDisabled is permissive until the policy is known", async () => {
    const { gov } = await load();
    expect(gov.currentGovernance()).toBeNull();
    // Unknown → never report a feature as disabled.
    expect(gov.featureDisabled("mcp_enabled")).toBe(false);
    expect(gov.featureDisabled("code_reference_tracker")).toBe(false);
  });

  it("featureDisabled reflects a known policy", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ features: { ...govState().features, mcp_enabled: false } }),
    });

    expect(gov.currentGovernance()?.known).toBe(true);
    expect(gov.featureDisabled("mcp_enabled")).toBe(true); // known + off
    expect(gov.featureDisabled("web_tools_enabled")).toBe(false); // known + on
    expect(gov.featureDisabled("code_reference_tracker")).toBe(true); // known + off
  });

  it("renders the read-only Organization-policy disclosure when known", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({ type: "governance_state", chat_id: "", payload: govState() });

    const section = document.getElementById("general-governance-section");
    expect(section).not.toBeNull();
    expect(section?.hidden).toBe(false);
    const text = section?.textContent ?? "";
    expect(text).toContain("Organization policy");
    expect(text).toContain("Individual"); // is_enterprise=false
    // Privacy-relevant flags are surfaced.
    expect(text).toContain("Prompt logging");
    expect(text).toContain("Usage analytics");
    expect(text).toContain("Content collection");
    // A privacy flag that is off renders as "Off" with the muted class.
    const offCells = section?.querySelectorAll("dd.governance-off");
    expect(offCells?.length ?? 0).toBeGreaterThan(0);
  });

  it("shows the disabledReason when present", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ is_enterprise: true, disabled_reason: "Managed by ACME IT" }),
    });
    const section = document.getElementById("general-governance-section");
    expect(section?.textContent).toContain("Enterprise (managed)");
    expect(section?.textContent).toContain("Managed by ACME IT");
  });

  it("keeps the disclosure hidden while the policy is unknown", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({ type: "governance_state", chat_id: "", payload: govState({ known: false }) });
    const section = document.getElementById("general-governance-section");
    expect(section?.hidden).toBe(true);
  });

  it("onGovernanceChange replays the current state and fires on updates", async () => {
    const { gov, bus } = await load();
    gov.initGovernance();
    bus.dispatch({ type: "governance_state", chat_id: "", payload: govState() });

    const seen: boolean[] = [];
    gov.onGovernanceChange((g) => seen.push(g.features.mcp_enabled));
    // Immediate replay of the current (mcp on) state.
    expect(seen).toEqual([true]);

    // A later change fires again.
    bus.dispatch({
      type: "governance_state",
      chat_id: "",
      payload: govState({ features: { ...govState().features, mcp_enabled: false } }),
    });
    expect(seen).toEqual([true, false]);
  });
});
