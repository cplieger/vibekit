// Actions for the Forge auth panel: sign-out, device flow start,
// clone repo, delete local repo. Batch operations (clone_all,
// delete_all) live in forge-auth.ts — they fan out per-repo
// action.dispatch() calls with button progress and aggregate toast.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";

// --- Types local to this slice ---

export interface CloneArgs {
  url: string;
}

export interface DeleteLocalArgs {
  repoName: string;
}

export interface SignOutArgs {
  forgeId: string;
}

export interface StartDeviceFlowArgs {}

interface DeviceFlowResponse {
  user_code: string;
  verification_uri: string;
  device_code: string;
  interval: number;
  expires_in: number;
}

// --- Actions ---

/** Start the GitHub OAuth device flow. Returns the device flow
 *  response on success or null on failure (toast fires). */
export const startDeviceFlow = apiAction<StartDeviceFlowArgs, DeviceFlowResponse>({
  name: "forge.start_device_flow",
  request: () => ({
    method: "POST",
    path: "/api/forges/oauth/github/start",
    body: {},
  }),
  error: "Couldn't start device flow",
});

/** Sign out of a forge account (delete the token). */
export const signOut = apiAction<SignOutArgs, unknown>({
  name: "forge.sign_out",
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
});
