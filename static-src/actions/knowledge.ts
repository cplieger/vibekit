// Knowledge-base actions: add / remove workspace knowledge contexts.
//
// The knowledge list is server-canonical (it lives in kiro-cli's global store,
// not vibekit's chat store) and is refetched after every mutation, so these
// actions carry no optimistic state — `add` is async/background anyway (the
// server returns a "indexing in background" message). See knowledge.ts.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";

/** Base path for the knowledge API — single source of truth. */
const KNOWLEDGE_API = "/api/knowledge";

// --- knowledge.add ---

interface AddArgs {
  path: string;
  name?: string;
}

/** POST /api/knowledge {path, name?} — start a background index of a directory.
 *  `error: false`: the add form surfaces the server's validation message inline
 *  (bad path, missing args) rather than as a toast. */
export const addKnowledge = apiAction<AddArgs, { message?: string }>({
  name: "knowledge.add",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ path, name }) => ({
    method: "POST",
    path: KNOWLEDGE_API,
    body: name !== undefined && name !== "" ? { path, name } : { path },
  }),
  error: false,
});

// --- knowledge.remove ---

interface RemoveArgs {
  name: string;
}

// No auto-retry: a timed-out DELETE may have succeeded server-side; retrying
// would hit 404 and surface a misleading error.
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
export const removeKnowledge = apiAction<RemoveArgs, void>({
  name: "knowledge.remove",
  dedupe: (args) => `knowledge.remove:${args.name}`,
  request: ({ name }) => ({
    method: "DELETE",
    path: `${KNOWLEDGE_API}/${encodeURIComponent(name)}`,
  }),
  error: "Couldn't remove knowledge base",
});
