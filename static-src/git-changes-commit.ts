// ---------------------------------------------------------------------------
// Commit workflow rendering: commit message textarea + AI generate +
// recent commits collapsible section. Extracted from git-changes-tab.ts
// to isolate the commit-specific UI concern.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { withAsyncFeedback } from "./async-button.js";
import { bindLoadingState } from "./actions/index.js";
import { commit as commitAction, generateCommitMessage } from "./actions/git-changes.js";
import { el } from "@cplieger/reactive";
import type { GitRepoStatus } from "./git-types.js";

type RepoStatus = GitRepoStatus;

/** Dependencies injected from git-changes-tab module state. */
export interface CommitDeps {
  commitMessages: Map<string, string>;
  bindingCleanups: (() => void)[];
  diffAbort: AbortController | null;
  refreshChanges: () => Promise<void>;
  assertOk: <T>(result: T) => asserts result is NonNullable<T>;
}

/** Render the recent-commits collapsible section for a repo. */
export function renderRecentCommits(r: RepoStatus, deps: CommitDeps): HTMLElement {
  const body = el("div", { className: "git-recent-commits-body" }, "Loading…");
  const wrap = el(
    "details",
    { className: "git-recent-commits" },
    el("summary", { className: "git-recent-commits-summary" }, "Recent commits"),
    body,
  ) as HTMLDetailsElement;

  let loaded = false;
  wrap.addEventListener("toggle", () => {
    if (!wrap.open || loaded) {
      return;
    }
    loaded = true;
    void apiGet<{ entries?: string[]; remote?: string; behind?: number }>(
      `/api/git/log?repo=${encodeURIComponent(r.repo)}`,
      deps.diffAbort?.signal,
    ).then((data) => {
      if (deps.diffAbort?.signal.aborted) {
        return;
      }
      if (data === null) {
        body.textContent = "Failed to load.";
        return;
      }
      const entries = data.entries ?? [];
      if (entries.length === 0) {
        body.textContent = "No commits.";
        return;
      }
      body.replaceChildren();
      const list = el("ul", { className: "git-recent-commits-list" });
      for (const line of entries.slice(0, 20)) {
        const li = el("li", { className: "git-recent-commits-row" });
        // line shape: "<sha> <subject>"
        const sp = line.indexOf(" ");
        if (sp > 0) {
          li.appendChild(el("code", { className: "git-recent-commits-sha" }, line.slice(0, sp)));
          li.appendChild(
            el("span", { className: "git-recent-commits-subject" }, line.slice(sp + 1)),
          );
        } else {
          li.textContent = line;
        }
        list.appendChild(li);
      }
      body.appendChild(list);
    });
  });

  return wrap;
}

/** Render the commit message textarea + AI generate + Commit button. */
export function renderCommitArea(r: RepoStatus, deps: CommitDeps): HTMLElement {
  const wrap = el("div", { className: "git-commit-area" });

  const ta = el("textarea", {
    className: "git-commit-input",
    placeholder: "Commit message…",
    rows: 2,
    "data-repo": r.repo,
  }) as HTMLTextAreaElement;
  // Restore previously typed commit message.
  const saved = deps.commitMessages.get(r.repo);
  if (saved) {
    ta.value = saved;
  }
  wrap.appendChild(ta);

  const row = el("div", { className: "git-commit-row" });

  const ai = el(
    "button",
    {
      type: "button",
      className: "btn-small",
      "data-tooltip": "Generate commit message from staged changes",
    },
    "✨ AI message",
  ) as HTMLButtonElement;
  ai.addEventListener("click", () => {
    void withAsyncFeedback(ai, async () => {
      const msg = await generateCommitMessage.dispatch({ repo: r.repo });
      deps.assertOk(msg);
      // Server returns {output}; only fill when non-empty so a failed/empty
      // generation never wipes a message the user already typed.
      const generated = msg.output ?? "";
      if (generated !== "") {
        deps.commitMessages.set(r.repo, generated);
        if (ta.isConnected) {
          ta.value = generated;
        }
      }
    });
  });
  row.appendChild(ai);
  deps.bindingCleanups.push(bindLoadingState("git.generate_message", ai));

  const commit = el(
    "button",
    { type: "button", className: "btn-small btn-primary" },
    "Commit",
  ) as HTMLButtonElement;
  commit.addEventListener("click", () => {
    void withAsyncFeedback(commit, async () => {
      const message = ta.value.trim();
      if (message === "") {
        throw new Error("Commit message required");
      }
      deps.assertOk(await commitAction.dispatch({ repo: r.repo, message }));
      ta.value = "";
      deps.commitMessages.delete(r.repo);
      await deps.refreshChanges();
    });
  });
  row.appendChild(commit);
  deps.bindingCleanups.push(bindLoadingState("git.commit", commit));

  wrap.appendChild(row);
  return wrap;
}
