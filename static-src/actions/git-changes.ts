// Actions for the Git Changes tab. Each user-initiated mutation gets
// its own action with typed args and a descriptive error prefix.
//
// All actions POST to /api/git/<op> with { repo, ...body }. The server
// replies HTTP 200 for BOTH outcomes (internal/git/helpers.go
// writeCmdResult): {"output": "..."} / {"ok": true} on success and
// {"error": "<scrubbed git output>"} on failure — presence of a
// non-empty `error` field is the failure signal, NOT the HTTP status.
// apiAction resolves any 2xx body as success (it has no response-decode
// seam), which used to turn every git failure into a success toast; so
// these actions use defineAction with one shared runner (runGitPost)
// that throws ActionError on the error envelope. The framework's error
// toast then carries the server's scrubbed git message, the success
// toast never fires on a failed mutation, and dispatch resolves null so
// callers' assertOk guards fail closed.
// ---------------------------------------------------------------------------

import {
  defineAction,
  ActionError,
  classifyFetchError,
  hasErrorString,
  retryNetwork,
  withTimeout,
  API_TIMEOUT_MS,
  RETRY_STANDARD,
} from "./index.js";
import type { ActionContext } from "./index.js";

import { truncate } from "../strings.js";

// --- Wire types ---

interface GitRepoArgs {
  repo: string;
}

interface GitRepoFilesArgs extends GitRepoArgs {
  files: string[];
}

/** Result envelope of every /api/git mutation. Success carries an
 *  optional `output` (git's combined output, ScrubAuth'd server-side);
 *  failure carries a non-empty `error`. runGitPost converts the error
 *  arm into a thrown ActionError, so a RESOLVED dispatch is a genuine
 *  success and `error` is never set on returned values. */
export interface GitCmdResult {
  output?: string;
  error?: string;
}

// --- Shared envelope-checked POST runner ---

/** Same header apiAction's executeRequest sets from ctx.idempotencyKey;
 *  the server's REST dedup middleware (internal/server/idempotency.go)
 *  keys on it to replay retried mutations instead of re-executing. */
const IDEMPOTENCY_HEADER = "Idempotency-Key";

/** Narrow a parsed success body to the fields GitCmdResult carries. */
function liftOutput(parsed: unknown): GitCmdResult {
  if (typeof parsed === "object" && parsed !== null && "output" in parsed) {
    const out = (parsed as { output?: unknown }).output;
    if (typeof out === "string") {
      return { output: out };
    }
  }
  return {};
}

/** POST a git mutation and decode the 200-with-error envelope.
 *
 *  Failure modes, all thrown as ActionError so the framework's error
 *  toast + registry log observe them:
 *   - transport failure / timeout / cancellation → classifyFetchError
 *     (codes "network" / "timeout" / "cancelled", retryNetwork-eligible)
 *   - genuine non-2xx → status + the body's `error` message when present
 *   - HTTP 200 + {"error": …} → the git subprocess failed; code "git"
 *     (never retried: the command may have had side effects)
 *
 *  Raw fetch is sanctioned here: action run() implementations are part
 *  of the framework's HTTP surface (see actions/files.ts precedent);
 *  apiAction can't express the envelope check. */
async function runGitPost(
  path: string,
  body: unknown,
  signal: AbortSignal,
  ctx?: ActionContext,
): Promise<GitCmdResult> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (ctx?.idempotencyKey !== undefined) {
    headers[IDEMPOTENCY_HEADER] = ctx.idempotencyKey;
  }
  let res: Response;
  try {
    res = await fetch(path, {
      method: "POST",
      headers,
      body: JSON.stringify(body),
      signal: withTimeout(signal, API_TIMEOUT_MS),
    });
  } catch (e) {
    throw classifyFetchError(e, signal);
  }
  let parsed: unknown;
  try {
    parsed = await res.json();
  } catch (e) {
    if (signal.aborted) {
      throw classifyFetchError(e, signal); // aborted mid-body-read → "cancelled"
    }
    parsed = undefined; // non-JSON body: fall through to the status check
  }
  if (!res.ok) {
    const msg = hasErrorString(parsed) ? parsed.error : `HTTP ${String(res.status)}`;
    throw new ActionError(msg, { status: res.status });
  }
  if (hasErrorString(parsed) && parsed.error !== "") {
    // HTTP 200 + {"error": …}: the git subprocess failed (18-F1).
    throw new ActionError(parsed.error, { code: "git" });
  }
  return liftOutput(parsed);
}

