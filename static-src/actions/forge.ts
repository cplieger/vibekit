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

import type { DeviceFlowResponse, ForgeKind } from "../wire/types.gen.js";

// --- Types local to this slice ---

interface CloneArgs {
  url: string;
  /** Live progress lines from git's own stream, for a button label. */
  onProgress?: (line: string) => void;
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

/** How long the clone may go without the server streaming anything before
 *  the client gives up. NOT a bound on the clone itself: the server
 *  streams a progress line whenever git reports one, so a healthy
 *  transfer of any size resets this continuously — the timeout only fires
 *  when the stream has genuinely died. This replaced a wall-clock budget,
 *  whose original 30s cut killed any repo too large to clone in time (the
 *  abort cancels the request, whose context kills the git subprocess). */
const CLONE_STALL_TIMEOUT_MS = 3 * 60_000;

/** One line of the clone's NDJSON stream: progress while git transfers,
 *  then a final output/error envelope. */
interface CloneStreamLine {
  progress?: string;
  output?: string;
  error?: string;
}

/** Clone a single repo into the workspace. Error toast suppressed —
 *  callers handle toasting (single-repo toasts directly, batch
 *  aggregates).
 *
 *  Reads the server's NDJSON progress stream (raw fetch — the apiAction
 *  transport expects one JSON body on a fixed 30s timeout, and both
 *  halves are wrong for a transfer that legitimately runs for minutes).
 *  Liveness is measured, not budgeted: each received chunk re-arms the
 *  stall timer. NOT retryable: an interrupted clone may have left a
 *  partial destination server-side, so a retry reports a misleading
 *  "already exists" instead of the real failure. */
export const cloneRepo = defineAction<CloneArgs, { output?: string; error?: string }>({
  name: "forge.clone_repo",
  run: async ({ url, onProgress }, signal) => {
    const ctrl = new AbortController();
    // Aborting the fetch stops the network; cancelling the reader unblocks
    // a pending read() even when the body stream is not wired to the
    // signal (a Response handed to us by a test stub, some polyfills).
    let cancelStream: (() => void) | null = null;
    const die = (): void => {
      ctrl.abort();
      cancelStream?.();
    };
    signal.addEventListener("abort", die);
    let stallTimer = setTimeout(die, CLONE_STALL_TIMEOUT_MS);
    const armStall = (): void => {
      clearTimeout(stallTimer);
      stallTimer = setTimeout(die, CLONE_STALL_TIMEOUT_MS);
    };
    try {
      let r: Response;
      try {
        r = await fetch("/api/git/clone", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ url }),
          signal: ctrl.signal,
        });
      } catch (e) {
        throw classifyFetchError(e, signal);
      }
      if (!r.ok) {
        throw new ActionError("Clone failed", { status: r.status });
      }
      if (r.body === null) {
        throw new ActionError("Clone failed: empty response", { status: 0, code: "invalid" });
      }
      const reader = r.body.getReader();
      cancelStream = () => {
        void reader.cancel().catch(() => undefined);
      };
      const decoder = new TextDecoder();
      let buf = "";
      let final: CloneStreamLine | null = null;
      // Returns the line when it is the final envelope, null for progress
      // and noise — the assignment stays in this scope so TypeScript's
      // narrowing sees it.
      const takeLine = (line: string): CloneStreamLine | null => {
        if (line === "") {
          return null;
        }
        let obj: CloneStreamLine;
        try {
          obj = JSON.parse(line) as CloneStreamLine;
        } catch {
          return null; // a torn line is a transport artifact, not an envelope
        }
        if (obj.progress !== undefined) {
          onProgress?.(obj.progress);
          return null;
        }
        return obj;
      };
      for (;;) {
        let chunk: ReadableStreamReadResult<Uint8Array>;
        try {
          chunk = await reader.read();
        } catch (e) {
          throw classifyFetchError(e, signal);
        }
        if (chunk.done) {
          break;
        }
        armStall();
        buf += decoder.decode(chunk.value, { stream: true });
        let nl = buf.indexOf("\n");
        while (nl >= 0) {
          final = takeLine(buf.slice(0, nl).trim()) ?? final;
          buf = buf.slice(nl + 1);
          nl = buf.indexOf("\n");
        }
      }
      final = takeLine((buf + decoder.decode()).trim()) ?? final;
      if (final === null) {
        // No verdict: the stream stalled out or the server died mid-clone.
        throw new ActionError("Clone interrupted", { status: 0, code: "network" });
      }
      return final;
    } finally {
      clearTimeout(stallTimer);
      signal.removeEventListener("abort", die);
    }
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
