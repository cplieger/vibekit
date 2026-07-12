// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
import { applyMcpGovernance } from "./mcp-ui.js";
import type { GovernanceStatePayload } from "./types.js";

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

function fixtures(): { add: HTMLButtonElement; notice: HTMLParagraphElement } {
  const add = document.createElement("button");
  const notice = document.createElement("p");
  notice.hidden = true;
  return { add, notice };
}

describe("applyMcpGovernance", () => {
  it("disables the add affordance + shows the notice when MCP is org-disabled", () => {
    const { add, notice } = fixtures();
    applyMcpGovernance(
      govState({ features: { ...govState().features, mcp_enabled: false } }),
      add,
      notice,
    );
    expect(add.disabled).toBe(true);
    expect(add.getAttribute("data-tooltip")).toContain("disabled by your organization");
    expect(notice.hidden).toBe(false);
    expect(notice.textContent).toContain("disabled by your organization");
  });

  it("includes the disabledReason in the notice when present", () => {
    const { add, notice } = fixtures();
    applyMcpGovernance(
      govState({
        disabled_reason: "Blocked by policy X",
        features: { ...govState().features, mcp_enabled: false },
      }),
      add,
      notice,
    );
    expect(notice.textContent).toContain("Blocked by policy X");
  });

  it("leaves the affordance enabled when MCP is allowed", () => {
    const { add, notice } = fixtures();
    applyMcpGovernance(govState(), add, notice);
    expect(add.disabled).toBe(false);
    expect(notice.hidden).toBe(true);
  });

  it("stays permissive while the policy is unknown", () => {
    const { add, notice } = fixtures();
    // Known=false → treat as unknown: don't disable even though mcp_enabled is false.
    applyMcpGovernance(
      govState({ known: false, features: { ...govState().features, mcp_enabled: false } }),
      add,
      notice,
    );
    expect(add.disabled).toBe(false);
    expect(notice.hidden).toBe(true);
  });
});
