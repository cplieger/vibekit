// @vitest-environment happy-dom
// Tests for toggleServer and deleteServer optimistic + rollback.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  CancellableSlot: class {
    start() {
      return new AbortController().signal;
    }
    // eslint-disable-next-line @typescript-eslint/no-empty-function
    abort() {}
  },
  apiGetTyped: vi.fn().mockResolvedValue(null),
}));

vi.mock("../mcp-state.js", () => ({
  updateConfiguredEntry: vi.fn(),
  removeConfiguredEntry: vi.fn(),
  insertConfiguredEntry: vi.fn(),
}));

import {
  updateConfiguredEntry,
  removeConfiguredEntry,
  insertConfiguredEntry,
} from "../mcp-state.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { toggleServer, deleteServer, importServers, summariseImport } from "./mcp.js";
import type { ImportServersResult } from "./mcp.js";
import type { Server } from "../mcp-state.js";

const mockFetch = vi.fn();
const mockUpdate = vi.mocked(updateConfiguredEntry);
const mockRemove = vi.mocked(removeConfiguredEntry);
const mockInsert = vi.mocked(insertConfiguredEntry);

function makeServer(id: string, enabled = true): Server {
  return { id, name: `srv-${id}`, transport: "stdio", enabled, created_at: 1000, updated_at: 1000 };
}

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("toggleServer optimistic + rollback", () => {
  it("calls updateConfiguredEntry optimistically", async () => {
    mockUpdate.mockReturnValue(makeServer("a", true));
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await toggleServer.dispatch({ id: "a", enabled: false });
    expect(mockUpdate).toHaveBeenCalledWith("a", { enabled: false });
  });

  it("restores previous enabled state on failure", async () => {
    const prev = makeServer("a", true);
    mockUpdate.mockReturnValue(prev);
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await toggleServer.dispatch({ id: "a", enabled: false });
    // Rollback: second call restores prev.enabled
    expect(mockUpdate).toHaveBeenCalledTimes(2);
    expect(mockUpdate).toHaveBeenLastCalledWith("a", { enabled: true });
  });

  it("toggles disabled server to enabled", async () => {
    mockUpdate.mockReturnValue(makeServer("c", false));
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await toggleServer.dispatch({ id: "c", enabled: true });
    expect(mockUpdate).toHaveBeenCalledWith("c", { enabled: true });
  });

  it("rolls back enable on failure", async () => {
    const prev = makeServer("c", false);
    mockUpdate.mockReturnValue(prev);
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await toggleServer.dispatch({ id: "c", enabled: true });
    expect(mockUpdate).toHaveBeenLastCalledWith("c", { enabled: false });
  });
});

describe("deleteServer optimistic + rollback", () => {
  it("calls removeConfiguredEntry optimistically", async () => {
    const entry = makeServer("b");
    mockRemove.mockReturnValue([entry, 1]);
    mockFetch.mockResolvedValue(new Response("", { status: 204 }));
    await deleteServer.dispatch({ id: "b" });
    expect(mockRemove).toHaveBeenCalledWith("b");
  });

  it("reinserts entry at original index on failure", async () => {
    const entry = makeServer("b");
    mockRemove.mockReturnValue([entry, 1]);
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    await deleteServer.dispatch({ id: "b" });
    expect(mockInsert).toHaveBeenCalledWith(entry, 1);
  });
});

describe("importServers", () => {
  it("posts the pasted block unchanged to the import route", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ results: [{ name: "github", outcome: "created" }] }), {
        status: 200,
      }),
    );
    const block = { mcpServers: { github: { command: "npx", args: ["-y", "pkg"] } } };
    await importServers.dispatch(block);

    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/mcp/import");
    expect(init.method).toBe("POST");
    // Unchanged: the server owns the translation, so a second copy of those
    // rules here is exactly what must not exist.
    expect(JSON.parse(init.body as string)).toEqual(block);
  });

  it("reports the outcome instead of failing on an already-configured server", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ results: [{ name: "github", outcome: "unchanged" }] }), {
        status: 200,
      }),
    );
    const o = await importServers.dispatch({ mcpServers: {} }).outcome;
    expect(o.status).toBe("success");
  });
});

describe("summariseImport", () => {
  const cases: { label: string; input: ImportServersResult | null; want: string }[] = [
    {
      label: "one created",
      input: { results: [{ name: "a", outcome: "created" }] },
      want: "Connected 1 integration.",
    },
    {
      label: "several created",
      input: {
        results: [
          { name: "a", outcome: "created" },
          { name: "b", outcome: "created" },
        ],
      },
      want: "Connected 2 integrations.",
    },
    {
      label: "a re-paste says so rather than claiming a rewrite",
      input: { results: [{ name: "a", outcome: "unchanged" }] },
      want: "1 already configured.",
    },
    {
      label: "mixed",
      input: {
        results: [
          { name: "a", outcome: "created" },
          { name: "b", outcome: "unchanged" },
        ],
      },
      want: "Connected 1 integration. 1 already configured.",
    },
    {
      label: "one note is quoted verbatim, because it names the key",
      input: {
        results: [{ name: "a", outcome: "created" }],
        notes: [`server "a": ignoring "timeout": vibekit has no timeout field`],
      },
      want: `Connected 1 integration. server "a": ignoring "timeout": vibekit has no timeout field.`,
    },
    {
      label: "several notes are counted",
      input: {
        results: [{ name: "a", outcome: "created" }],
        notes: ["one", "two", "three"],
      },
      want: "Connected 1 integration. 3 keys vibekit does not store were ignored.",
    },
    { label: "nothing", input: { results: [] }, want: "Nothing to connect." },
    { label: "null result", input: null, want: "Nothing to connect." },
  ];

  for (const { label, input, want } of cases) {
    it(label, () => {
      expect(summariseImport(input)).toBe(want);
    });
  }
});
