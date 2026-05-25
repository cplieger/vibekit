// ---------------------------------------------------------------------------
// MCP state: wire types, secret sentinel, in-memory state, server fetch.
// ---------------------------------------------------------------------------

import { apiGet, apiGetTyped, CancellableSlot } from "./api-client.js";
import { registerCleanup } from "./actions/cleanup.js";
import {
  asObject, decodeArray, optStr, reqStr, type Decoder,
} from "./validators.js";

const decodeWireRuntimeStatus: Decoder<WireRuntimeStatus> = (v) => {
  const s = asObject(v, "$.mcp_status.server");
  const out: WireRuntimeStatus = {
    name: reqStr(s, "name", "$.mcp_status.server"),
    state: reqStr(s, "state", "$.mcp_status.server"),
  };
  const oauthUrl = optStr(s, "oauth_url", "$.mcp_status.server");
  if (oauthUrl !== undefined) out.oauth_url = oauthUrl;
  const err = optStr(s, "error", "$.mcp_status.server");
  if (err !== undefined) out.error = err;
  return out;
};

const decodeMCPStatusResponseLocal: Decoder<{ servers: WireRuntimeStatus[] }> = (v) => {
  const o = asObject(v, "$.mcp_status");
  return {
    servers: decodeArray(o["servers"], decodeWireRuntimeStatus, "$.mcp_status.servers"),
  };
};

// --- Wire types (match internal/mcp + internal/hub/mcp_registry) ---

export type Transport = "stdio" | "http" | "sse";

export interface KeyPair { name: string; value: string }

/** Persisted server record. `value` fields come back as "***" when a
 *  secret is stored; send "***" unchanged to preserve it, any other
 *  string to replace it. */
export interface Server {
  id: string;
  name: string;
  transport: Transport;
  enabled: boolean;
  prewarm?: boolean;
  command?: string;
  args?: string[];
  env?: KeyPair[];
  url?: string;
  headers?: KeyPair[];
  disabled_tools?: string[];
  known_tools?: string[];
  created_at: number;
  updated_at: number;
}

export type RuntimeState = "connected" | "needs_auth" | "idle" | "failed";

export type RuntimeStatus =
  | { name: string; state: "connected" }
  | { name: string; state: "needs_auth"; oauth_url: string }
  | { name: string; state: "idle" }
  | { name: string; state: "failed"; error: string };

// --- Secret sentinel ---
export const SECRET_MASK = "***";

// --- Wire status type ---

interface WireRuntimeStatus {
  name: string;
  state: RuntimeState | (string & {});
  oauth_url?: string;
  error?: string;
}

/** Exported for testing: adapt a wire status to the domain type. */
export function adaptStatus(w: WireRuntimeStatus): RuntimeStatus {
  switch (w.state) {
    case "needs_auth": return { name: w.name, state: "needs_auth", oauth_url: w.oauth_url ?? "" };
    case "failed":     return { name: w.name, state: "failed", error: w.error ?? "" };
    case "connected":  return { name: w.name, state: "connected" };
    default:           return { name: w.name, state: "idle" };
  }
}

// --- MCPStateController ---

class MCPStateController {
  private readonly _status = new Map<string, RuntimeStatus>();
  renderCb: (() => void) | null = null;
  private readonly serversSlot = new CancellableSlot();
  private readonly statusSlot = new CancellableSlot();
  private serversFetchPending = false;
  private statusFetchPending = false;

  get status(): ReadonlyMap<string, RuntimeStatus> { return this._status; }

  abort(): void {
    this.serversSlot.abort();
    this.statusSlot.abort();
    this.serversFetchPending = false;
    this.statusFetchPending = false;
  }

  setRenderCallback(cb: () => void): void { this.renderCb = cb; }

  setStatus(name: string, rs: RuntimeStatus): void { this._status.set(name, rs); }

  deleteStatus(name: string): void { this._status.delete(name); }

  refetchServers(): void {
    if (this.serversFetchPending) return;
    this.serversFetchPending = true;
    queueMicrotask(() => {
      this.serversFetchPending = false;
      void this.doRefetchServers();
    });
  }

  private async doRefetchServers(): Promise<void> {
    const signal = this.serversSlot.start();
    const d = await apiGet<{ servers: Server[] }>("/api/mcp", signal);
    if (signal.aborted) return;
    configured = d?.servers ?? [];
    this.renderCb?.();
  }

  refetchStatus(): void {
    if (this.statusFetchPending) return;
    this.statusFetchPending = true;
    queueMicrotask(() => {
      this.statusFetchPending = false;
      void this.doRefetchStatus();
    });
  }

  private async doRefetchStatus(): Promise<void> {
    const signal = this.statusSlot.start();
    const d = await apiGetTyped("/api/mcp/status", decodeMCPStatusResponseLocal, signal);
    if (signal.aborted) return;
    this._status.clear();
    for (const s of d?.servers ?? []) this._status.set(s.name, adaptStatus(s));
    this.renderCb?.();
  }
}

// --- Singleton + delegate exports (preserves public API) ---

const instance = new MCPStateController();
registerCleanup(() => { instance.abort(); });

// `configured` remains a module-level let (live binding for consumers).
// `status` is a readonly reference to the controller's internal Map.
export let configured: readonly Server[] = [];
export const status: ReadonlyMap<string, RuntimeStatus> = instance.status;

export function setRenderCallback(cb: () => void): void { instance.setRenderCallback(cb); }
export function setStatus(name: string, rs: RuntimeStatus): void { instance.setStatus(name, rs); }
export function deleteStatus(name: string): void { instance.deleteStatus(name); }
export function refetchServers(): void { instance.refetchServers(); }
export function refetchStatus(): void { instance.refetchStatus(); }

// --- Optimistic mutation helpers ---

/** Patch a configured entry in-place and re-render. Returns the previous entry for rollback. */
export function updateConfiguredEntry(id: string, patch: Partial<Server>): Server | undefined {
  const idx = configured.findIndex((s) => s.id === id);
  if (idx === -1) return undefined;
  const prev = { ...configured[idx] } as Server;
  const arr = [...configured];
  arr[idx] = { ...arr[idx], ...patch } as Server;
  configured = arr;
  instance.renderCb?.();
  return prev;
}

/** @internal Remove a configured entry by id. Returns [entry, index] for rollback. */
export function removeConfiguredEntry(id: string): [Server, number] | undefined {
  const arr = [...configured];
  const idx = arr.findIndex((s) => s.id === id);
  if (idx === -1) return undefined;
  const entry = arr[idx]!;
  arr.splice(idx, 1);
  configured = arr;
  instance.renderCb?.();
  return [entry, idx];
}

/** Re-insert a previously removed entry at its original position when available. */
export function insertConfiguredEntry(entry: Server, atIndex?: number): void {
  const arr = [...configured];
  let pos: number;
  if (atIndex !== undefined && atIndex >= 0 && atIndex <= arr.length) {
    pos = atIndex;
  } else {
    // Fall back to id ordering if no positional hint.
    pos = arr.findIndex((s) => s.id > entry.id);
    if (pos === -1) pos = arr.length;
  }
  arr.splice(pos, 0, entry);
  configured = arr;
  instance.renderCb?.();
}
