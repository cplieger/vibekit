// Unit tests for mcp-state.ts: adaptStatus wire-to-domain mapping plus
// characterization of the optimistic mutation helpers (insert/remove/update)
// over the shared `servers` collection.
import { describe, it, expect, beforeEach } from "vitest";
import { effect, flushSync } from "@cplieger/reactive";
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
      expected: { name: "github", state: "connected" },
    },
    {
      name: "needs_auth with oauth_url preserves url",
      input: { name: "linear", state: "needs_auth", oauth_url: "https://auth.example.com" },
      expected: { name: "linear", state: "needs_auth", oauth_url: "https://auth.example.com" },
    },
    {
      name: "needs_auth without oauth_url defaults to empty string",
      input: { name: "sentry", state: "needs_auth" },
      expected: { name: "sentry", state: "needs_auth", oauth_url: "" },
    },
    {
      name: "failed with error preserves error",
      input: { name: "pg", state: "failed", error: "connection refused" },
      expected: { name: "pg", state: "failed", error: "connection refused" },
    },
    {
      name: "failed without error defaults to empty string",
      input: { name: "redis", state: "failed" },
      expected: { name: "redis", state: "failed", error: "" },
    },
    {
      name: "idle state",
      input: { name: "slack", state: "idle" },
      expected: { name: "slack", state: "idle" },
    },
    {
      name: "unknown state falls through to idle",
      input: { name: "custom", state: "reconnecting" },
      expected: { name: "custom", state: "idle" },
    },
    {
      name: "empty name preserved as-is",
      input: { name: "", state: "connected" },
      expected: { name: "", state: "connected" },
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
    flushSync();

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
