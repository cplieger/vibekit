// ---------------------------------------------------------------------------
// MCP state: wire types, secret sentinel, in-memory state, server fetch.
// ---------------------------------------------------------------------------

import { apiGetTyped, CancellableSlot } from "./api-client.js";
import { registerCleanup } from "./actions/index.js";
import {
  SignalMap,
  createCollection,
  type ReadonlySignal,
  type Collection,
} from "@cplieger/reactive";
import {
  asObject,
  decodeArray,
  optBool,
  optStr,
  reqBool,
  reqNum,
  reqStr,
  type Decoder,
} from "./validators.js";

const decodeWireRuntimeStatus: Decoder<WireRuntimeStatus> = (v) => {
  const s = asObject(v, "$.mcp_status.server");
  const out: WireRuntimeStatus = {
    name: reqStr(s, "name", "$.mcp_status.server"),
    state: reqStr(s, "state", "$.mcp_status.server"),
  };
  const oauthUrl = optStr(s, "oauth_url", "$.mcp_status.server");
  if (oauthUrl !== undefined) {
    out.oauth_url = oauthUrl;
  }
  const err = optStr(s, "error", "$.mcp_status.server");
  if (err !== undefined) {
    out.error = err;
  }
  return out;
};

const decodeMCPStatusResponseLocal: Decoder<{ servers: WireRuntimeStatus[] }> = (v) => {
  const o = asObject(v, "$.mcp_status");
  return {
    servers: decodeArray(o["servers"], decodeWireRuntimeStatus, "$.mcp_status.servers"),
  };
};

const decodeKeyPair: Decoder<KeyPair> = (v) => {
  const o = asObject(v, "$.kp");
  return { name: reqStr(o, "name", "$.kp"), value: reqStr(o, "value", "$.kp") };
};

const decodeServer: Decoder<Server> = (v) => {
  const o = asObject(v, "$.server");
  const p = "$.server";
  const out: Server = {
    id: reqStr(o, "id", p),
    name: reqStr(o, "name", p),
    transport: reqStr(o, "transport", p) as Transport,
    enabled: reqBool(o, "enabled", p),
    created_at: reqNum(o, "created_at", p),
    updated_at: reqNum(o, "updated_at", p),
  };
  const command = optStr(o, "command", p);
  if (command !== undefined) {
    out.command = command;
  }
  const url = optStr(o, "url", p);
  if (url !== undefined) {
    out.url = url;
  }
  const oauthClientId = optStr(o, "oauth_client_id", p);
  if (oauthClientId !== undefined) {
    out.oauth_client_id = oauthClientId;
  }
  const prewarm = optBool(o, "prewarm", p);
  if (prewarm !== undefined) {
    out.prewarm = prewarm;
  }
  if (Array.isArray(o["args"])) {
    out.args = (o["args"] as unknown[]).map((x) => String(x));
  }
  if (Array.isArray(o["env"])) {
    out.env = decodeArray(o["env"], decodeKeyPair, `${p}.env`);
  }
  if (Array.isArray(o["headers"])) {
    out.headers = decodeArray(o["headers"], decodeKeyPair, `${p}.headers`);
  }
  if (Array.isArray(o["disabled_tools"])) {
    out.disabled_tools = (o["disabled_tools"] as unknown[]).map((x) => String(x));
  }
  if (Array.isArray(o["known_tools"])) {
    out.known_tools = (o["known_tools"] as unknown[]).map((x) => String(x));
  }
  return out;
};

const decodeMCPServersResponseLocal: Decoder<{ servers: Server[] }> = (v) => {
  const o = asObject(v, "$.mcp_servers");
  return {
    servers: decodeArray(o["servers"], decodeServer, "$.mcp_servers.servers"),
  };
};

// --- Wire types (match internal/mcp + internal/hub/mcp_registry) ---

export type Transport = "stdio" | "http" | "sse";

export interface KeyPair {
  name: string;
  value: string;
}

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
  /** Pre-registered OAuth 2.0 client ID for HTTP servers without
   *  Dynamic Client Registration support (Slack, GitHub, Figma).
   *  Forwarded to kiro-cli as `oauth.clientId` on session/new
   *  (kiro-cli 2.3+). Empty falls back to DCR. */
  oauth_client_id?: string;
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
  state: string;
  oauth_url?: string;
  error?: string;
}

/** Exported for testing: adapt a wire status to the domain type. */
export function adaptStatus(w: WireRuntimeStatus): RuntimeStatus {
  switch (w.state) {
    case "needs_auth":
      return { name: w.name, state: "needs_auth", oauth_url: w.oauth_url ?? "" };
    case "failed":
      return { name: w.name, state: "failed", error: w.error ?? "" };
    case "connected":
      return { name: w.name, state: "connected" };
    default:
      return { name: w.name, state: "idle" };
  }
}

// --- Runtime status registry (per-server signal, keyed by server name) ---

const statusMap = new SignalMap<RuntimeStatus>();

function idleStatus(name: string): RuntimeStatus {
  return { name, state: "idle" };
}

