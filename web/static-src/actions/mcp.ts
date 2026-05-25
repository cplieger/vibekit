// MCP actions: user-initiated mutations for the MCP integrations UI.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";
import { type Server, updateConfiguredEntry, removeConfiguredEntry, insertConfiguredEntry } from "../mcp-state.js";

/** Result shape from the registry search endpoint. */
export interface RegistrySearchResult {
  servers: Array<{
    name: string;
    title?: string;
    description?: string;
    version?: string;
    repository?: string;
    packages?: Array<{
      registry_type: string;
      identifier: string;
      version?: string;
      env_vars?: Array<{ name: string; description?: string; required?: boolean; secret?: boolean }>;
    }>;
    remotes?: Array<{
      type: string;
      url: string;
      headers?: Array<{ name: string; description?: string; value?: string; required?: boolean; secret?: boolean }>;
    }>;
  }>;
}

// --- mcp.toggle_server ---

export interface ToggleArgs {
  id: string;
  enabled: boolean;
}

export const toggleServer = apiAction<ToggleArgs, void, Server>({
  name: "mcp.toggle_server",
  retryable: "network",
  retry: { count: 2, delay: 300 },
  scope: (args) => "mcp:" + args.id,
  request: ({ id, enabled }) => ({
    method: "PATCH",
    path: `/api/mcp/${encodeURIComponent(id)}`,
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

export interface DeleteArgs {
  id: string;
}

// No auto-retry: a timed-out DELETE may have succeeded server-side;
// retrying would hit 404 and surface a misleading error toast.
// Manual Retry button is still available via retryable: "network".
export const deleteServer = apiAction<DeleteArgs, void, [Server, number]>({
  name: "mcp.delete_server",
  retryable: "network",
  scope: (args) => "mcp:" + args.id,
  request: ({ id }) => ({
    method: "DELETE",
    path: `/api/mcp/${encodeURIComponent(id)}`,
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
  retryable: "network",
  retry: { count: 2, delay: 300 },
  dedupe: (id) => id,
  request: (id) => ({
    method: "GET",
    path: `/api/mcp/${encodeURIComponent(id)}`,
  }),
  error: "Couldn't load integration details",
});

// --- mcp.save_server ---

export interface SaveArgs {
  /** Empty string for create, non-empty for update. */
  id: string;
  body: Partial<Server>;
}

export const saveServer = apiAction<SaveArgs, Server>({
  name: "mcp.save_server",
  idempotencyKey: true,
  retryable: "network",
  retry: { count: 2, delay: 300 },
  scope: (args) => "mcp:" + args.id,
  request: ({ id, body }) => ({
    method: id === "" ? "POST" : "PUT",
    path: id === "" ? "/api/mcp" : `/api/mcp/${encodeURIComponent(id)}`,
    body,
  }),
  error: false,
});

// --- mcp.search_registry ---

export interface SearchRegistryArgs {
  q: string;
}

export const searchRegistry = apiAction<SearchRegistryArgs, RegistrySearchResult>({
  name: "mcp.search_registry",
  retryable: "network",
  retry: { count: 2, delay: 300 },
  dedupe: (args) => args.q,
  request: ({ q }) => ({
    method: "GET",
    path: `/api/mcp/registry/search?q=${encodeURIComponent(q)}&limit=20`,
  }),
  error: false,
});
