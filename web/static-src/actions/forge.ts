// Actions for the Forge auth panel: sign-out, device flow start,
// clone repo, delete local repo. Batch operations (clone_all,
// delete_all) live in forge-auth.ts — they fan out per-repo
// action.dispatch() calls with button progress and aggregate toast.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";
import type { DeviceFlowResponse, ForgeKind } from "../wire/types.gen.js";

// --- Types local to this slice ---

interface CloneArgs {
  url: string;
}

export interface DeleteLocalArgs {
  repoName: string;
}

export interface SignOutArgs {
  forgeId: string;
}

// --- Actions ---

/** Start the GitHub OAuth device flow. Returns the device flow
 *  response on success or null on failure. Error toast suppressed —
 *  the callsite renders inline status instead. */
export const startDeviceFlow = apiAction<void, DeviceFlowResponse>({
  name: "forge.start_device_flow",
  dedupe: true,
  retryable: "network",
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
export const signOut = apiAction<SignOutArgs, void>({
  name: "forge.sign_out",
  retryable: false,
  request: ({ forgeId }) => ({
    method: "DELETE",
    path: `/api/forges/${encodeURIComponent(forgeId)}`,
  }),
  error: "Couldn't sign out",
});

/** Clone a single repo into the workspace. Error toast suppressed —
 *  callers handle toasting (single-repo toasts directly, batch
 *  aggregates). */
export const cloneRepo = apiAction<CloneArgs, { output?: string; error?: string }>({
  name: "forge.clone_repo",
  idempotencyKey: true,
  retryable: "network",
  retry: { count: 2, delay: 300 },
  request: ({ url }) => ({
    method: "POST",
    path: "/api/git/clone",
    body: { url },
  }),
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
  retryable: false,
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
  retryable: "network",
  retry: { count: 2, delay: 300 },
  request: ({ kind, host, token }) => ({
    method: "POST",
    path: `/api/forges/${encodeURIComponent(`${kind}:${host}`)}/login/pat`,
    body: { token },
  }),
  error: false,
});
