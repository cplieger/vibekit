// @vitest-environment happy-dom
// Tests for toggleServer and deleteServer optimistic + rollback.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  CancellableSlot: class {
    start() {
      return new AbortController().signal;
    }
    // eslint-disable-next-line @typescript-eslint/no-empty-function
    abort() {}
  },
  apiGet: vi.fn().mockResolvedValue(null),
  apiGetTyped: vi.fn().mockResolvedValue(null),

vi.mock("../mcp-state.js", () => ({
  updateConfiguredEntry: vi.fn(),
  removeConfiguredEntry: vi.fn(),
  insertConfiguredEntry: vi.fn(),

import { _resetForTest as resetDefine } from "./define.js";
import { _resetForTest as resetRegistry } from "./registry.js";
import { _resetForTest as resetCleanup } from "./cleanup.js";
import {
  updateConfiguredEntry,
  removeConfiguredEntry,
  insertConfiguredEntry,
} from "../mcp-state.js";
import { toggleServer, deleteServer } from "./mcp.js";
import type { Server } from "../mcp-state.js";

const mockFetch = vi.fn();
const mockUpdate = vi.mocked(updateConfiguredEntry);
const mockRemove = vi.mocked(removeConfiguredEntry);
const mockInsert = vi.mocked(insertConfiguredEntry);

function makeServer(id: string, enabled = true): Server {
  return { id, name: `srv-${id}`, transport: "stdio", enabled, created_at: 1000, updated_at: 1000 };
}

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
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