/** Reactive per-server runtime-status signal (created lazily as "idle"). Read
 *  `.value` inside an effect to re-render only when THIS server's status
 *  changes. */
export function statusSignalFor(name: string): ReadonlySignal<RuntimeStatus> {
  return statusMap.ensure(name, idleStatus(name));
}

// --- Prewarm registry (per-server npx-install state, keyed by server id) ---

export type PrewarmState = "none" | "installing" | "failed";

const prewarmMap = new SignalMap<PrewarmState>();

/** Reactive per-server prewarm-state signal (created lazily as "none"). */
export function prewarmSignalFor(id: string): ReadonlySignal<PrewarmState> {
  return prewarmMap.ensure(id, "none");
}

/** Set a server's prewarm state ("done" clears the badge -> "none"). */
export function setPrewarm(id: string, state: "installing" | "done" | "failed"): void {
  prewarmMap.ensure(id, "none").value = state === "done" ? "none" : state;
}

// --- Server collection (ordered, keyed by id) ---

/** The configured MCP servers. Rendered by bindList in mcp-ui; per-server
 *  field changes fire `signalFor(id)`, add/remove/reorder fire `ids`. */
export const servers: Collection<Server> = createCollection<Server>((s) => s.id);

/** Snapshot of the configured servers (non-reactive). */
export function configuredServers(): Server[] {
  return servers.items();
}

// --- MCP fetch controller (servers + status; no manual render callback) ---

class MCPStateController {
  private readonly serversSlot = new CancellableSlot();
  private readonly statusSlot = new CancellableSlot();
  private serversFetchPending = false;
  private statusFetchPending = false;

  abort(): void {
    this.serversSlot.abort();
    this.statusSlot.abort();
    this.serversFetchPending = false;
    this.statusFetchPending = false;
  }

  setStatus(name: string, rs: RuntimeStatus): void {
    statusMap.ensure(name, rs).value = rs;
  }

  deleteStatus(name: string): void {
    // Reset to idle (fires the signal so subscribed rows re-render) rather
    // than clear (which would orphan the row's subscription).
    statusMap.ensure(name, idleStatus(name)).value = idleStatus(name);
  }

  refetchServers(): void {
    if (this.serversFetchPending) {
      return;
    }
    this.serversFetchPending = true;
    queueMicrotask(() => {
      this.serversFetchPending = false;
      void this.doRefetchServers();
    });
  }

  private async doRefetchServers(): Promise<void> {
    const signal = this.serversSlot.start();
    const d = await apiGetTyped("/api/mcp", decodeMCPServersResponseLocal, signal);
    if (signal.aborted) {
      return;
    }
    servers.setAll(d?.servers ?? []);
  }

  refetchStatus(): void {
    if (this.statusFetchPending) {
      return;
    }
    this.statusFetchPending = true;
    queueMicrotask(() => {
      this.statusFetchPending = false;
      void this.doRefetchStatus();
    });
  }

  private async doRefetchStatus(): Promise<void> {
    const signal = this.statusSlot.start();
    const d = await apiGetTyped("/api/mcp/status", decodeMCPStatusResponseLocal, signal);
    if (signal.aborted) {
      return;
    }
    const seen = new Set<string>();
    for (const s of d?.servers ?? []) {
      seen.add(s.name);
      this.setStatus(s.name, adaptStatus(s));
    }
    // Servers with no reported status revert to idle (fires their signal).
    for (const s of servers.items()) {
      if (!seen.has(s.name)) {
        this.deleteStatus(s.name);
      }
    }
  }
}

// --- Singleton export ---

const instance = new MCPStateController();
export const mcpState = instance;
registerCleanup(() => {
  instance.abort();
});

// --- Optimistic mutation helpers (over the collection) ---

/** Patch a configured entry in-place. Returns the previous entry for rollback. */
export function updateConfiguredEntry(id: string, patch: Partial<Server>): Server | undefined {
  const prev = servers.get(id);
  if (prev === undefined) {
    return undefined;
  }
  const snapshot = { ...prev };
  servers.update(id, (cur) => ({ ...cur, ...patch }));
  return snapshot;
}

/** @internal Remove a configured entry by id. Returns [entry, index] for rollback. */
export function removeConfiguredEntry(id: string): [Server, number] | undefined {
  const idx = servers.ids.peek().indexOf(id);
  if (idx === -1) {
    return undefined;
  }
  const entry = servers.get(id);
  if (entry === undefined) {
    return undefined;
  }
  servers.remove(id);
  return [entry, idx];
}

/** Re-insert a previously removed entry at its original position when available. */
export function insertConfiguredEntry(entry: Server, atIndex?: number): void {
  if (servers.has(entry.id)) {
    return;
  }
  const arr = servers.items();
  let pos: number;
  if (atIndex !== undefined && atIndex >= 0 && atIndex <= arr.length) {
    pos = atIndex;
  } else {
    // Fall back to id ordering if no positional hint.
    pos = arr.findIndex((s) => s.id > entry.id);
    if (pos === -1) {
      pos = arr.length;
    }
  }
  arr.splice(pos, 0, entry);
  servers.setAll(arr);
}
