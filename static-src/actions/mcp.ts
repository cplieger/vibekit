// MCP actions: user-initiated mutations for the MCP integrations UI.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";

import {
  type Server,
  updateConfiguredEntry,
  removeConfiguredEntry,
  insertConfiguredEntry,
} from "../mcp-state.js";

/** Base path for MCP API endpoints — single source of truth. */
export const MCP_API = "/api/mcp";

/** Result shape from the registry search endpoint. */
export interface RegistrySearchResult {
  servers: {
    name: string;
    title?: string;
    description?: string;
    version?: string;
    repository?: string;
    packages?: {
      registry_type: string;
      identifier: string;
      version?: string;
      env_vars?: {
        name: string;
        description?: string;
        required?: boolean;
        secret?: boolean;
      }[];
    }[];
    remotes?: {
      type: string;
      url: string;
      headers?: {
        name: string;
        description?: string;
        value?: string;
        required?: boolean;
        secret?: boolean;
      }[];
    }[];
  }[];
}

// --- mcp.toggle_server ---

interface ToggleArgs {
  id: string;
  enabled: boolean;
}

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const toggleServer = apiAction<ToggleArgs, void, Server>({
  name: "mcp.toggle_server",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  scope: (args) => "mcp:" + args.id,
  request: ({ id, enabled }) => ({
    method: "PATCH",
    path: `${MCP_API}/${encodeURIComponent(id)}`,
    body: { enabled },
  }),
  optimistic: ({ id, enabled }) => {
    return updateConfiguredEntry(id, { enabled });
  },
  rollback: (_args, op) => {
    if (op !== undefined) {
      updateConfiguredEntry(op.id, { enabled: op.enabled });
    }
  },
  error: "Couldn't toggle integration",
});

// --- mcp.delete_server ---

interface DeleteArgs {
  id: string;
}

// No auto-retry and no manual retry: a timed-out DELETE may have
// succeeded server-side; retrying would hit 404 and trigger a
// misleading rollback (re-inserting an already-deleted entry).
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const deleteServer = apiAction<DeleteArgs, void, [Server, number]>({
  name: "mcp.delete_server",
  dedupe: (args) => `mcp.delete:${args.id}`,
  scope: (args) => "mcp:" + args.id,
  request: ({ id }) => ({
    method: "DELETE",
    path: `${MCP_API}/${encodeURIComponent(id)}`,
  }),
  optimistic: ({ id }) => {
    return removeConfiguredEntry(id);
  },
  rollback: (_args, op) => {
    if (op !== undefined) {
      const [entry, atIndex] = op;
      insertConfiguredEntry(entry, atIndex);
    }
  },
  error: "Couldn't remove integration",
});

// --- mcp.open_edit ---

export const openEdit = apiAction<string, Server>({
  name: "mcp.open_edit",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: (id) => id,
  request: (id) => ({
    method: "GET",
    path: `${MCP_API}/${encodeURIComponent(id)}`,
  }),
  error: "Couldn't load integration details",
});

// --- mcp.save_server ---

interface SaveArgs {
  /** Empty string for create, non-empty for update. */
  id: string;
  body: Partial<Server>;
}

export const saveServer = apiAction<SaveArgs, Server>({
  name: "mcp.save_server",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  scope: (args) => "mcp:" + args.id,
  request: ({ id, body }) => ({
    method: id === "" ? "POST" : "PUT",
    path: id === "" ? MCP_API : `${MCP_API}/${encodeURIComponent(id)}`,
    body,
  }),
  error: false,
});

// --- mcp.search_registry ---

interface SearchRegistryArgs {
  q: string;
}

export const searchRegistry = apiAction<SearchRegistryArgs, RegistrySearchResult>({
  name: "mcp.search_registry",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: (args) => args.q,
  request: ({ q }) => ({
    method: "GET",
    path: `${MCP_API}/registry/search?q=${encodeURIComponent(q)}&limit=20`,
  }),
  error: false,
});

// --- mcp.reconnect_server ---
//
// Reconnect a wedged / expired-OAuth server on every live chat bridge
// (server-side fan-out). The refreshed runtime status arrives via SSE +
// a /api/mcp/status refetch, so there's no optimistic state to flip.

/** Result of POST /api/mcp/reconnect: how many live bridges were targeted. */
export interface ReconnectResult {
  reconnected: number;
}

export const reconnectServer = apiAction<{ server: string }, ReconnectResult>({
  name: "mcp.reconnect_server",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  scope: (args) => "mcp-reconnect:" + args.server,
  request: ({ server }) => ({
    method: "POST",
    path: `${MCP_API}/reconnect`,
    body: { server },
  }),
  error: "Couldn't reconnect integration",
});

// --- mcp.get_prompt / mcp.get_resource ---
//
// Resolve an MCP prompt / read an MCP resource from a live bridge's pool.
// The response is the raw MCP result; the UI extracts its text and inserts
// it into the prompt bar.

/** One content block of an MCP message (text is the only kind we surface). */
export interface MCPContentBlock {
  type?: string;
  text?: string;
}

/** Raw MCP GetPromptResult: an ordered list of role-tagged messages. */
export interface MCPPromptResult {
  description?: string;
  messages?: { role?: string; content?: MCPContentBlock | MCPContentBlock[] }[];
}

/** Raw MCP ReadResourceResult: one or more resource contents. */
export interface MCPResourceResult {
  contents?: { uri?: string; mimeType?: string; text?: string; blob?: string }[];
}

interface GetPromptArgs {
  server: string;
  prompt: string;
  arguments?: Record<string, string>;
}

export const getPromptContent = apiAction<GetPromptArgs, MCPPromptResult>({
  name: "mcp.get_prompt",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ server, prompt, arguments: args }) => ({
    method: "POST",
    path: `${MCP_API}/prompt`,
    body: { server, prompt, arguments: args ?? {} },
  }),
  error: "Couldn't load prompt",
});

export const getResourceContent = apiAction<{ server: string; uri: string }, MCPResourceResult>({
  name: "mcp.get_resource",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ server, uri }) => ({
    method: "POST",
    path: `${MCP_API}/resource`,
    body: { server, uri },
  }),
  error: "Couldn't load resource",
});
