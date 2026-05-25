// Actions for the Git Changes tab. Each user-initiated mutation gets
// its own action with typed args and a descriptive error prefix.
// All actions POST to /api/git/<op> with { repo, ...body } and expect
// { ok: true } or { error: "..." } back. Server-side errors in the
// `error` field are surfaced as ActionError so the framework toasts them.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";

// --- Wire types ---

interface GitRepoArgs {
  repo: string;
}

interface GitRepoFilesArgs extends GitRepoArgs {
  files: string[];
}

// --- Actions ---

/** Stage files (used for both "stage all" and single-file stage). */
export const stage = apiAction<GitRepoFilesArgs, unknown>({
  name: "git.stage",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stage", body: args }),
  error: (args) => args.files.length === 1
    ? `Couldn't stage \u201c${args.files[0]!.length > 40 ? args.files[0]!.slice(0, 37) + "\u2026" : args.files[0]!}\u201d`
    : `Couldn't stage ${String(args.files.length)} files`,
  retryable: "network",
  retry: { count: 2, delay: 300 },
});

/** Discard files (used for both "discard all" and single-file discard). */
export const discard = apiAction<GitRepoFilesArgs, unknown>({
  name: "git.discard",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/discard", body: args }),
  error: (args) => args.files.length === 1
    ? `Couldn't discard \u201c${args.files[0]!.length > 40 ? args.files[0]!.slice(0, 37) + "\u2026" : args.files[0]!}\u201d`
    : `Couldn't discard ${String(args.files.length)} files`,
  // Destructive: timed-out discard may have succeeded server-side
  retryable: false,
});

/** Unstage a file. */
export const unstage = apiAction<GitRepoFilesArgs, unknown>({
  name: "git.unstage",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/unstage", body: args }),
  error: (args) => args.files.length === 1
    ? `Couldn't unstage \u201c${args.files[0]!.length > 40 ? args.files[0]!.slice(0, 37) + "\u2026" : args.files[0]!}\u201d`
    : `Couldn't unstage ${String(args.files.length)} files`,
  retryable: "network",
  retry: { count: 2, delay: 300 },
});

export const pull = apiAction<{ repo: string }, unknown>({
  name: "git.pull",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/pull", body: args }),
  success: (args) => args.repo !== "" ? `Pulled ${args.repo}` : "Pulled",
  error: "Pull failed",
  retryable: "network",
  retry: { count: 2, delay: 300 },
});

export const push = apiAction<{ repo: string }, unknown>({
  name: "git.push",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/push", body: args }),
  success: (args) => args.repo !== "" ? `Pushed ${args.repo}` : "Pushed",
  error: "Push failed",
  // Not retryable: a timed-out push may have succeeded server-side.
  retryable: false,
});

// TODO: Add idempotencyKey back once the server reads the Idempotency-Key header
// and deduplicates stash creation server-side.
export const stash = apiAction<{ repo: string }, unknown>({
  name: "git.stash",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stash", body: args }),
  error: "Stash failed",
  // Not retryable: without server-side idempotency, a timed-out stash that
  // succeeded would create a duplicate stash on retry.
  retryable: false,
});

export const stashPop = apiAction<{ repo: string }, unknown>({
  name: "git.stash_pop",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stash-pop", body: args }),
  error: "Stash pop failed",
  // Not retryable: a timed-out stash pop may have succeeded server-side.
  retryable: false,
});

export const commit = apiAction<{ repo: string; message: string }, unknown>({
  name: "git.commit",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/commit", body: args }),
  success: "Committed",
  error: (args) => {
    const line = args.message.split("\n")[0] ?? "";
    const short = line.length > 40 ? line.slice(0, 37) + "\u2026" : line;
    return short !== "" ? `Commit failed: \u201c${short}\u201d` : "Commit failed";
  },
  // Not retryable: a timed-out commit may have succeeded server-side;
  // retrying would create a duplicate commit.
  retryable: false,
});

export const generateCommitMessage = apiAction<{ repo: string }, { message?: string }>({
  name: "git.generate_message",
  scope: (args) => "git:" + args.repo,
  dedupe: (args) => args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/commit-message", body: args }),
  error: "Couldn't generate commit message",
  retryable: "network",
  retry: { count: 2, delay: 300 },
});
