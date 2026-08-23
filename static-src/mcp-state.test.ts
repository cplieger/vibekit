// Unit tests for mcp-state.ts: adaptStatus wire-to-domain mapping plus
// characterization of the optimistic mutation helpers (insert/remove/update)
// over the shared `servers` collection.
import { describe, it, expect, beforeEach } from "vitest";
import { effect } from "@cplieger/reactive";
import {
  adaptStatus,
  servers,
  insertConfiguredEntry,
  removeConfiguredEntry,
  updateConfiguredEntry,
} from "./mcp-state.js";
import type { Server } from "./mcp-state.js";

describe("adaptStatus", () => {
  const cases = [
    {
      name: "connected state preserves name",
      input: { name: "github", state: "connected" },
      expected: { name: "github", origin: "user", state: "connected" },
    },
    {
      name: "needs_auth with oauth_url preserves url",
      input: { name: "linear", state: "needs_auth", oauth_url: "https://auth.example.com" },
      expected: {
        name: "linear",
        origin: "user",
        state: "needs_auth",
        oauth_url: "https://auth.example.com",
        relayed: false,
      },
    },
    {
      name: "needs_auth without oauth_url defaults to empty string",
      input: { name: "sentry", state: "needs_auth" },
      expected: {
        name: "sentry",
        origin: "user",
        state: "needs_auth",
        oauth_url: "",
        relayed: false,
      },
    },
    {
      // An absent `relayed` must read as false, not as "unknown": the flag gates
      // whether the loopback-relay paste box is offered, and defaulting it true
      // would hide the only recovery path for a callback never delivered.
      name: "needs_auth without relayed defaults to not-yet-relayed",
      input: { name: "sentry", state: "needs_auth", oauth_url: "https://a.example" },
      expected: {
        name: "sentry",
        origin: "user",
        state: "needs_auth",
        oauth_url: "https://a.example",
        relayed: false,
      },
    },
    {
      name: "needs_auth carries a delivered relay through",
      input: { name: "sentry", state: "needs_auth", oauth_url: "https://a.example", relayed: true },
      expected: {
        name: "sentry",
        origin: "user",
        state: "needs_auth",
        oauth_url: "https://a.example",
        relayed: true,
      },
    },
    {
      name: "failed with error preserves error",
      input: { name: "pg", state: "failed", error: "connection refused" },
      expected: { name: "pg", origin: "user", state: "failed", error: "connection refused" },
    },
    {
      name: "failed without error defaults to empty string",
      input: { name: "redis", state: "failed" },
      expected: { name: "redis", origin: "user", state: "failed", error: "" },
    },
    {
      name: "idle state",
      input: { name: "slack", state: "idle" },
      expected: { name: "slack", origin: "user", state: "idle" },
    },
    {
      name: "unknown state falls through to idle",
      input: { name: "custom", state: "reconnecting" },
      expected: { name: "custom", origin: "user", state: "idle" },
    },
    {
      name: "empty name preserved as-is",
      input: { name: "", state: "connected" },
      expected: { name: "", origin: "user", state: "connected" },
    },
    // A server KAS reports as off. Only ever sent for one vibekit did not
    // configure, so it must survive adaptation rather than degrading to idle:
    // "off" and "no chat is running" are different rows.
    {
      name: "disabled state is a state of its own, not idle",
      input: { name: "off-server", state: "disabled", origin: "power" },
      expected: { name: "off-server", origin: "power", state: "disabled" },
    },
    // The origin drives whether the row gets edit affordances, so each value is
    // pinned separately.
    {
      name: "a power's origin is carried through",
      input: { name: "from-power", state: "connected", origin: "power" },
      expected: { name: "from-power", origin: "power", state: "connected" },
    },
    {
      name: "an unattributable origin is carried through",
      input: { name: "mystery", state: "connected", origin: "unknown" },
      expected: { name: "mystery", origin: "unknown", state: "connected" },
    },
    // Both fall back to "user", which is the safe direction: it cannot invent a
    // read-only row for a server the config list owns and offers edits for.
    {
      name: "an absent origin falls back to user",
      input: { name: "old-server", state: "connected" },
      expected: { name: "old-server", origin: "user", state: "connected" },
    },
    {
      name: "an unrecognised origin falls back to user",
      input: { name: "weird", state: "connected", origin: "sideloaded" },
      expected: { name: "weird", origin: "user", state: "connected" },
    },
  ] as const;

  for (const { name, input, expected } of cases) {
    it(name, () => {
      expect(adaptStatus(input as Parameters<typeof adaptStatus>[0])).toEqual(expected);
    });
  }
});

