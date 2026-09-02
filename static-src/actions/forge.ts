// Actions for the Forge auth panel: sign-out, device flow start,
// clone repo, delete local repo. Batch operations (clone_all,
// delete_all) live in forge-auth.ts — they fan out per-repo
// action.dispatch() calls with button progress and aggregate toast.
// ---------------------------------------------------------------------------

import {
  apiAction,
  defineAction,
  retryNetwork,
  RETRY_STANDARD,
  ActionError,
  classifyFetchError,
} from "./index.js";
import { withTimeout } from "@cplieger/fetch";

import type { DeviceFlowResponse, ForgeKind } from "../wire/types.gen.js";

// --- Types local to this slice ---

interface CloneArgs {
  url: string;
}

interface DeleteLocalArgs {
  repoName: string;
}

interface SignOutArgs {
  forgeId: string;
}

// --- Actions ---

/** Start the GitHub OAuth device flow. Returns the device flow
 *  response on success or null on failure. Error toast suppressed —
 *  the callsite renders inline status instead. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const startDeviceFlow = apiAction<void, DeviceFlowResponse>({
  name: "forge.start_device_flow",
  dedupe: true,
  retryable: retryNetwork,
  request: () => ({
    method: "POST",
    path: "/api/forges/oauth/github/start",
    body: {},
  }),
  error: false,
});

/** Sign out of a forge account (delete the token).
 *  Not retryable: a timed-out DELETE may have succeeded server-side;
 *  retrying would hit 404 and surface a misleading error toast. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const signOut = apiAction<SignOutArgs, void>({
  name: "forge.sign_out",
  request: ({ forgeId }) => ({
    method: "DELETE",
    path: `/api/forges/${encodeURIComponent(forgeId)}`,
  }),
  error: "Couldn't sign out",
});

/** How long the client waits out one clone. Above the server's own
 *  10-minute clone budget (internal/git defaultTimeouts), so the verdict
 *  the user sees is the server's — a git error or the clone's success —
 *  never a client-side abort. The abort was the original defect: the
 *  standard 30s API timeout cancelled the request, which killed the git
 *  subprocess mid-transfer (the handler's context derives from the
 *  request), so any repo too large to clone in 30s could never be cloned
 *  from the UI, and the retry that followed hit the half-cloned
 *  destination's "already exists" refusal. */
const CLONE_TIMEOUT_MS = 11 * 60_000;

/** Clone a single repo into the workspace. Error toast suppressed —
 *  callers handle toasting (single-repo toasts directly, batch
 *  aggregates).
 *
 *  Raw fetch on a long timeout rather than apiAction, whose transport pins
 *  the standard 30s timeout with no per-action override (the files.download
 *  precedent). NOT retryable: an interrupted clone may have left a partial
 *  destination server-side, so a retry reports a misleading
 *  "already exists" instead of the real failure. */
export const cloneRepo = defineAction<CloneArgs, { output?: string; error?: string }>({
  name: "forge.clone_repo",
  run: async ({ url }, signal) => {
    let r: Response;
    try {
      r = await fetch("/api/git/clone", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ url }),
        signal: withTimeout(signal, CLONE_TIMEOUT_MS),
      });
    } catch (e) {
      throw classifyFetchError(e, signal);
    }
    if (!r.ok) {
      throw new ActionError("Clone failed", { status: r.status });
    }
    // The git endpoints answer 200 with an {error} envelope on git
    // failure (writeCmdResult); callers read res.error.
    return (await r.json()) as { output?: string; error?: string };
  },
  error: false,
});

/** Remove a locally-cloned repo from the workspace. Error toast
 *  suppressed — callers handle toasting. */
export const deleteLocal = apiAction<DeleteLocalArgs, { status?: string; error?: string }>({
  name: "forge.delete_local",
  request: ({ repoName }) => ({
    method: "POST",
    path: "/api/git/remove",
    body: { repo: repoName },
  }),
  error: false,
  // Not retryable: a timed-out delete may have succeeded server-side.
});

// --- PAT connect ---

interface ConnectPATArgs {
  kind: ForgeKind;
  host: string;
  token: string;
}

/** Connect a forge account via PAT. Error toast suppressed — the
 *  form renders inline error status. */
export const connectPAT = apiAction<ConnectPATArgs, { status?: string; error?: string }>({
  name: "forge.connect_pat",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: ({ kind, host, token }) => ({
    method: "POST",
    path: `/api/forges/${encodeURIComponent(`${kind}:${host}`)}/login/pat`,
    body: { token },
  }),
  error: false,
});
