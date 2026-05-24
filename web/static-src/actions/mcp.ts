// MCP actions: user-initiated mutations for the MCP integrations UI.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";
import { type Server, updateConfiguredEntry, removeConfiguredEntry, insertConfiguredEntry } from "../mcp-state.js";

// --- mcp.toggle_server ---

export interface ToggleArgs {
  id: string;
  enabled: boolean;
}

export const toggleServer = apiAction<ToggleArgs, void>({
  name: "mcp.toggle_server",
  retryable: "network",
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
      const prev = op as Server;
      updateConfiguredEntry(prev.id, { enabled: prev.enabled });
    }
  },
  error: "Couldn't toggle integration",
});

// --- mcp.delete_server ---

export interface DeleteArgs {
  id: string;
}

export const deleteServer = apiAction<DeleteArgs, void>({
  name: "mcp.delete_server",
  retryable: "network",
  request: ({ id }) => ({
    method: "DELETE",
    path: `/api/mcp/${encodeURIComponent(id)}`,
  }),
  optimistic: ({ id }) => {
    return removeConfiguredEntry(id);
  },
  rollback: (_args, op) => {
    if (op !== undefined) {
      const [entry] = op as [Server, number];
      insertConfiguredEntry(entry);
    }
  },
  error: "Couldn't remove integration",
});

// --- mcp.open_edit ---

export const openEdit = apiAction<string, Server>({
  name: "mcp.open_edit",
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
  request: ({ id, body }) => ({
    method: id === "" ? "POST" : "PUT",
    path: id === "" ? "/api/mcp" : `/api/mcp/${encodeURIComponent(id)}`,
    body,
  }),
  error: false,
});
