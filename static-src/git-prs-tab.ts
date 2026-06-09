// ---------------------------------------------------------------------------
// Git Pull Requests tab: per-repo collapsible sections rendered into
// #git-prs-mount. Each section header shows the repo name + count of
// open PRs; the body lists each PR with title, author, age, and quick
// actions (open on forge, merge, close).
//
// Data is aggregated client-side: we list configured forges, then for
// each forge list its repos, then for each repo with a clone_url
// fetch the open PRs in parallel. This keeps the backend simple at
// the cost of N HTTP fetches per refresh — usually 5-15 round-trips
// for a typical workspace, fine over local LAN.
// ---------------------------------------------------------------------------

import { apiGet, apiPost } from "./api-client.js";
import { onSSE } from "./bus.js";
import { relativeTime } from "./utils-format.js";
import { kindTitle, FORGE_META } from "./forge-types.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import { ICON_REFRESH, ICON_PR_EMPTY, ICON_FILTER } from "./icons.js";
import { preserveGitScroll } from "./git-scroll.js";
import type { ConfiguredForge, Repo } from "./wire/types.gen.js";
import { mergePR, closePR, refreshPRs as refreshPRsAction } from "./actions/git-prs.js";
import { registerCleanup } from "./actions/index.js";
import { bindLoadingState } from "./actions/index.js";
import { bindPRPaint, getPRGroups, setPRGroups } from "./git-prs-state.js";
import { reconcile } from "./reconcile.js";
import { el } from "@cplieger/reactive";
import { escAttr as escapeHTML } from "./strings.js";

// --- Types ---

import type { GitPR as PR, GitRepoGroup as RepoGroup } from "./git-types.js";

interface ForgesListResponse {
  forges: ConfiguredForge[];
}
interface RepoListResponse {
  repos: Repo[];
}
interface PRListResponse {
  prs: PR[];
}

// --- State ---

let filterText = "";
let refreshGen = 0;
let refreshController: AbortController | null = null;
registerCleanup(() => refreshController?.abort());

// --- Canonical PR groups + optimistic mutations live in git-prs-state.ts ---
// (getPRGroups/setPRGroups own the array; removePRFromGroups and
// reinsertPRInGroups mutate it) to break the circular dependency with
// actions/git-prs.ts. The tab reads groups exclusively via getPRGroups().

// --- Public API ---

let prsInited = false;

export function initPRsTab(): void {
  if (prsInited) {
    return;
  }
  prsInited = true;
  bindPRPaint(paint);

  const filterEl = document.getElementById("git-prs-filter") as HTMLInputElement | null;
  filterEl?.addEventListener("input", () => {
    filterText = filterEl.value.trim().toLowerCase();
    paint();
  });

  // Manual refresh button next to the filter — mirrors the
  // pattern on the Changes tab. Spinner replaces the icon while
  // the parallel PR fetch is in flight.
  const refreshBtn = document.getElementById("git-refresh-prs-btn") as HTMLButtonElement | null;
  if (refreshBtn !== null) {
    refreshBtn.innerHTML = ICON_REFRESH;
    refreshBtn.addEventListener("click", () => {
      void refreshPRsAction.dispatch(undefined);
    });
    bindLoadingState("git.refresh_prs", refreshBtn);
  }

  // Refetch on forge credential changes; PRs list depends on which
  // forges are connected.
  onSSE("forges_changed", () => {
    void refreshPRsAction.dispatch(undefined);
  });
}

/** Force a full PR refresh (parallel fan-out across all credentialled
 *  repos). Safe to call multiple times — only the latest result wins. */
