// MCP actions: user-initiated mutations for the MCP integrations UI.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";
import { type Server } from "../mcp-state.js";

// --- mcp.toggle_server ---

export interface ToggleArgs {
  id: string;
  enabled: boolean;
  /** The checkbox input element — used for optimistic flip + rollback. */
  input: HTMLInputElement;
  /** Previous checked state, captured BEFORE the change event fires. */
  previousEnabled: boolean;
}

export const toggleServer = apiAction<ToggleArgs, void>({
  name: "mcp.toggle_server",
  request: ({ id, enabled }) => ({
    method: "PATCH",
    path: `/api/mcp/${encodeURIComponent(id)}`,
    body: { enabled },
  }),
  // No optimistic needed — the browser already flipped the checkbox
  // before the change event fires. Rollback restores on failure.
  rollback: ({ input, previousEnabled }) => {
    input.checked = previousEnabled;
  },
  error: "Couldn't toggle integration",
});

// --- mcp.delete_server ---

export interface DeleteArgs {
  id: string;
  row: HTMLDivElement;
}

export const deleteServer = apiAction<DeleteArgs, void>({
  name: "mcp.delete_server",
  request: ({ id }) => ({
    method: "DELETE",
    path: `/api/mcp/${encodeURIComponent(id)}`,
  }),
  optimistic: ({ row }) => {
    row.classList.add("exiting");
    return undefined;
  },
  rollback: (args) => {
    args.row.classList.remove("exiting");
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
