// T15: a server the agent reports that this page does not configure gets a
// read-only row with a provenance chip, instead of being invisible while its
// tools sit in the agent's tool list.
import { describe, expect, it, vi, beforeEach } from "vitest";

// The status fetch is the only input to the read-only name list, so it is the
// one thing mocked. Everything else in mcp-state.ts runs for real.
const statusResponse = { servers: [] as Record<string, unknown>[] };
// The replacement must carry the REAL signature, generic parameter included:
// tsconfig.test.json type-checks this file, and vi.mock's factory is checked
// against Partial<typeof module>, so a loosened `(v: unknown) => unknown` decoder
// or an unparameterised Promise<unknown> return fails to assign.
vi.mock(import("./api-client.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    apiGetTyped: async <T>(path: string, decode: Decoder<T>): Promise<T | null> =>
      path === "/api/mcp/status" ? decode(statusResponse) : null,
  };
});
// mcp-ui.ts pulls the whole actions graph in transitively; only the two members
// it calls at module scope need replacing, so the rest is the real module.
vi.mock(import("./actions/index.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    // registerCleanup returns its UNREGISTER function, so a bare `() => {}` is
    // the wrong shape as well as the wrong arity.
    registerCleanup:
      (_fn: () => void): (() => void) =>
      () => {
        /* noop: the singleton cleanup registry is process-wide */
      },
  };
});

import type { Decoder } from "./api-client.js";
import { applyOriginChip, renderForeignMeta } from "./mcp-ui.js";
import { mcpState, servers, statusSignalFor, unconfiguredNames } from "./mcp-state.js";
import type { Origin, RuntimeStatus, Server } from "./mcp-state.js";

function status(over: Partial<RuntimeStatus> = {}): RuntimeStatus {
  return { name: "s", origin: "power", state: "connected", ...over } as RuntimeStatus;
}

function configured(name: string): Server {
  return {
    id: `id-${name}`,
    name,
    transport: "stdio",
    enabled: true,
    created_at: 0,
    updated_at: 0,
  };
}

/** Let the controller's queueMicrotask + awaited fetch settle. */
async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
}

describe("applyOriginChip", () => {
  it("stays hidden for the user's own server", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, "user");
    expect(chip.hidden).toBe(true);
    expect(chip.textContent).toBe("");
  });

  it("names the Power a server came from", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, "power");
    expect(chip.hidden).toBe(false);
    expect(chip.textContent).toContain("Power");
    // The title has to say what the reader can DO about it, since the row
    // deliberately carries no edit or remove control.
    expect(chip.title).toContain("cannot edit or remove");
  });

  it("says an unattributable server is not managed here", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, "unknown");
    expect(chip.hidden).toBe(false);
    expect(chip.textContent).toContain("not managed here");
  });

  it("clears a stale chip when the origin becomes user", () => {
    const chip = document.createElement("span");
    applyOriginChip(chip, "power");
    applyOriginChip(chip, "user");
    expect(chip.hidden).toBe(true);
    expect(chip.textContent).toBe("");
    expect(chip.hasAttribute("title")).toBe(false);
  });
});

describe("renderForeignMeta", () => {
  // "disabled" must NOT read like "idle": one says the agent is not using this
  // server, the other says no chat is running.
  const cases: { state: RuntimeStatus["state"]; want: string }[] = [
    { state: "connected", want: "available to the agent" },
    { state: "needs_auth", want: "Waiting for sign-in" },
    { state: "disabled", want: "not using it" },
    { state: "idle", want: "Not connected" },
  ];
  for (const { state, want } of cases) {
    it(`${state} reads as "${want}"`, () => {
      expect(renderForeignMeta(status({ state } as Partial<RuntimeStatus>))).toContain(want);
    });
  }

  it("surfaces the failure reason", () => {
    expect(renderForeignMeta(status({ state: "failed", error: "spawn ENOENT" }))).toContain(
      "spawn ENOENT",
    );
  });
});

describe("unconfiguredNames after a status fetch", () => {
  beforeEach(() => {
    servers.clear();
    statusResponse.servers = [];
    unconfiguredNames.value = [];
  });

  it("lists a server the config list does not hold, and only that one", async () => {
    servers.setAll([configured("mine")]);
    statusResponse.servers = [
      { name: "mine", state: "connected", origin: "user" },
      { name: "from-a-power", state: "connected", origin: "power" },
      { name: "mystery", state: "disabled", origin: "unknown" },
    ];

    mcpState.refetchStatus();
    await settle();

    // Sorted, so the assertion is on content rather than on response order.
    expect(unconfiguredNames.value).toEqual(["from-a-power", "mystery"]);
  });

  it("does not double-render a server that IS configured, whatever the origin says", async () => {
    // The two fetches are independent, so /api/mcp/status can name a server the
    // config list already has a row for. A second read-only row for it would be
    // the same server twice.
    servers.setAll([configured("mine")]);
    statusResponse.servers = [{ name: "mine", state: "connected", origin: "power" }];

    mcpState.refetchStatus();
    await settle();

    expect(unconfiguredNames.value).toEqual([]);
  });

  it("empties the list once the foreign server stops being reported", async () => {
    statusResponse.servers = [{ name: "gone-later", state: "connected", origin: "power" }];
    mcpState.refetchStatus();
    await settle();
    expect(unconfiguredNames.value).toEqual(["gone-later"]);

    statusResponse.servers = [];
    mcpState.refetchStatus();
    await settle();
    expect(unconfiguredNames.value).toEqual([]);
  });
});

describe("setStatusFromEvent", () => {
  it("keeps the origin an SSE frame cannot carry", async () => {
    statusResponse.servers = [{ name: "from-a-power", state: "connected", origin: "power" }];
    mcpState.refetchStatus();
    await settle();

    // mcp_failed carries {server, error} and no origin. Falling back to "user"
    // here would strip the row's chip and imply the page can edit a server it
    // has no record of, until the next status refetch undid it.
    mcpState.setStatusFromEvent("from-a-power", {
      name: "from-a-power",
      state: "failed",
      error: "boom",
    });

    const after = statusSignalFor("from-a-power").peek();
    expect(after.origin).toBe<Origin>("power");
    expect(after.state).toBe("failed");
  });
});
