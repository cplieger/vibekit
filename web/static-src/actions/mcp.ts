// MCP actions: user-initiated mutations for the MCP integrations UI.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork } from "./index.js";
import { RETRY_STANDARD } from "./types.js";
import {
  type Server,
  updateConfiguredEntry,
  removeConfiguredEntry,
  insertConfiguredEntry,
} from "../mcp-state.js";

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

export const toggleServer = apiAction<ToggleArgs, void, Server>({
  name: "mcp.toggle_server",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
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

interface DeleteArgs {
  id: string;
}

// No auto-retry and no manual retry: a timed-out DELETE may have
// succeeded server-side; retrying would hit 404 and trigger a
// misleading rollback (re-inserting an already-deleted entry).
export const deleteServer = apiAction<DeleteArgs, void, [Server, number]>({
  name: "mcp.delete_server",
  dedupe: (args) => `mcp.delete:${args.id}`,
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
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: (id) => id,
  request: (id) => ({
    method: "GET",
    path: `/api/mcp/${encodeURIComponent(id)}`,
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
    path: id === "" ? "/api/mcp" : `/api/mcp/${encodeURIComponent(id)}`,
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
    path: `/api/mcp/registry/search?q=${encodeURIComponent(q)}&limit=20`,
  }),
  error: false,
});
