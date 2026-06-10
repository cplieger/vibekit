// Actions for the Git Changes tab. Each user-initiated mutation gets
// its own action with typed args and a descriptive error prefix.
// All actions POST to /api/git/<op> with { repo, ...body } and expect
// { ok: true } or { error: "..." } back. Server-side errors in the
// `error` field are surfaced as ActionError so the framework toasts them.
// ---------------------------------------------------------------------------

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";

import { truncate } from "../strings.js";

// --- Wire types ---

interface GitRepoArgs {
  repo: string;
}

interface GitRepoFilesArgs extends GitRepoArgs {
  files: string[];
}

// --- Actions ---

/** Stage files (used for both "stage all" and single-file stage). */
export const stage = apiAction<GitRepoFilesArgs>({
  name: "git.stage",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stage", body: args }),
  error: (args) =>
    args.files.length === 1
      ? // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by length === 1
        `Couldn't stage \u201c${truncate(args.files[0]!)}\u201d`
      : `Couldn't stage ${String(args.files.length)} files`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Discard files (used for both "discard all" and single-file discard). */
export const discard = apiAction<GitRepoFilesArgs>({
  name: "git.discard",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/discard", body: args }),
  error: (args) =>
    args.files.length === 1
      ? // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by length === 1
        `Couldn't discard \u201c${truncate(args.files[0]!)}\u201d`
      : `Couldn't discard ${String(args.files.length)} files`,
  // Destructive: timed-out discard may have succeeded server-side
});

/** Unstage a file. */
export const unstage = apiAction<GitRepoFilesArgs>({
  name: "git.unstage",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/unstage", body: args }),
  error: (args) =>
    args.files.length === 1
      ? // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by length === 1
        `Couldn't unstage \u201c${truncate(args.files[0]!)}\u201d`
      : `Couldn't unstage ${String(args.files.length)} files`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

export const pull = apiAction<{ repo: string }>({
  name: "git.pull",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/pull", body: args }),
  success: (args) => (args.repo !== "" ? `Pulled ${args.repo}` : "Pulled"),
  error: "Pull failed",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

export const push = apiAction<{ repo: string }>({
  name: "git.push",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/push", body: args }),
  success: (args) => (args.repo !== "" ? `Pushed ${args.repo}` : "Pushed"),
  error: "Push failed",
  // Not retryable: a timed-out push may have succeeded server-side.
});

export const stash = apiAction<{ repo: string }>({
  name: "git.stash",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stash", body: args }),
  error: "Stash failed",
  idempotencyKey: true,
  // Idempotent server-side via Idempotency-Key dedup; left non-retryable for now.
});

export const stashPop = apiAction<{ repo: string }>({
  name: "git.stash_pop",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stash-pop", body: args }),
  error: "Stash pop failed",
  idempotencyKey: true,
  // Idempotent server-side via Idempotency-Key dedup; left non-retryable for now.
});

export const commit = apiAction<{ repo: string; message: string }>({
  name: "git.commit",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/commit", body: args }),
  success: "Committed",
  error: (args) => {
    const line = args.message.split("\n")[0] ?? "";
    const short = truncate(line);
    return short !== "" ? `Commit failed: \u201c${short}\u201d` : "Commit failed";
  },
  idempotencyKey: true,
  // Not retryable: a timed-out commit may have succeeded server-side;
  // retrying would create a duplicate commit.
});

export const generateCommitMessage = apiAction<{ repo: string }, { message?: string }>({
  name: "git.generate_message",
  scope: (args) => "git:" + args.repo,
  dedupe: (args) => args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/commit-message", body: args }),
  error: "Couldn't generate commit message",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});
