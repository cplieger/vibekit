// Actions for the Git Changes tab. Each user-initiated mutation gets
// its own action with typed args and a descriptive error prefix.
//
// All actions POST to /api/git/<op> with { repo, ...body }. The server
// replies HTTP 200 for BOTH outcomes (internal/git/helpers.go
// writeCmdResult): {"output": "..."} / {"ok": true} on success and
// {"error": "<scrubbed git output>"} on failure — presence of a
// non-empty `error` field is the failure signal, NOT the HTTP status.
// decodeGitResult owns that envelope through apiAction's decode seam:
// the error arm throws ActionError (code "git", never retried — the
// command may have had side effects), so the framework's error toast
// carries the server's scrubbed git message, the success toast never
// fires on a failed mutation, and dispatch resolves null so callers'
// assertOk guards fail closed.
// ---------------------------------------------------------------------------

import { apiAction, ActionError, hasErrorString, retryNetwork, RETRY_STANDARD } from "./index.js";

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
 *  failure carries a non-empty `error`. decodeGitResult converts the
 *  error arm into a thrown ActionError, so a RESOLVED dispatch is a
 *  genuine success and `error` is never set on returned values. */
export interface GitCmdResult {
  output?: string;
  error?: string;
}

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

/** Compose the message a failed /api/git envelope should carry: the kind, plus
 *  the `detail` field when the server supplied one.
 *
 *  Without the detail the toast reads `Couldn't suggest a branch name:
 *  generation_failed` — a machine discriminator and nothing a reader can act
 *  on, while the server had already put the cause in the same body. The two
 *  callers that pass an EMPTY detail do so deliberately (`no_staged_changes`,
 *  `not_in_repo` say everything in their kind), so those keep rendering bare. */
function errorMessage(data: { error: string }): string {
  const detail = (data as { detail?: unknown }).detail;
  return typeof detail === "string" && detail !== "" ? `${data.error}: ${detail}` : data.error;
}

/** Decode the /api/git 200-with-error envelope (apiAction's decode seam).
 *
 *  HTTP 200 + {"error": …} means the git subprocess failed; code "git"
 *  (never retried: the command may have had side effects). Transport
 *  failures and genuine non-2xx responses keep apiAction's default
 *  mapping (codes "network"/"timeout"/"cancelled", retryNetwork-eligible;
 *  status + lifted body error for non-2xx). Shared with git-branch.ts. */
export function decodeGitResult(data: unknown): GitCmdResult {
  if (hasErrorString(data) && data.error !== "") {
    // HTTP 200 + {"error": …}: the git subprocess failed (18-F1).
    throw new ActionError(errorMessage(data), { code: "git" });
  }
  return liftOutput(data);
}

// --- Actions ---

/** Stage files (used for both "stage all" and single-file stage). */
export const stage = apiAction<GitRepoFilesArgs, GitCmdResult>({
  name: "git.stage",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stage", body: args }),
  decode: decodeGitResult,
  error: (args, err) =>
    args.files.length === 1
      ? // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by length === 1
        `Couldn't stage \u201c${truncate(args.files[0]!)}\u201d: ${err.message}`
      : `Couldn't stage ${String(args.files.length)} files: ${err.message}`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Discard files (used for both "discard all" and single-file discard). */
export const discard = apiAction<GitRepoFilesArgs, GitCmdResult>({
  name: "git.discard",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/discard", body: args }),
  decode: decodeGitResult,
  error: (args, err) =>
    args.files.length === 1
      ? // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by length === 1
        `Couldn't discard \u201c${truncate(args.files[0]!)}\u201d: ${err.message}`
      : `Couldn't discard ${String(args.files.length)} files: ${err.message}`,
  // Destructive: timed-out discard may have succeeded server-side
});

/** Unstage a file. */
export const unstage = apiAction<GitRepoFilesArgs, GitCmdResult>({
  name: "git.unstage",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/unstage", body: args }),
  decode: decodeGitResult,
  error: (args, err) =>
    args.files.length === 1
      ? // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by length === 1
        `Couldn't unstage \u201c${truncate(args.files[0]!)}\u201d: ${err.message}`
      : `Couldn't unstage ${String(args.files.length)} files: ${err.message}`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

export const pull = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.pull",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/pull", body: args }),
  decode: decodeGitResult,
  success: (args) => (args.repo !== "" ? `Pulled ${args.repo}` : "Pulled"),
  error: "Pull failed",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

export const push = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.push",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/push", body: args }),
  decode: decodeGitResult,
  success: (args) => (args.repo !== "" ? `Pushed ${args.repo}` : "Pushed"),
  error: "Push failed",
  // Not retryable: a timed-out push may have succeeded server-side.
});

export const stash = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.stash",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stash", body: args }),
  decode: decodeGitResult,
  error: "Stash failed",
  idempotencyKey: true,
  // Idempotent server-side via Idempotency-Key dedup; left non-retryable for now.
});

export const stashPop = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.stash_pop",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stash-pop", body: args }),
  decode: decodeGitResult,
  error: "Stash pop failed",
  idempotencyKey: true,
  // Idempotent server-side via Idempotency-Key dedup; left non-retryable for now.
});

export const commit = apiAction<{ repo: string; message: string }, GitCmdResult>({
  name: "git.commit",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/commit", body: args }),
  decode: decodeGitResult,
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

export const generateCommitMessage = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.generate_message",
  scope: (args) => "git:" + args.repo,
  dedupe: (args) => args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/commit-message", body: args }),
  decode: decodeGitResult,
  error: "Couldn't generate commit message",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});
