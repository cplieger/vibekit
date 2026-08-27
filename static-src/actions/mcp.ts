// MCP actions: user-initiated mutations for the MCP integrations UI.
// ---------------------------------------------------------------------------

import { ActionError, apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { ApiErrorInfo, ApiErrorDecision } from "./index.js";

import {
  type Server,
  updateConfiguredEntry,
  removeConfiguredEntry,
  insertConfiguredEntry,
} from "../mcp-state.js";

/** Base path for MCP API endpoints — single source of truth. */
export const MCP_API = "/api/mcp";

/** One validation failure attributed to the wire field it came from
 *  (`internal/mcp.FieldError`). The server accumulates across independent
 *  checks, so a pasted block with three bad fields answers with three of these
 *  in one response instead of over three submit-fix-submit round trips. */
export interface ValidationField {
  field: string;
  message: string;
}

/** Narrow a `fields` array off a 400 body. Server-controlled input, so every
 *  entry is shape-checked rather than cast. */
function readValidationFields(body: unknown): ValidationField[] {
  if (typeof body !== "object" || body === null || !("fields" in body)) {
    return [];
  }
  // `"fields" in body` already narrows, so no assertion is needed here.
  const raw: unknown = body.fields;
  if (!Array.isArray(raw)) {
    return [];
  }
  const out: ValidationField[] = [];
  for (const entry of raw) {
    if (typeof entry !== "object" || entry === null) {
      continue;
    }
    const e = entry as { field?: unknown; message?: unknown };
    if (typeof e.field === "string" && typeof e.message === "string") {
      out.push({ field: e.field, message: e.message });
    }
  }
  return out;
}

/** Recover the per-field breakdown a 400 carries, onto the error's `cause`.
 *
 *  The dispatch still FAILS — a rejected record is not a success, so the modal
 *  must stay open with the form as the user left it — and `message` keeps the
 *  server's joined text so a caller that only reads that renders exactly what it
 *  did before. `cause` is the addition, and it is what lets the form mark three
 *  inputs instead of printing three sentences above one box. */
function decodeValidationError<T>(info: ApiErrorInfo): ApiErrorDecision<T> | undefined {
  if (info.status !== 400) {
    return undefined;
  }
  const fields = readValidationFields(info.body);
  if (fields.length === 0) {
    return undefined;
  }
  return {
    kind: "error",
    error: new ActionError(info.message, { status: info.status, cause: fields }),
  };
}

/** Read the field breakdown back off a failed dispatch's error. Returns an empty
 *  array for every other failure, which is what keeps a non-validation 400 (and
 *  a network death) on the single-message path. */
export function validationFieldsOf(err: { cause?: unknown } | undefined): ValidationField[] {
  const raw = err?.cause;
  if (!Array.isArray(raw)) {
    return [];
  }
  return raw.filter((f): f is ValidationField => {
    if (typeof f !== "object" || f === null) {
      return false;
    }
    const c = f as { field?: unknown; message?: unknown };
    return typeof c.field === "string" && typeof c.message === "string";
  });
}

/** Result shape from the registry search endpoint. */
export interface RegistrySearchResult {
  servers: {
    name: string;
    title?: string;
    description?: string;
    version?: string;
    repository?: string;
    /** Upstream lifecycle status, present only when it is NOT active
     *  (`deprecated` / `deleted`). The server omits the common case. */
    status?: string;
    /** The publisher's reason for a non-active status, when they gave one. */
    status_message?: string;
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
  decodeError: decodeValidationError,
  error: false,
});

// --- mcp.import_servers ---
//
// Connect every server of a pasted README block. The server owns the
// translation from the publisher's shape (see internal/mcp/paste.go), so this
// posts the parsed JSON unchanged: a second translator here would be a second
// copy of the same rules, and the one that names an unknown key has to be the
// one at the decode boundary.

/** What one entry of a pasted block did. There is no "updated": an entry naming
 *  a configured server either matches its spec or fails the paste. */
interface ImportResult {
  name: string;
  outcome: "created" | "unchanged";
}

/** Per-entry outcomes plus what the translation had to say about keys vibekit
 *  recognises and cannot store, so an accepted `timeout` does not read as a
 *  silently-dropped field. */
export interface ImportServersResult {
  results: ImportResult[];
  notes?: string[];
}

export const importServers = apiAction<Record<string, unknown>, ImportServersResult>({
  name: "mcp.import_servers",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: (block) => ({
    method: "POST",
    path: `${MCP_API}/import`,
    body: block,
  }),
  decodeError: decodeValidationError,
  success: (_args, res) => summariseImport(res),
  // The panel renders the failure inline beside the textarea the user is fixing,
  // which is where they are looking; a toast would put it somewhere else.
  error: false,
});

/** One sentence naming what landed. Exported for its test: the wording is the
 *  only report a user gets that a re-paste was a no-op rather than a rewrite. */
export function summariseImport(res: ImportServersResult | null): string {
  const results = res?.results ?? [];
  const created = results.filter((r) => r.outcome === "created").length;
  const unchanged = results.length - created;
  const parts: string[] = [];
  if (created > 0) {
    parts.push(`Connected ${created} integration${created === 1 ? "" : "s"}`);
  }
  if (unchanged > 0) {
    parts.push(`${unchanged} already configured`);
  }
  if (parts.length === 0) {
    parts.push("Nothing to connect");
  }
  const notes = res?.notes ?? [];
  const onlyNote = notes.length === 1 ? notes[0] : undefined;
  if (onlyNote !== undefined) {
    parts.push(onlyNote);
  } else if (notes.length > 1) {
    parts.push(`${notes.length} keys vibekit does not store were ignored`);
  }
  return parts.join(". ") + ".";
}

// --- mcp.search_registry ---

interface SearchRegistryArgs {
  q: string;
}

// No automatic retry, alone among the MCP actions. The registry refuses
// connections after a burst, so `retryNetwork` + RETRY_STANDARD turned one slow
// query into three attempts against the endpoint that was already refusing, and
// made the user wait out every upstream timeout in series before the panel said
// anything. The panel's own Retry button is the retry, and typing one more
// character is the other one.
export const searchRegistry = apiAction<SearchRegistryArgs, RegistrySearchResult>({
  name: "mcp.search_registry",
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

// --- mcp.relay_oauth_callback ---
//
// Rescue a sign-in whose redirect landed on the wrong machine. KAS binds its
// OAuth redirect listener on the CONTAINER's localhost, so a browser reaching
// vibekit from a phone or another laptop is sent to its own localhost, where
// nothing is listening. The user pastes that dead address here and the server
// replays it inward. Server contract and its validation:
// `internal/hub/mcp_oauth_relay.go`.

/** Result of POST /api/mcp/oauth-relay: the loopback listener's HTTP status. */
export interface OAuthRelayResult {
  status: number;
}

export const relayOAuthCallback = apiAction<
  { server: string; redirect_url: string },
  OAuthRelayResult
>({
  name: "mcp.relay_oauth_callback",
  // Deliberately NO retry, matching deleteServer's reasoning: an authorization
  // code is single-use, so a replay of a request that may already have been
  // delivered spends it against a listener that will refuse the second copy.
  // The server latches the attempt for the same reason.
  scope: (args) => "mcp-oauth-relay:" + args.server,
  request: ({ server, redirect_url }) => ({
    method: "POST",
    path: `${MCP_API}/oauth-relay`,
    body: { server, redirect_url },
  }),
  // The panel shows the refusal inline, beside the box the address was pasted
  // into: every rejection names which part of the address was wrong, and that
  // belongs next to the field rather than in a toast that outlives it.
  error: false,
});
