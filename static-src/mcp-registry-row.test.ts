// @vitest-environment happy-dom
// A registry search row has to say two things the payload now carries: that an
// entry is deprecated (the registry still LISTS those, so without a badge a dead
// entry reads exactly like a live one), and what installing it will ask for.
import { describe, it, expect, vi } from "vitest";

vi.mock("./dom.js", () => ({
  byId: () => document.createElement("div"),
}));
vi.mock("./actions/mcp.js", () => ({
  searchRegistry: { cancel: () => undefined, dispatch: async () => null },
}));
vi.mock("./actions/index.js", () => ({
  subscribeToActions: () => () => undefined,
  bindLoadingState: () => () => undefined,
  debouncedDispatch: () => Object.assign(() => undefined, { cancel: () => undefined }),
  registerCleanup: () => undefined,
}));

import { renderRegistryResult } from "./mcp-panels-search.js";
import type { RegistrySearchResult } from "./actions/mcp.js";

type Entry = RegistrySearchResult["servers"][number];

const liveRemote: Entry = {
  name: "ex/live",
  title: "Live",
  version: "2.0.0",
  description: "still maintained",
  remotes: [{ type: "http", url: "https://live/mcp" }],
};

describe("deprecated flag on a registry row", () => {
  it("badges a deprecated entry and shows the publisher's reason", () => {
    const row = renderRegistryResult({
      ...liveRemote,
      name: "ex/dead",
      status: "deprecated",
      status_message: "unmaintained; use ex/live instead",
    });
    const badge = row.querySelector(".mcp-result-status");
    expect(badge).not.toBeNull();
    expect(badge!.textContent).toBe("deprecated");
    expect(row.classList.contains("mcp-result-deprecated")).toBe(true);
    expect(row.querySelector(".mcp-result-status-note")!.textContent).toContain("use ex/live");
  });

  it("falls back to naming the status when the publisher gave no reason", () => {
    const row = renderRegistryResult({ ...liveRemote, status: "deleted" });
    expect(row.querySelector(".mcp-result-status")!.textContent).toBe("deleted");
    expect(row.querySelector(".mcp-result-status-note")!.textContent).toContain("deleted");
  });

  it("leaves a live entry unbadged", () => {
    const row = renderRegistryResult(liveRemote);
    expect(row.querySelector(".mcp-result-status")).toBeNull();
    expect(row.querySelector(".mcp-result-status-note")).toBeNull();
    expect(row.classList.contains("mcp-result-deprecated")).toBe(false);
  });
});

describe("install preview on a registry row", () => {
  it("names the required env vars a package install will need", () => {
    const row = renderRegistryResult({
      name: "ex/gh",
      version: "1.0.0",
      packages: [
        {
          registry_type: "npm",
          identifier: "@ex/gh",
          env_vars: [
            {
              name: "GITHUB_TOKEN",
              description: "PAT with repo scope",
              required: true,
              secret: true,
            },
            { name: "GITHUB_HOST", description: "for GHES" },
          ],
        },
      ],
    });
    const preview = row.querySelector<HTMLDetailsElement>(".mcp-requires");
    expect(preview).not.toBeNull();
    // Open by default when something is required: a closed disclosure does not
    // tell the user the token exists, which is the failure this fixes.
    expect(preview!.open).toBe(true);
    expect(preview!.querySelector("summary")!.textContent).toContain("Needs 1 of 2");
    const names = [...preview!.querySelectorAll(".mcp-requires-name")].map((n) => n.textContent);
    expect(names).toEqual(["GITHUB_TOKEN", "GITHUB_HOST"]);
    expect(preview!.textContent).toContain("PAT with repo scope");
    expect(preview!.querySelectorAll(".mcp-pair-mark-required")).toHaveLength(1);
  });

  it("names a remote's headers and stays closed when none are required", () => {
    const row = renderRegistryResult({
      name: "ex/remote",
      remotes: [
        {
          type: "http",
          url: "https://remote/mcp",
          headers: [{ name: "X-Tenant", description: "optional tenant id" }],
        },
      ],
    });
    const preview = row.querySelector<HTMLDetailsElement>(".mcp-requires");
    expect(preview).not.toBeNull();
    expect(preview!.open).toBe(false);
    expect(preview!.querySelector("summary")!.textContent).toContain("Optional headers (1)");
  });

  it("shows no preview when the publisher declared nothing", () => {
    const row = renderRegistryResult(liveRemote);
    expect(row.querySelector(".mcp-requires")).toBeNull();
    // The install button is still there: nothing to configure is a valid answer.
    expect(row.querySelector(".mcp-install-btn")).not.toBeNull();
  });

  it("keeps one preview per install option", () => {
    const row = renderRegistryResult({
      name: "ex/both",
      packages: [
        { registry_type: "npm", identifier: "@ex/both", env_vars: [{ name: "A", required: true }] },
      ],
      remotes: [
        { type: "http", url: "https://both/mcp", headers: [{ name: "B", required: true }] },
      ],
    });
    expect(row.querySelectorAll(".mcp-install-option")).toHaveLength(2);
    expect(row.querySelectorAll(".mcp-requires")).toHaveLength(2);
  });
});