// --- Actions ---

/** Stage files (used for both "stage all" and single-file stage). */
export const stage = defineAction<GitRepoFilesArgs, GitCmdResult>({
  name: "git.stage",
  scope: (args) => "git:" + args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/stage", args, signal, ctx),
  error: (args, err) =>
    args.files.length === 1
      ? // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by length === 1
        `Couldn't stage \u201c${truncate(args.files[0]!)}\u201d: ${err.message}`
      : `Couldn't stage ${String(args.files.length)} files: ${err.message}`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Discard files (used for both "discard all" and single-file discard). */
export const discard = defineAction<GitRepoFilesArgs, GitCmdResult>({
  name: "git.discard",
  scope: (args) => "git:" + args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/discard", args, signal, ctx),
  error: (args, err) =>
    args.files.length === 1
      ? // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by length === 1
        `Couldn't discard \u201c${truncate(args.files[0]!)}\u201d: ${err.message}`
      : `Couldn't discard ${String(args.files.length)} files: ${err.message}`,
  // Destructive: timed-out discard may have succeeded server-side
});

/** Unstage a file. */
export const unstage = defineAction<GitRepoFilesArgs, GitCmdResult>({
  name: "git.unstage",
  scope: (args) => "git:" + args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/unstage", args, signal, ctx),
  error: (args, err) =>
    args.files.length === 1
      ? // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by length === 1
        `Couldn't unstage \u201c${truncate(args.files[0]!)}\u201d: ${err.message}`
      : `Couldn't unstage ${String(args.files.length)} files: ${err.message}`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

export const pull = defineAction<GitRepoArgs, GitCmdResult>({
  name: "git.pull",
  scope: (args) => "git:" + args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/pull", args, signal, ctx),
  success: (args) => (args.repo !== "" ? `Pulled ${args.repo}` : "Pulled"),
  error: "Pull failed",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

export const push = defineAction<GitRepoArgs, GitCmdResult>({
  name: "git.push",
  scope: (args) => "git:" + args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/push", args, signal, ctx),
  success: (args) => (args.repo !== "" ? `Pushed ${args.repo}` : "Pushed"),
  error: "Push failed",
  // Not retryable: a timed-out push may have succeeded server-side.
});

export const stash = defineAction<GitRepoArgs, GitCmdResult>({
  name: "git.stash",
  scope: (args) => "git:" + args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/stash", args, signal, ctx),
  error: "Stash failed",
  idempotencyKey: true,
  // Idempotent server-side via Idempotency-Key dedup; left non-retryable for now.
});

export const stashPop = defineAction<GitRepoArgs, GitCmdResult>({
  name: "git.stash_pop",
  scope: (args) => "git:" + args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/stash-pop", args, signal, ctx),
  error: "Stash pop failed",
  idempotencyKey: true,
  // Idempotent server-side via Idempotency-Key dedup; left non-retryable for now.
});

export const commit = defineAction<{ repo: string; message: string }, GitCmdResult>({
  name: "git.commit",
  scope: (args) => "git:" + args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/commit", args, signal, ctx),
  success: "Committed",
  error: (args, err) => {
    const line = args.message.split("\n")[0] ?? "";
    const short = truncate(line);
    return short !== ""
      ? `Commit failed (\u201c${short}\u201d): ${err.message}`
      : `Commit failed: ${err.message}`;
  },
  idempotencyKey: true,
  // Not retryable: a timed-out commit may have succeeded server-side;
  // retrying would create a duplicate commit.
});

export const generateCommitMessage = defineAction<GitRepoArgs, GitCmdResult>({
  name: "git.generate_message",
  scope: (args) => "git:" + args.repo,
  dedupe: (args) => args.repo,
  run: (args, signal, ctx) => runGitPost("/api/git/commit-message", args, signal, ctx),
  error: "Couldn't generate commit message",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});
