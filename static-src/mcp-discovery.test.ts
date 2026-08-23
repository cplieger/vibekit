// Unit tests for the MCP discovery signal layer (mcp-state.ts): a server's
// tools, prompts and resources are stored and read reactively by server name.
//
// `tools` joined the other two when KAS's config file became the source of
// truth: the tool names are a discovery RESULT, and they used to be persisted
// into vibekit's config record as `known_tools`.

import { describe, it, expect } from "vitest";
import { effect } from "@cplieger/reactive";
import { mcpState, discoverySignalFor } from "./mcp-state.js";
import type { MCPPromptInfo, MCPResourceInfo } from "./mcp-state.js";

const prompts: MCPPromptInfo[] = [
  { name: "Simple Prompt", prompt_name: "simple-prompt", description: "no args" },
];
const resources: MCPResourceInfo[] = [
  { name: "doc", uri: "demo://doc", mime_type: "text/markdown" },
];
const tools = ["create_issue", "search_repos"];

describe("discovery signals", () => {
  it("defaults to empty for an unknown server", () => {
    const d = discoverySignalFor("disc-unknown").value;
    expect(d.tools).toEqual([]);
    expect(d.prompts).toEqual([]);
    expect(d.resources).toEqual([]);
  });

  it("stores tools/prompts/resources set via setDiscovery", () => {
    mcpState.setDiscovery("disc-a", tools, prompts, resources);
    const d = discoverySignalFor("disc-a").value;
    expect(d.tools).toEqual(tools);
    expect(d.prompts).toHaveLength(1);
    expect(d.prompts[0]?.prompt_name).toBe("simple-prompt");
    expect(d.resources[0]?.uri).toBe("demo://doc");
  });

  it("stores tools even when the server advertises no prompts or resources", () => {
    // The common case: most MCP servers expose tools only. The empty-value
    // fast path must not swallow them.
    mcpState.setDiscovery("disc-tools-only", tools, [], []);
    expect(discoverySignalFor("disc-tools-only").value.tools).toEqual(tools);
  });

  it("resets to empty when the server advertises nothing", () => {
    mcpState.setDiscovery("disc-b", tools, prompts, resources);
    mcpState.setDiscovery("disc-b", [], [], []);
    const d = discoverySignalFor("disc-b").value;
    expect(d.tools).toEqual([]);
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
    const before = runs;
    mcpState.setDiscovery("disc-c", tools, prompts, resources);
    expect(runs).toBeGreaterThan(before);
    dispose();
  });
});
