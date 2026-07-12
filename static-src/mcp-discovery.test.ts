// Unit tests for the MCP discovery signal layer (mcp-state.ts): per-server
// prompts/resources are stored and read reactively by server name.

import { describe, it, expect } from "vitest";
import { effect, flushSync } from "@cplieger/reactive";
import { mcpState, discoverySignalFor } from "./mcp-state.js";
import type { MCPPromptInfo, MCPResourceInfo } from "./mcp-state.js";

const prompts: MCPPromptInfo[] = [
  { name: "Simple Prompt", prompt_name: "simple-prompt", description: "no args" },
];
const resources: MCPResourceInfo[] = [
  { name: "doc", uri: "demo://doc", mime_type: "text/markdown" },
];

describe("discovery signals", () => {
  it("defaults to empty prompts/resources for an unknown server", () => {
    const d = discoverySignalFor("disc-unknown").value;
    expect(d.prompts).toEqual([]);
    expect(d.resources).toEqual([]);
  });

  it("stores prompts/resources set via setDiscovery", () => {
    mcpState.setDiscovery("disc-a", prompts, resources);
    const d = discoverySignalFor("disc-a").value;
    expect(d.prompts).toHaveLength(1);
    expect(d.prompts[0]?.prompt_name).toBe("simple-prompt");
    expect(d.resources[0]?.uri).toBe("demo://doc");
  });

  it("resets to empty when given no prompts/resources", () => {
    mcpState.setDiscovery("disc-b", prompts, resources);
    mcpState.setDiscovery("disc-b", [], []);
    const d = discoverySignalFor("disc-b").value;
    expect(d.prompts).toEqual([]);
    expect(d.resources).toEqual([]);
  });

  it("fires the per-server signal so a subscribed row re-renders", () => {
    let runs = 0;
    const dispose = effect(() => {
      // Read .value to subscribe this effect to the server's discovery signal.
      void discoverySignalFor("disc-c").value.prompts.length;
      runs++;
    });
    flushSync();
    const before = runs;
    mcpState.setDiscovery("disc-c", prompts, resources);
    flushSync();
    expect(runs).toBeGreaterThan(before);
    dispose();
  });
});
