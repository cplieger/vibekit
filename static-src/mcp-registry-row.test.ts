// A registry search row has to say two things the payload now carries: that an
// entry is deprecated (the registry still LISTS those, so without a badge a dead
// entry reads exactly like a live one), and what installing it will ask for.
//
// The row is a COMPACT DISCLOSURE since 2026-09: one line while closed, with the
// install buttons beside it rather than inside its `<summary>`. So two structural
// properties are pinned here that the old flat card had no way to get wrong — the
// row starts closed, and installing is reachable without opening it.
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

/** The row's one disclosure. */
function disc(row: HTMLElement): HTMLDetailsElement {
  const d = row.querySelector<HTMLDetailsElement>(".mcp-result-disc");
  expect(d).not.toBeNull();
  if (d === null) {
    throw new Error("no .mcp-result-disc");
  }
  return d;
}

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

  it("keeps the badge on the collapsed line and the reason behind the disclosure", () => {
    // The badge is the signal a reader must not have to click for, so it is in the
    // summary; the publisher's sentence is detail and is in the body. Replaces the
    // old card's "both are always visible", which is what a one-line row gives up.
    const row = renderRegistryResult({
      ...liveRemote,
      status: "deprecated",
      status_message: "unmaintained",
    });
    expect(row.querySelector(".mcp-result-summary > .mcp-result-status")).not.toBeNull();
    expect(row.querySelector(".mcp-result-body > .mcp-result-status-note")).not.toBeNull();
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

describe("the row as a disclosure", () => {
  it("starts closed, whatever the entry declares", () => {
    // The design property that replaced the requirements preview's `open` flag:
    // the ROW is the one thing that opens, and it opens on the reader's word. An
    // entry with a required credential is the case that used to force itself open.
    const required = renderRegistryResult({
      name: "ex/gh",
      packages: [
        {
          registry_type: "npm",
          identifier: "@ex/gh",
          env_vars: [{ name: "GITHUB_TOKEN", required: true, secret: true }],
        },
      ],
    });
    expect(disc(required).open).toBe(false);
    expect(disc(renderRegistryResult(liveRemote)).open).toBe(false);
  });

  it("has exactly one disclosure and no nested one", () => {
    // Replaces "stays closed when none are required": the requirements were a
    // second <details> inside the card, so opening a row asked the reader to open
    // something else inside it. There is one now, and the requirements are plain.
    const row = renderRegistryResult({
      name: "ex/both",
      packages: [
        { registry_type: "npm", identifier: "@ex/both", env_vars: [{ name: "A", required: true }] },
      ],
      remotes: [{ type: "http", url: "https://both/mcp", headers: [{ name: "B" }] }],
    });
    expect(row.querySelectorAll("details")).toHaveLength(1);
    expect(row.querySelectorAll("summary")).toHaveLength(1);
  });

  it("puts the install buttons outside the summary", () => {
    // Two reasons, and both are load-bearing. A <summary> maps to role=button, so
    // a <button> inside one is axe's nested-interactive (serious). And a button
    // outside the disclosure is what keeps installing reachable without expanding.
    const row = renderRegistryResult(liveRemote);
    const btn = row.querySelector<HTMLButtonElement>(".mcp-install-btn");
    expect(btn).not.toBeNull();
    expect(btn!.closest("summary")).toBeNull();
    expect(btn!.closest("details")).toBeNull();
    expect(btn!.parentElement!.className).toBe("mcp-result-actions");
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
    const preview = row.querySelector<HTMLElement>(".mcp-requires");
    expect(preview).not.toBeNull();
    expect(preview!.querySelector(".mcp-requires-label")!.textContent).toContain("Needs 1 of 2");
    const names = [...preview!.querySelectorAll(".mcp-requires-name")].map((n) => n.textContent);
    expect(names).toEqual(["GITHUB_TOKEN", "GITHUB_HOST"]);
    expect(preview!.textContent).toContain("PAT with repo scope");
    expect(preview!.querySelectorAll(".mcp-pair-mark-required")).toHaveLength(1);
  });

  it("names a remote's headers and says when none are required", () => {
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
    const preview = row.querySelector<HTMLElement>(".mcp-requires");
    expect(preview).not.toBeNull();
    expect(preview!.querySelector(".mcp-requires-label")!.textContent).toContain(
      "Optional headers (1)",
    );
    expect(preview!.querySelectorAll(".mcp-pair-mark-required")).toHaveLength(0);
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
    expect(row.querySelectorAll(".mcp-install-btn")).toHaveLength(2);
  });

  it("names the transport on the button and the identifier in its accessible name", () => {
    // The identifier left the label so the row fits one line; dropping it outright
    // would make two remotes of one kind two buttons reading the same two words.
    const row = renderRegistryResult({
      name: "ex/both",
      packages: [{ registry_type: "npm", identifier: "@ex/both" }],
      remotes: [{ type: "http", url: "https://both/mcp" }],
    });
    const btns = [...row.querySelectorAll<HTMLButtonElement>(".mcp-install-btn")];
    expect(btns.map((b) => b.textContent)).toEqual(["Use npm", "Use http"]);
    expect(btns.map((b) => b.getAttribute("aria-label"))).toEqual([
      "Use npm: @ex/both",
      "Use http: https://both/mcp",
    ]);
    // Each option's identifier is readable once the row is open.
    expect([...row.querySelectorAll(".mcp-install-id")].map((c) => c.textContent)).toEqual([
      "npm: @ex/both",
      "http: https://both/mcp",
    ]);
  });
});