export async function refreshPRs(externalSignal?: AbortSignal): Promise<void> {
  const myGen = ++refreshGen;
  refreshController?.abort();
  refreshController = new AbortController();
  const signal = AbortSignal.any([refreshController.signal, AbortSignal.timeout(20_000)]);
  // Honour external signal (e.g. from action framework).
  // Capture local ref to avoid stale closure over module-level refreshController.
  const myController = refreshController;
  if (externalSignal) {
    externalSignal.addEventListener(
      "abort",
      () => {
        myController.abort();
      },
      { once: true },
    );
  }
  const forgesRes = await apiGet<ForgesListResponse>("/api/forges", signal);
  if (signal.aborted) {
    return;
  }
  if (forgesRes === null) {
    throw new Error("Failed to load forges");
  }
  const forges = forgesRes.forges.filter((f) => f.connected);

  // Build a flat (forge, owner/name) list to fetch.
  const tasks: { forge: ConfiguredForge; repo: Repo }[] = [];
  const repoResults = await Promise.all(
    forges.map((forge) =>
      apiGet<RepoListResponse>(`/api/forges/${encodeURIComponent(forge.id)}/repos`, signal).then(
        (res) => ({ forge, res }),
      ),
    ),
  );
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check
  if (signal.aborted) {
    return;
  }
  for (const { forge, res } of repoResults) {
    if (res === null) {
      continue;
    }
    for (const repo of res.repos) {
      tasks.push({ forge, repo });
    }
  }

  const groups: RepoGroup[] = await Promise.all(
    tasks.map(async ({ forge, repo }) => {
      try {
        const res = await apiGet<PRListResponse>(
          `/api/forges/${encodeURIComponent(forge.id)}/repos/${encodeURIComponent(repo.owner)}/${encodeURIComponent(repo.name)}/prs?state=open`,
          signal,
        );
        return {
          forge_id: forge.id,
          forge_kind: forge.kind,
          forge_host: forge.host,
          owner: repo.owner,
          name: repo.name,
          full_name: repo.full_name,
          prs: res?.prs ?? [],
        };
      } catch (err) {
        return {
          forge_id: forge.id,
          forge_kind: forge.kind,
          forge_host: forge.host,
          owner: repo.owner,
          name: repo.name,
          full_name: repo.full_name,
          prs: [],
          error: err instanceof Error ? err.message : String(err),
        };
      }
    }),
  );

  // Sort: repos with PRs first, then alphabetical by full_name.
  groups.sort((a, b) => {
    if ((a.prs.length === 0) !== (b.prs.length === 0)) {
      return a.prs.length === 0 ? 1 : -1;
    }
    return a.full_name.localeCompare(b.full_name);
  });

  // Bail if a newer refresh was started while we were fetching.
  if (myGen !== refreshGen) {
    return;
  }

  setPRGroups(groups);
  paint();
}

// --- Render ---

function paint(): void {
  preserveGitScroll(paintInner);
}

