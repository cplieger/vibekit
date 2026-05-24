// MCP actions: user-initiated mutations for the MCP integrations UI.
// ---------------------------------------------------------------------------

import { apiAction } from "./api.js";
import { defineAction, ActionError } from "./index.js";
import { type Server } from "../mcp-state.js";

// --- mcp.toggle_server ---

export interface ToggleArgs {
  id: string;
  enabled: boolean;
  /** The checkbox input element — used for optimistic flip + rollback. */
  input: HTMLInputElement;
}

export const toggleServer = apiAction<ToggleArgs, void>({
  name: "mcp.toggle_server",
  request: ({ id, enabled }) => ({
    method: "PATCH",
    path: `/api/mcp/${encodeURIComponent(id)}`,
    body: { enabled },
  }),
  optimistic: ({ input, enabled }) => {
    const before = input.checked;
    input.checked = enabled;
    return { before };
  },
  rollback: ({ input }, op) => {
    if (op !== undefined && typeof op === "object" && op !== null && "before" in op) {
      input.checked = op.before as boolean;
    }
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
    return { row };
  },
  rollback: (_args, op) => {
    if (op !== undefined && typeof op === "object" && op !== null && "row" in op) {
      (op.row as HTMLDivElement).classList.remove("exiting");
    }
  },
  error: "Couldn't remove integration",
});

// --- mcp.open_edit ---

export const openEdit = defineAction<string, Server>({
  name: "mcp.open_edit",
  run: async (id, signal) => {
    const r = await fetch(`/api/mcp/${encodeURIComponent(id)}`, { signal });
    if (!r.ok) {
      let msg = "";
      try {
        const b = (await r.json()) as { error?: unknown };
        if (typeof b.error === "string") msg = b.error;
      } catch { /* ignore */ }
      throw new ActionError(msg || `HTTP ${String(r.status)}`, { status: r.status });
    }
    return (await r.json()) as Server;
  },
  error: "Couldn't load integration details",
});