// Shared `servers` collection is a module singleton; reset before each test so
// each mutation helper runs against a known starting order. These exercise the
// REAL splice/fallback/dedup logic — actions/mcp.test.ts only mocks it.
describe("configured-server mutation helpers", () => {
  beforeEach(() => {
    servers.clear();
  });

  function makeServer(id: string, name = `srv-${id}`): Server {
    return {
      id,
      name,
      transport: "stdio",
      enabled: true,
      created_at: 1000,
      updated_at: 1000,
    };
  }

  const orderedIds = (): string[] => servers.items().map((s) => s.id);

  it("insertConfiguredEntry inserts at the given index", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    const c = makeServer("c");
    servers.setAll([a, c]);
    insertConfiguredEntry(b, 1);
    expect(orderedIds()).toEqual(["a", "b", "c"]);
  });

  it("insertConfiguredEntry falls back to id ordering when atIndex is out of range / omitted", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    const c = makeServer("c");

    // omitted → lexicographic id position (b sorts between a and c)
    servers.setAll([a, c]);
    insertConfiguredEntry(b);
    expect(orderedIds()).toEqual(["a", "b", "c"]);

    // atIndex past the end → fall back to id ordering
    servers.clear();
    servers.setAll([a, c]);
    insertConfiguredEntry(b, 99);
    expect(orderedIds()).toEqual(["a", "b", "c"]);

    // negative atIndex → fall back to id ordering
    servers.clear();
    servers.setAll([a, c]);
    insertConfiguredEntry(b, -1);
    expect(orderedIds()).toEqual(["a", "b", "c"]);
  });

  it("insertConfiguredEntry is idempotent when the id already exists", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    const c = makeServer("c");
    servers.setAll([a, b, c]);

    // Same id, different name + a positional hint that would otherwise move it.
    insertConfiguredEntry(makeServer("b", "CHANGED"), 0);

    // has(id) early-return: order unchanged AND the stored value is untouched.
    expect(orderedIds()).toEqual(["a", "b", "c"]);
    expect(servers.size).toBe(3);
    expect(servers.get("b")).toEqual(b);
  });

  it("insert preserves per-entity signal identity + does not re-fire unchanged rows", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    servers.setAll([a]);

    const sigA = servers.signalFor("a");
    if (sigA === undefined) {
      throw new Error("signalFor('a') missing after setAll");
    }

    let aRuns = 0;
    const dispose = effect(() => {
      void sigA.value;
      aRuns++;
    });
    expect(aRuns).toBe(1); // initial run

    // Insert b at the front: only the order (structure tier) changes. setAll
    // writes a's value back as the SAME object reference, so Object.is dedup on
    // a's per-entity signal means it never fires.
    insertConfiguredEntry(b, 0);

    expect(aRuns).toBe(1); // a's effect did NOT re-fire
    expect(orderedIds()).toEqual(["b", "a"]); // structure did change
    expect(servers.signalFor("a")).toBe(sigA); // same signal object, reused

    dispose();
  });

  it("removeConfiguredEntry returns [entry, index]; reinserting at that index restores order", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    const c = makeServer("c");
    servers.setAll([a, b, c]);

    const removed = removeConfiguredEntry("b");
    expect(removed).toEqual([b, 1]);
    expect(orderedIds()).toEqual(["a", "c"]);

    if (removed === undefined) {
      throw new Error("expected removed tuple");
    }
    insertConfiguredEntry(removed[0], removed[1]);
    expect(orderedIds()).toEqual(["a", "b", "c"]);

    expect(removeConfiguredEntry("missing")).toBeUndefined();
  });

  it("updateConfiguredEntry patches in place and returns the previous snapshot", () => {
    const a = makeServer("a");
    servers.setAll([a]);

    const prev = updateConfiguredEntry("a", { enabled: false });
    expect(prev).toEqual(a);
    expect(servers.get("a")).toEqual({ ...a, enabled: false });
    expect(updateConfiguredEntry("missing", { enabled: false })).toBeUndefined();
  });
});