function paintInner(): void {
  const root = document.getElementById("git-prs-mount");
  if (root === null) {
    return;
  }

  const groups = getPRGroups();

  if (groups.length === 0) {
    root.innerHTML = renderEmptyState({
      icon: ICON_PR_EMPTY,
      title: "No connected forges",
      hint: "Open the <strong>Sources</strong> tab to add a forge account.",
    });
    return;
  }

  const visible: RepoGroup[] = [];
  for (const g of groups) {
    const matchesFilter =
      filterText === "" ||
      g.full_name.toLowerCase().includes(filterText) ||
      g.prs.some((pr) => pr.title.toLowerCase().includes(filterText));
    if (matchesFilter) {
      visible.push(g);
    }
  }

  // Aggregate: any open PRs at all? If not, show a centered empty
  // state instead of N collapsed sections.
  const totalOpen = visible.reduce((acc, g) => acc + g.prs.length, 0);
  if (totalOpen === 0 && filterText === "") {
    // Wipe any prior keyed sections, then show centered empty state.
    reconcile(root, [] as RepoGroup[], {
      key: (g) => g.full_name,
      mount: () => el("div"),
    });
    for (const child of [...root.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }
    root.innerHTML = renderEmptyState({
      icon: ICON_PR_EMPTY,
      title: "All caught up",
      hint: "No open pull requests across your connected forges.",
    });
    return;
  }

  if (visible.length === 0) {
    reconcile(root, [] as RepoGroup[], {
      key: (g) => g.full_name,
      mount: () => el("div"),
    });
    for (const child of [...root.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }
    root.innerHTML = renderEmptyState({
      icon: ICON_FILTER,
      title: "No matching pull requests",
      hint: "Adjust your filter to see more.",
    });
    return;
  }

  // Drop any prior non-keyed empty-state placeholder before reconciling.
  for (const child of [...root.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  reconcile(root, visible, {
    key: (g: RepoGroup) => g.full_name,
    mount: (g: RepoGroup) => renderGroup(g),
    update: (section: HTMLElement, g: RepoGroup) => {
      paintGroupBody(section, g);
    },
  });
}

/** Refresh a kept group section's count + body content. Header
 *  identity (and expansion state) is preserved across paints. */
function paintGroupBody(section: HTMLElement, g: RepoGroup): void {
  const count = g.prs.length;
  const countText = count === 0 ? "no open PRs" : `${count} open`;
  const meta = section.querySelector(".git-repo-section-meta");
  if (meta !== null) {
    meta.textContent = countText;
  }

  const body = section.querySelector<HTMLElement>(":scope > .git-repo-section-body");
  if (body === null) {
    return;
  }

  // Drop any non-keyed placeholders (error / empty rows) before reconcile.
  for (const child of [...body.children]) {
    if (
      (child as HTMLElement).getAttribute("data-reconcile-key") === null &&
      !child.classList.contains("git-pr-list")
    ) {
      child.remove();
    }
  }

  if (g.error !== undefined && g.error !== "") {
    body.replaceChildren();
    body.appendChild(
      el("div", { className: "git-repo-row-error" }, `Failed to load PRs: ${g.error}`),
    );
    return;
  }

  const groupMatchesFilter = filterText !== "" && g.full_name.toLowerCase().includes(filterText);
  const filtered =
    filterText === "" || groupMatchesFilter
      ? g.prs
      : g.prs.filter((pr) => pr.title.toLowerCase().includes(filterText));

  if (filtered.length === 0) {
    body.replaceChildren();
    body.appendChild(
      el(
        "div",
        { className: "git-repo-row-empty" },
        `No open pull requests on ${kindTitle(g.forge_kind)}.`,
      ),
    );
    return;
  }

  let list = body.querySelector<HTMLElement>(":scope > .git-pr-list");
  if (list === null) {
    list = el("ul", { className: "git-pr-list" });
    body.replaceChildren(list);
  }
  reconcile(list, filtered, {
    key: (pr: PR) => `${g.forge_id}:${pr.number}`,
    mount: (pr: PR) => renderPRRow(g, pr),
  });
}

// --- Empty-state markup helpers ---

function renderEmptyState(opts: { icon: string; title: string; hint: string }): string {
  return `
    <div class="git-multirepo-empty">
      <div class="git-multirepo-empty-icon">${opts.icon}</div>
      <div class="git-multirepo-empty-title">${opts.title}</div>
      <div class="git-multirepo-empty-hint">${opts.hint}</div>
    </div>
  `;
}

function renderGroup(g: RepoGroup): HTMLElement {
  const expandedDefault = g.prs.length > 0 || filterText !== "";

  const section = el("section", { className: "git-repo-section", "data-repo": g.full_name });
  if (expandedDefault) {
    section.classList.add("expanded");
  }

  // Header is a flex container that hosts: chevron + forge icon +
  // name + count (left side, click-to-toggle), and a right-aligned
  // [+ New PR] button. The button's stopPropagation keeps the
  // toggle from firing when the user clicks New PR.
  const header = el("div", { className: "git-repo-section-header git-repo-section-header-row" });

  const toggle = el("button", {
    type: "button",
    className: "git-repo-section-header-toggle",
    "aria-expanded": expandedDefault ? "true" : "false",
  });
  const count = g.prs.length;
  const countText = count === 0 ? "no open PRs" : `${count} open`;
  toggle.innerHTML = `
    <span class="git-repo-section-chevron" aria-hidden="true">▸</span>
    <span class="git-repo-section-forge-icon git-repo-section-forge-${g.forge_kind}" aria-hidden="true">${FORGE_META[g.forge_kind].icon}</span>
    <span class="git-repo-section-name">${escapeHTML(g.full_name)}</span>
    <span class="git-repo-section-meta">${escapeHTML(countText)}</span>
  `;
  toggle.addEventListener("click", () => {
    const open = section.classList.toggle("expanded");
    toggle.setAttribute("aria-expanded", open ? "true" : "false");
  });
  header.appendChild(toggle);

  const newBtn = el("button", { type: "button", className: "btn-small btn-primary" }, "+ New PR");
  newBtn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    openNewPRDialog(g);
  });
  header.appendChild(newBtn);

  section.appendChild(header);

  const body = el("div", { className: "git-repo-section-body" });

  if (g.error !== undefined && g.error !== "") {
    body.appendChild(
      el("div", { className: "git-repo-row-error" }, `Failed to load PRs: ${g.error}`),
    );
  } else if (g.prs.length === 0) {
    body.appendChild(
      el(
        "div",
        { className: "git-repo-row-empty" },
        `No open pull requests on ${kindTitle(g.forge_kind)}.`,
      ),
    );
  } else {
    const list = el("ul", { className: "git-pr-list" });
    const groupMatchesFilter = filterText !== "" && g.full_name.toLowerCase().includes(filterText);
    const filtered =
      filterText === "" || groupMatchesFilter
        ? g.prs
        : g.prs.filter((pr) => pr.title.toLowerCase().includes(filterText));
    for (const pr of filtered) {
      list.appendChild(renderPRRow(g, pr));
    }
    body.appendChild(list);
  }

  section.appendChild(body);
  return section;
}

function renderPRRow(g: RepoGroup, pr: PR): HTMLElement {
  const li = el("li", { className: "git-pr-row" });

  const meta = el("div", { className: "git-pr-row-meta" });

  // Title + number share one click target — opens the PR on the
  // forge in a new tab. Keeps the row reading like a link without
  // a redundant "Open" button on the right.
  const hasURL = pr.url !== undefined && pr.url !== "";
  const linkOrSpan = (cls: string, text: string): HTMLElement => {
    if (hasURL) {
      const a = el("a", { className: cls, target: "_blank", rel: "noreferrer" }, text);
      a.setAttribute("href", pr.url!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      return a;
    }
    return el("span", { className: cls }, text);
  };

  const num = linkOrSpan("git-pr-row-number", `#${pr.number}`);
  meta.appendChild(num);

  const title = linkOrSpan("git-pr-row-title", pr.title);
  title.setAttribute("data-tooltip", pr.title);
  meta.appendChild(title);

  if (pr.draft === true) {
    meta.appendChild(el("span", { className: "git-pr-row-tag" }, "draft"));
  }

  li.appendChild(meta);

  const sub = el("div", { className: "git-pr-row-sub" });
  const parts: string[] = [];
  if (pr.author !== undefined && pr.author !== "") {
    parts.push(`by @${pr.author}`);
  }
  if (pr.updated_at !== undefined && pr.updated_at > 0) {
    parts.push(relativeTime(pr.updated_at));
  }
  if (pr.source_branch !== "" && pr.target_branch !== "") {
    parts.push(`${pr.source_branch} → ${pr.target_branch}`);
  }
  sub.textContent = parts.join(" · ");
  li.appendChild(sub);

  // Actions
  const actions = el("div", { className: "git-pr-row-actions" });

  const merge = el(
    "button",
    { type: "button", className: "btn-small btn-primary" },
    "Merge",
  ) as HTMLButtonElement;
  const mergeReason = computeMergeBlockReason(pr);
  merge.disabled = mergeReason !== "";
  merge.setAttribute(
    "data-tooltip",
    mergeReason !== "" ? `Cannot merge: ${mergeReason}` : "Merge this pull request",
  );
  merge.addEventListener("click", () => {
    void (async () => {
      const ok = await confirmDialog(`Merge PR #${pr.number} (${pr.title})?`, "Merge", "normal");
      if (!ok) {
        return;
      }
      await withAsyncFeedback(merge, async () => {
        const res = await mergePR.dispatch({
          forge_id: g.forge_id,
          owner: g.owner,
          name: g.name,
          pr_number: pr.number,
        });
        if (res === null) {
          throw new Error("failed");
        }
        // Skip refreshPRs — optimistic remove already shows correct state.
      });
    })();
  });
  actions.appendChild(merge);

  const close = el(
    "button",
    { type: "button", className: "btn-small btn-danger" },
    "Close",
  ) as HTMLButtonElement;
  close.addEventListener("click", () => {
    void (async () => {
      const ok = await confirmDialog(
        `Close PR #${pr.number} without merging?`,
        "Close PR",
        "destructive",
      );
      if (!ok) {
        return;
      }
      await withAsyncFeedback(close, async () => {
        const res = await closePR.dispatch({
          forge_id: g.forge_id,
          owner: g.owner,
          name: g.name,
          pr_number: pr.number,
        });
        if (res === null) {
          throw new Error("failed");
        }
        // Skip refreshPRs — optimistic remove already shows correct state.
      });
    })();
  });
  actions.appendChild(close);

  li.appendChild(actions);
  return li;
}

// --- Create-PR flow ---
//
// Lightweight inline form for now. The fancier two-stage modal in
// pr-panel.ts can be wired in later if we miss it; this gets the
// flow working in the rewrite without a port of the old single-repo
// dialog.

/** Open the New PR dialog targeting a specific local repo by name.
 *  Used by the contextual "Open PR" hint on the Changes tab. If we
 *  haven't fetched groups yet, refetch first; the source branch is
 *  pre-filled from the call site so the user doesn't have to retype. */
export async function openNewPRForRepo(repoName: string, sourceBranch: string): Promise<void> {
  if (getPRGroups().length === 0) {
    try {
      await refreshPRs();
    } catch {
      /* ignore — we'll check the groups below */
    }
  }
  const group = getPRGroups().find((g) => g.name === repoName);
  if (group === undefined) {
    // Repo not in any forge group (probably not on a connected
    // forge). Render a small inline error in the mount.
    const root = document.getElementById("git-prs-mount");
    if (root !== null) {
      root.innerHTML = `<div class="git-multirepo-error">No connected forge knows about <strong>${escapeHTML(repoName)}</strong> — connect one in Sources.</div>`;
    }
    return;
  }
  openNewPRDialog(group, sourceBranch);
}

function openNewPRDialog(g: RepoGroup, sourceBranch = ""): void {
  const dlg = document.getElementById("pr-create-dialog") as HTMLDialogElement | null;
  if (dlg === null) {
    return;
  }

  const baseInput = document.getElementById("pr-base") as HTMLInputElement | null;
  const headInput = document.getElementById("pr-head") as HTMLInputElement | null;
  const titleInput = document.getElementById("pr-title") as HTMLInputElement | null;
  const bodyInput = document.getElementById("pr-body") as HTMLTextAreaElement | null;
  const draftInput = document.getElementById("pr-draft") as HTMLInputElement | null;
  const status = document.getElementById("pr-dialog-status");
  const submitBtn = document.getElementById("pr-submit-btn") as HTMLButtonElement | null;
  const generateBtn = document.getElementById("pr-generate-btn") as HTMLButtonElement | null;

  if (
    !baseInput ||
    !headInput ||
    !titleInput ||
    !bodyInput ||
    !draftInput ||
    !submitBtn ||
    !generateBtn
  ) {
    console.error("PR dialog missing required elements");
    return;
  }

  // Stage 1: edit. Pre-fill base/head, generate title+body via AI.
  baseInput.value = "main";
  headInput.value = sourceBranch;
  titleInput.value = "";
  bodyInput.value = "";
  draftInput.checked = false;
  if (status !== null) {
    status.textContent = "Generating description…";
    status.className = "forge-status";
  }

  // Drop any prior listeners by cloning the buttons.
  const newSubmit = submitBtn.cloneNode(true) as HTMLButtonElement;
  submitBtn.replaceWith(newSubmit);
  newSubmit.disabled = false;
  const newGenerate = generateBtn.cloneNode(true) as HTMLButtonElement;
  generateBtn.replaceWith(newGenerate);
  // The static close buttons (data-pr-close) keep their close handler
  // wired in index.html-side via this closure too.
  for (const btn of dlg.querySelectorAll<HTMLButtonElement>("[data-pr-close]")) {
    const fresh = btn.cloneNode(true) as HTMLButtonElement;
    btn.replaceWith(fresh);
    fresh.addEventListener("click", () => {
      dlg.close();
    });
  }

  let generateAbort = new AbortController();
  dlg.addEventListener(
    "close",
    () => {
      generateAbort.abort();
    },
    { once: true },
  );

  const generate = async (): Promise<void> => {
    if (status !== null) {
      status.textContent = "Generating description…";
    }
    const res = await apiPost<{ title?: string; body?: string; error?: string }>(
      `/api/git/pr-description`,
      { repo: g.name, branch: baseInput.value.trim() || "main" },
      generateAbort.signal,
    );
    if (res === null) {
      if (generateAbort.signal.aborted) {
        return;
      }
      if (status !== null) {
        status.textContent = "Network error.";
        status.className = "forge-status err";
      }
      return;
    }
    if (res.error !== undefined && res.error !== "") {
      if (status !== null) {
        status.textContent = res.error;
        status.className = "forge-status err";
      }
      return;
    }
    if (res.title !== undefined && titleInput.value === "") {
      titleInput.value = res.title;
    }
    if (res.body !== undefined && bodyInput.value === "") {
      bodyInput.value = res.body;
    }
    if (status !== null) {
      status.textContent = "Description generated. Edit and submit.";
      status.className = "forge-status ok";
    }
  };

  newGenerate.addEventListener("click", () => {
    generateAbort.abort();
    generateAbort = new AbortController();
    void generate();
  });

  // Stage 2: review + submit.
  // eslint-disable-next-line @typescript-eslint/no-misused-promises
  newSubmit.addEventListener("click", async () => {
    newSubmit.disabled = true;
    if (status !== null) {
      status.textContent = "Opening PR…";
      status.className = "forge-status";
    }
    const res = await apiPost<{ number?: number; error?: string }>(
      `/api/forges/${encodeURIComponent(g.forge_id)}/repos/${encodeURIComponent(g.owner)}/${encodeURIComponent(g.name)}/prs`,
      {
        source_branch: headInput.value.trim(),
        target_branch: baseInput.value.trim(),
        title: titleInput.value.trim(),
        body: bodyInput.value,
        draft: draftInput.checked,
      },
    );
    if (res === null) {
      if (status !== null) {
        status.textContent = "Network error.";
        status.className = "forge-status err";
      }
      newSubmit.disabled = false;
      return;
    }
    if (res.error !== undefined && res.error !== "") {
      if (status !== null) {
        status.textContent = res.error;
        status.className = "forge-status err";
      }
      newSubmit.disabled = false;
      return;
    }
    dlg.close();
    await refreshPRs().catch(() => {
      /* noop */
    });
  });

  dlg.showModal();
  // Kick off the AI generation immediately so it overlaps with the
  // user picking up the form. Errors are non-fatal — they just leave
  // the title/body blank for the user to fill manually.
  void generate();
}

// --- Helpers ---

/** Explain why the Merge button is disabled, or "" if it should be
 *  enabled. The PR struct only exposes `mergeable` (bool) + `draft`
 *  (bool) — not the rich GitHub mergeStateStatus / GitLab merge_status
 *  values — so the reason is somewhat coarse. We pick the most
 *  actionable phrasing we can from those two flags so the user sees
 *  something concrete on hover instead of a silently-disabled button.
 *
 *  Possible returns:
 *    "" — enabled, no reason
 *    "PR is a draft. Mark it as ready for review first."
 *    "this PR isn't mergeable — likely conflicts, failing required
 *      checks, or branch protection. Check the PR on the forge for
 *      details."
 */
function computeMergeBlockReason(pr: PR): string {
  if (pr.draft === true) {
    return "PR is a draft. Mark it as ready for review first.";
  }
  if (pr.mergeable === false) {
    return "this PR isn't mergeable — likely conflicts, failing required checks, or branch protection. Open it on the forge for details.";
  }
  return "";
}
