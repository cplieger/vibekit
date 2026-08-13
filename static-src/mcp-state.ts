// ---------------------------------------------------------------------------
// MCP state: wire types, secret sentinel, in-memory state, server fetch.
// ---------------------------------------------------------------------------

import { apiGetTyped, CancellableSlot } from "./api-client.js";
import { registerCleanup } from "./actions/index.js";
import {
  SignalMap,
  createCollection,
  signal,
  type ReadonlySignal,
  type Signal,
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

const decodePromptArg: Decoder<MCPPromptArg> = (v) => {
  const o = asObject(v, "$.mcp.prompt.arg");
  const out: MCPPromptArg = { name: reqStr(o, "name", "$.mcp.prompt.arg") };
  const d = optStr(o, "description", "$.mcp.prompt.arg");
  if (d !== undefined) {
    out.description = d;
  }
  const r = optBool(o, "required", "$.mcp.prompt.arg");
  if (r !== undefined) {
    out.required = r;
  }
  return out;
};

const decodePromptInfo: Decoder<MCPPromptInfo> = (v) => {
  const o = asObject(v, "$.mcp.prompt");
  const out: MCPPromptInfo = {
    name: reqStr(o, "name", "$.mcp.prompt"),
    prompt_name: reqStr(o, "prompt_name", "$.mcp.prompt"),
  };
  const d = optStr(o, "description", "$.mcp.prompt");
  if (d !== undefined) {
    out.description = d;
  }
  if (Array.isArray(o["arguments"])) {
    out.arguments = decodeArray(o["arguments"], decodePromptArg, "$.mcp.prompt.arguments");
  }
  return out;
};

const decodeResourceInfo: Decoder<MCPResourceInfo> = (v) => {
  const o = asObject(v, "$.mcp.resource");
  const out: MCPResourceInfo = {
    name: reqStr(o, "name", "$.mcp.resource"),
    uri: reqStr(o, "uri", "$.mcp.resource"),
  };
  const d = optStr(o, "description", "$.mcp.resource");
  if (d !== undefined) {
    out.description = d;
  }
  const m = optStr(o, "mime_type", "$.mcp.resource");
  if (m !== undefined) {
    out.mime_type = m;
  }
  return out;
};

const decodeWireRuntimeStatus: Decoder<WireRuntimeStatus> = (v) => {
  const s = asObject(v, "$.mcp_status.server");
  const out: WireRuntimeStatus = {
    name: reqStr(s, "name", "$.mcp_status.server"),
    state: reqStr(s, "state", "$.mcp_status.server"),
  };
  // Optional on the decode side even though the server always sends it: a
  // decoder that REQUIRED it would reject the whole status response, blanking
  // every row, if a server ever omitted it. adaptOrigin's fallback is the
  // conservative answer for a missing value.
  const origin = optStr(s, "origin", "$.mcp_status.server");
  if (origin !== undefined) {
    out.origin = origin;
  }
  const oauthUrl = optStr(s, "oauth_url", "$.mcp_status.server");
  if (oauthUrl !== undefined) {
    out.oauth_url = oauthUrl;
  }
  const err = optStr(s, "error", "$.mcp_status.server");
  if (err !== undefined) {
    out.error = err;
  }
  if (Array.isArray(s["tools"])) {
    out.tools = (s["tools"] as unknown[]).map((x) => String(x));
  }
  if (Array.isArray(s["prompts"])) {
    out.prompts = decodeArray(s["prompts"], decodePromptInfo, "$.mcp_status.server.prompts");
  }
  if (Array.isArray(s["resources"])) {
    out.resources = decodeArray(
      s["resources"],
      decodeResourceInfo,
      "$.mcp_status.server.resources",
    );
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
  const oauthClientSecret = optStr(o, "oauth_client_secret", p);
  if (oauthClientSecret !== undefined) {
    out.oauth_client_secret = oauthClientSecret;
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
  return out;
};

const decodeMCPServersResponseLocal: Decoder<{ servers: Server[] }> = (v) => {
  const o = asObject(v, "$.mcp_servers");
  return {
    servers: decodeArray(o["servers"], decodeServer, "$.mcp_servers.servers"),
  };
};

// --- Wire types (match internal/mcp + internal/hub/mcp_registry) ---

// Transport mirrors the wiregen-emitted shape (stdio | http | sse). SSE is
// the legacy HTTP+SSE remote transport, re-adopted for v3 (KAS advertises
// mcpCapabilities.sse:true and accepts a distinct {type:"sse"} entry on
// session/new). It is a first-class stored value that shares the url/headers
// form with http, differing only in the ACP `type` discriminator.
export type { Transport } from "./wire/types.gen.js";
import type { Transport } from "./wire/types.gen.js";

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
  /** Pre-registered OAuth 2.0 client ID for HTTP servers without
   *  Dynamic Client Registration support (Slack, GitHub, Figma).
   *  Forwarded to kiro-cli as `oauth.clientId` on session/new
   *  (kiro-cli 2.3+). Empty falls back to DCR. */
  oauth_client_id?: string;
  /** Pre-registered OAuth 2.0 client secret for confidential HTTP MCP
   *  servers that authenticate at the token endpoint (kiro-cli 2.12+).
   *  Forwarded as `oauth.clientSecret`. Secret: returned as "***" when
   *  set; send "***" unchanged to preserve, any other value to replace. */
  oauth_client_secret?: string;
  created_at: number;
  updated_at: number;
}

/** One argument of an MCP prompt (from _kiro/mcp/status discovery). */
export interface MCPPromptArg {
  name: string;
  description?: string;
  required?: boolean;
}

/** A prompt a connected MCP server advertises. `prompt_name` is the machine
 *  id passed to the fetch endpoint; `name` is the display title. */
export interface MCPPromptInfo {
  name: string;
  prompt_name: string;
  description?: string;
  arguments?: MCPPromptArg[];
}

/** A resource a connected MCP server advertises. `uri` is the fetch key. */
export interface MCPResourceInfo {
  name: string;
  uri: string;
  description?: string;
  mime_type?: string;
}

/** What a connected server exposes: its tool names, prompts and resources
 *  (empty when none / not connected). Keyed by server name in the discovery
 *  signal map. All three arrive together on /api/mcp/status.
 *
 *  `tools` used to be a PERSISTED config field (`known_tools` on the server
 *  record). It moved here when KAS's config file became the source of truth:
 *  the tool names are what the agent found at connect time, not something the
 *  user configured, and writing them back into the config file made every
 *  status notification a disk write. */
export interface ServerDiscovery {
  tools: string[];
  prompts: MCPPromptInfo[];
  resources: MCPResourceInfo[];
}

export type RuntimeState = "connected" | "needs_auth" | "idle" | "failed" | "disabled";

/** Where a server came from (`internal/api.Origin`).
 *
 *  `user` is a server this page's config list owns — its row carries every edit
 *  affordance. `power` came from an installed Power, `unknown` from a config
 *  vibekit cannot read; both are read-only, and the difference is only what the
 *  row TELLS the reader. */
export type Origin = "user" | "power" | "unknown";

export type RuntimeStatus =
  | { name: string; origin: Origin; state: "connected" }
  | { name: string; origin: Origin; state: "needs_auth"; oauth_url: string }
  | { name: string; origin: Origin; state: "idle" }
  | { name: string; origin: Origin; state: "failed"; error: string }
  | { name: string; origin: Origin; state: "disabled" };

/** A RuntimeStatus variant with the origin stripped — what an SSE frame can
 *  express. Distributive on purpose: a plain `Omit<RuntimeStatus, "origin">`
 *  collapses the union to its common members and loses `oauth_url` / `error`. */
export type WithoutOrigin<T> = T extends unknown ? Omit<T, "origin"> : never;

// --- Secret sentinel ---
export const SECRET_MASK = "***";

// --- Wire status type ---

interface WireRuntimeStatus {
  name: string;
  state: string;
  origin?: string;
  oauth_url?: string;
  error?: string;
  tools?: string[];
  prompts?: MCPPromptInfo[];
  resources?: MCPResourceInfo[];
}

/** Narrow a wire origin string to the enum, defaulting to `user`.
 *
 *  The default is deliberate and it is the SAFE direction: `user` is the origin
 *  of every row this page's config list already renders, so an unrecognised or
 *  absent value cannot conjure a read-only row for a server the user owns. A
 *  read-only row is only ever produced by the server naming `power` or
 *  `unknown` explicitly. */
function adaptOrigin(raw: string | undefined): Origin {
  return raw === "power" || raw === "unknown" ? raw : "user";
}

/** Exported for testing: adapt a wire status to the domain type. */
export function adaptStatus(w: WireRuntimeStatus): RuntimeStatus {
  const origin = adaptOrigin(w.origin);
  switch (w.state) {
    case "needs_auth":
      return { name: w.name, origin, state: "needs_auth", oauth_url: w.oauth_url ?? "" };
    case "failed":
      return { name: w.name, origin, state: "failed", error: w.error ?? "" };
    case "connected":
      return { name: w.name, origin, state: "connected" };
    case "disabled":
      return { name: w.name, origin, state: "disabled" };
    default:
      return { name: w.name, origin, state: "idle" };
  }
}

// --- Runtime status registry (per-server signal, keyed by server name) ---

const statusMap = new SignalMap<RuntimeStatus>();

function idleStatus(name: string): RuntimeStatus {
  return { name, origin: "user", state: "idle" };
}

/** Reactive per-server runtime-status signal (created lazily as "idle"). Read
 *  `.value` inside an effect to re-render only when THIS server's status
 *  changes. */
export function statusSignalFor(name: string): ReadonlySignal<RuntimeStatus> {
  return statusMap.ensure(name, idleStatus(name));
}

// --- Discovery registry (per-server prompts/resources, keyed by name) ---

const EMPTY_DISCOVERY: ServerDiscovery = { tools: [], prompts: [], resources: [] };
const discoveryMap = new SignalMap<ServerDiscovery>();

/** Reactive per-server discovery signal (tools/prompts/resources; empty
 *  default). Populated from /api/mcp/status, which carries what a connected
 *  server advertises. */
export function discoverySignalFor(name: string): ReadonlySignal<ServerDiscovery> {
  return discoveryMap.ensure(name, EMPTY_DISCOVERY);
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

/** Names /api/mcp/status reported that this page's config list does not hold —
 *  a Power's server, or one from a config vibekit cannot read. Sorted.
 *
 *  It exists because the configured list is keyed by a persisted server id and
 *  such a server has none: it is not in `mcp.json` at all. Without a second list
 *  the page shows nothing for it while its tools sit in the agent's tool list,
 *  which is the whole defect. Rows built from it are read-only — there is no
 *  record to edit, enable or delete. */
export const unconfiguredNames: Signal<string[]> = signal<string[]>([]);

/** Snapshot of the configured servers (non-reactive). */
export function configuredServers(): Server[] {
  return servers.items();
}

/** Element-wise equality for two sorted name lists. */
function sameNames(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((n, i) => n === b[i]);
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

  /** Set a server's runtime state from an SSE frame, keeping the origin already
   *  known for it.
   *
   *  The SSE payloads carry no origin — only `/api/mcp/status` does — and a
   *  server's provenance does not change while the process runs. Defaulting to
   *  `user` here instead would flip a read-only row's chip on its first state
   *  change and start offering edit affordances for a record that does not
   *  exist, until the next status refetch put it back. */
  setStatusFromEvent(name: string, next: WithoutOrigin<RuntimeStatus>): void {
    const origin = statusMap.ensure(name, idleStatus(name)).peek().origin;
    this.setStatus(name, { ...next, origin });
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
    // Named `abort`, not `signal`: this module now imports reactive's `signal`
    // factory, and shadowing it with an AbortSignal reads as the wrong thing.
    const abort = this.serversSlot.start();
    const d = await apiGetTyped("/api/mcp", decodeMCPServersResponseLocal, abort);
    if (abort.aborted) {
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
    const abort = this.statusSlot.start();
    const d = await apiGetTyped("/api/mcp/status", decodeMCPStatusResponseLocal, abort);
    if (abort.aborted) {
      return;
    }
    const configured = new Set(servers.items().map((s) => s.name));
    const seen = new Set<string>();
    const foreign: string[] = [];
    for (const s of d?.servers ?? []) {
      seen.add(s.name);
      const st = adaptStatus(s);
      this.setStatus(s.name, st);
      this.setDiscovery(s.name, s.tools ?? [], s.prompts ?? [], s.resources ?? []);
      // Both conditions, not either: the origin is the server's own judgment and
      // the authority, but the two fetches are independent, so a name already on
      // a configured row must never also get a read-only one (two rows for one
      // server) while /api/mcp is mid-flight.
      if (st.origin !== "user" && !configured.has(s.name)) {
        foreign.push(s.name);
      }
    }
    foreign.sort();
    // Replace only on a real change: this fires on every status refetch, and an
    // equal-but-new array would rebuild every read-only row for nothing.
    if (!sameNames(unconfiguredNames.peek(), foreign)) {
      unconfiguredNames.value = foreign;
    }
    // Servers with no reported status revert to idle (fires their signal).
    for (const s of servers.items()) {
      if (!seen.has(s.name)) {
        this.deleteStatus(s.name);
        this.setDiscovery(s.name, [], [], []);
      }
    }
  }

  setDiscovery(
    name: string,
    tools: string[],
    prompts: MCPPromptInfo[],
    resources: MCPResourceInfo[],
  ): void {
    if (tools.length === 0 && prompts.length === 0 && resources.length === 0) {
      // Share the frozen empty value so idle servers don't churn the signal.
      discoveryMap.ensure(name, EMPTY_DISCOVERY).value = EMPTY_DISCOVERY;
      return;
    }
    discoveryMap.ensure(name, EMPTY_DISCOVERY).value = { tools, prompts, resources };
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
