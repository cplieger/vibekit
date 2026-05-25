// Actions for the Git Changes tab. Each user-initiated mutation gets
// its own action with typed args and a descriptive error prefix.
// All actions POST to /api/git/<op> with { repo, ...body } and expect
// { ok: true } or { error: "..." } back. Server-side errors in the
// `error` field are surfaced as ActionError so the framework toasts them.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";

// --- Wire types ---

export interface GitRepoArgs {
  repo: string;
}

export interface GitRepoFilesArgs extends GitRepoArgs {
  files: string[];
}

// --- Actions ---

/** Stage files (used for both "stage all" and single-file stage). */
export const stage = apiAction<GitRepoFilesArgs, unknown>({
  name: "git.stage",
  request: (args) => ({ method: "POST", path: "/api/git/stage", body: args }),
  error: "Couldn't stage",
});

/** Discard files (used for both "discard all" and single-file discard). */
export const discard = apiAction<GitRepoFilesArgs, unknown>({
  name: "git.discard",
  request: (args) => ({ method: "POST", path: "/api/git/discard", body: args }),
  error: "Couldn't discard",
});

/** Unstage a file. */
export const unstage = apiAction<GitRepoFilesArgs, unknown>({
  name: "git.unstage",
  request: (args) => ({ method: "POST", path: "/api/git/unstage", body: args }),
  error: "Couldn't unstage",
});

export const pull = apiAction<{ repo: string }, unknown>({
  name: "git.pull",
  request: (args) => ({ method: "POST", path: "/api/git/pull", body: args }),
  error: "Pull failed",
  retryable: "network",
});

export const push = apiAction<{ repo: string }, unknown>({
  name: "git.push",
  request: (args) => ({ method: "POST", path: "/api/git/push", body: args }),
  error: "Push failed",
  retryable: "network",
});

export const stash = apiAction<{ repo: string }, unknown>({
  name: "git.stash",
  request: (args) => ({ method: "POST", path: "/api/git/stash", body: args }),
  error: "Stash failed",
  retryable: "network",
});

export const stashPop = apiAction<{ repo: string }, unknown>({
  name: "git.stash_pop",
  request: (args) => ({ method: "POST", path: "/api/git/stash-pop", body: args }),
  error: "Stash pop failed",
  retryable: "network",
});

export const commit = apiAction<{ repo: string; message: string }, unknown>({
  name: "git.commit",
  request: (args) => ({ method: "POST", path: "/api/git/commit", body: args }),
  error: "Commit failed",
  // Not retryable: a timed-out commit may have succeeded server-side;
  // retrying would create a duplicate commit.
  retryable: false,
});

export const generateCommitMessage = apiAction<{ repo: string }, { message?: string }>({
  name: "git.generate_message",
  request: (args) => ({ method: "POST", path: "/api/git/commit-message", body: args }),
  error: "Couldn't generate commit message",
  retryable: "network",
});
