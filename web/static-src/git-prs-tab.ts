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
import { relativeTime } from "./files-shared.js";
import { kindTitle } from "./forge-types.js";
import type { ForgeKind } from "./forge-types.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import type { ConfiguredForge, Repo } from "./wire/types.gen.js";

// --- Types ---

interface PR {
  number: number;
  title: string;
  state: string;
  draft?: boolean;
  mergeable?: boolean;
  source_branch: string;
  target_branch: string;
  url?: string;
  author?: string;
  created_at?: number;
  updated_at?: number;
}

interface RepoGroup {
  forge_id: string;
  forge_kind: ForgeKind;
  forge_host: string;
  owner: string;
  name: string;
  full_name: string;
  prs: PR[];
  error?: string;
}

interface ForgesListResponse { forges: ConfiguredForge[] }
interface RepoListResponse { repos: Repo[] }
interface PRListResponse { prs: PR[] }

// --- State ---

let lastGroups: RepoGroup[] = [];
let filterText = "";

// --- Public API ---

export function initPRsTab(): void {
  const filterEl = document.getElementById("git-prs-filter") as HTMLInputElement | null;
  filterEl?.addEventListener("input", () => {
    filterText = filterEl.value.trim().toLowerCase();
    paint();
  });

  // Refetch on forge credential changes; PRs list depends on which
  // forges are connected.
  onSSE("forges_changed", () => { void refreshPRs(); });
}

/** Force a full PR refresh (parallel fan-out across all credentialled
 *  repos). Safe to call multiple times — only the latest result wins. */
export async function refreshPRs(): Promise<void> {
  const forgesRes = await apiGet<ForgesListResponse>("/api/forges");
  const forges = (forgesRes?.forges ?? []).filter((f) => f.connected);

  // Build a flat (forge, owner/name) list to fetch.
  const tasks: Array<{ forge: ConfiguredForge; repo: Repo }> = [];
  for (const forge of forges) {
    const reposRes = await apiGet<RepoListResponse>(
      `/api/forges/${encodeURIComponent(forge.id)}/repos`,
    );
    if (reposRes === null) continue;
    for (const repo of reposRes.repos) {
      tasks.push({ forge, repo });
    }
  }

  const groups: RepoGroup[] = await Promise.all(
    tasks.map(async ({ forge, repo }) => {
      try {
        const res = await apiGet<PRListResponse>(
          `/api/forges/${encodeURIComponent(forge.id)}/repos/${encodeURIComponent(repo.owner)}/${encodeURIComponent(repo.name)}/prs?state=open`,
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

  lastGroups = groups;
  paint();
}

// --- Render ---

function paint(): void {
  const root = document.getElementById("git-prs-mount");
  if (root === null) return;

  if (lastGroups.length === 0) {
    root.innerHTML = `<div class="git-multirepo-empty">No repositories on connected forges. Open the <strong>Sources</strong> tab to add a forge.</div>`;
    return;
  }

  const visible: RepoGroup[] = [];
  for (const g of lastGroups) {
    const matchesFilter = filterText === ""
      || g.full_name.toLowerCase().includes(filterText)
      || g.prs.some((pr) => pr.title.toLowerCase().includes(filterText));
    if (matchesFilter) visible.push(g);
  }

  root.replaceChildren();
  if (visible.length === 0) {
    const empty = document.createElement("div");
    empty.className = "git-multirepo-empty";
    empty.textContent = "No matching pull requests.";
    root.appendChild(empty);
    return;
  }

  for (const g of visible) root.appendChild(renderGroup(g));
}

function renderGroup(g: RepoGroup): HTMLElement {
  const expandedDefault = g.prs.length > 0 || filterText !== "";

  const section = document.createElement("section");
  section.className = "git-repo-section";
  section.dataset["repo"] = g.full_name;
  if (expandedDefault) section.classList.add("expanded");

  const header = document.createElement("button");
  header.type = "button";
  header.className = "git-repo-section-header";
  header.setAttribute("aria-expanded", expandedDefault ? "true" : "false");
  const count = g.prs.length;
  const countText = count === 0 ? "no open PRs" : `${count} open`;
  header.innerHTML = `
    <span class="git-repo-section-chevron" aria-hidden="true">▸</span>
    <span class="git-repo-section-name">${escapeHTML(g.full_name)}</span>
    <span class="git-repo-section-meta">${escapeHTML(g.forge_host)} · ${escapeHTML(countText)}</span>
  `;
  header.addEventListener("click", () => {
    const open = section.classList.toggle("expanded");
    header.setAttribute("aria-expanded", open ? "true" : "false");
  });
  section.appendChild(header);

  const body = document.createElement("div");
  body.className = "git-repo-section-body";

  // Action bar: + New PR button
  const bar = document.createElement("div");
  bar.className = "git-repo-action-bar";
  const newBtn = document.createElement("button");
  newBtn.type = "button";
  newBtn.className = "btn-small btn-primary";
  newBtn.textContent = "+ New pull request";
  newBtn.addEventListener("click", () => {
    void openNewPRDialog(g);
  });
  bar.appendChild(newBtn);
  body.appendChild(bar);

  if (g.error !== undefined && g.error !== "") {
    const err = document.createElement("div");
    err.className = "git-repo-row-error";
    err.textContent = `Failed to load PRs: ${g.error}`;
    body.appendChild(err);
  } else if (g.prs.length === 0) {
    const empty = document.createElement("div");
    empty.className = "git-repo-row-empty";
    empty.textContent = `No open pull requests on ${kindTitle(g.forge_kind)}.`;
    body.appendChild(empty);
  } else {
    const list = document.createElement("ul");
    list.className = "git-pr-list";
    const filtered = filterText === ""
      ? g.prs
      : g.prs.filter((pr) => pr.title.toLowerCase().includes(filterText));
    for (const pr of filtered) list.appendChild(renderPRRow(g, pr));
    body.appendChild(list);
  }

  section.appendChild(body);
  return section;
}

function renderPRRow(g: RepoGroup, pr: PR): HTMLElement {
  const li = document.createElement("li");
  li.className = "git-pr-row";

  const meta = document.createElement("div");
  meta.className = "git-pr-row-meta";

  const num = document.createElement("a");
  num.className = "git-pr-row-number";
  if (pr.url !== undefined && pr.url !== "") {
    num.href = pr.url;
    num.target = "_blank";
    num.rel = "noreferrer";
  }
  num.textContent = `#${pr.number}`;
  meta.appendChild(num);

  const title = document.createElement("span");
  title.className = "git-pr-row-title";
  title.textContent = pr.title;
  title.title = pr.title;
  meta.appendChild(title);

  if (pr.draft === true) {
    const draft = document.createElement("span");
    draft.className = "git-pr-row-tag";
    draft.textContent = "draft";
    meta.appendChild(draft);
  }

  li.appendChild(meta);

  const sub = document.createElement("div");
  sub.className = "git-pr-row-sub";
  const parts: string[] = [];
  if (pr.author !== undefined && pr.author !== "") parts.push(`by @${pr.author}`);
  if (pr.updated_at !== undefined && pr.updated_at > 0) parts.push(relativeTime(pr.updated_at));
  if (pr.source_branch !== "" && pr.target_branch !== "") {
    parts.push(`${pr.source_branch} → ${pr.target_branch}`);
  }
  sub.textContent = parts.join(" · ");
  li.appendChild(sub);

  // Actions
  const actions = document.createElement("div");
  actions.className = "git-pr-row-actions";

  if (pr.url !== undefined && pr.url !== "") {
    const open = document.createElement("a");
    open.href = pr.url;
    open.target = "_blank";
    open.rel = "noreferrer";
    open.className = "btn-small";
    open.textContent = "Open ↗";
    actions.appendChild(open);
  }

  const merge = document.createElement("button");
  merge.type = "button";
  merge.className = "btn-small btn-primary";
  merge.textContent = "Merge";
  merge.disabled = pr.mergeable !== true;
  merge.addEventListener("click", () => {
    void withAsyncFeedback(merge, async () => {
      const ok = await confirmDialog(`Merge PR #${pr.number} (${pr.title})?`, "Merge", "normal");
      if (!ok) return;
      const res = await apiPost<{ status?: string; error?: string }>(
        `/api/forges/${encodeURIComponent(g.forge_id)}/repos/${encodeURIComponent(g.owner)}/${encodeURIComponent(g.name)}/prs/${pr.number}/merge`,
        {},
      );
      if (res === null || (res.error !== undefined && res.error !== "")) {
        throw new Error(res?.error ?? "merge failed");
      }
      await refreshPRs();
    });
  });
  actions.appendChild(merge);

  const close = document.createElement("button");
  close.type = "button";
  close.className = "btn-small btn-danger";
  close.textContent = "Close";
  close.addEventListener("click", () => {
    void withAsyncFeedback(close, async () => {
      const ok = await confirmDialog(`Close PR #${pr.number} without merging?`, "Close PR", "destructive");
      if (!ok) return;
      const res = await apiPost<{ status?: string; error?: string }>(
        `/api/forges/${encodeURIComponent(g.forge_id)}/repos/${encodeURIComponent(g.owner)}/${encodeURIComponent(g.name)}/prs/${pr.number}/close`,
        {},
      );
      if (res === null || (res.error !== undefined && res.error !== "")) {
        throw new Error(res?.error ?? "close failed");
      }
      await refreshPRs();
    });
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

async function openNewPRDialog(g: RepoGroup): Promise<void> {
  const dlg = document.createElement("dialog");
  dlg.className = "vk-confirm-dialog git-pr-create-dialog";
  dlg.innerHTML = `
    <h3 style="margin:0 0 var(--sp-3); font-size: var(--fs-md);">New pull request — ${escapeHTML(g.full_name)}</h3>
    <form>
      <div style="display: grid; gap: var(--sp-2);">
        <label>Source branch <input name="source" class="tool-form-input" required placeholder="feature-branch"></label>
        <label>Target branch <input name="target" class="tool-form-input" required value="main" placeholder="main"></label>
        <label>Title <input name="title" class="tool-form-input" required></label>
        <label>Body <textarea name="body" class="tool-form-input" rows="4"></textarea></label>
      </div>
      <div class="vk-confirm-actions" style="margin-block-start: var(--sp-3);">
        <button type="button" class="btn-small" data-cancel>Cancel</button>
        <button type="submit" class="btn-small btn-primary">Open PR</button>
      </div>
    </form>
  `;
  document.body.appendChild(dlg);
  dlg.showModal();

  const form = dlg.querySelector("form")!;
  const cancel = dlg.querySelector("[data-cancel]") as HTMLButtonElement;
  cancel.addEventListener("click", () => { dlg.close(); dlg.remove(); });

  await new Promise<void>((resolve) => {
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const data = new FormData(form);
      const body = {
        source_branch: String(data.get("source") ?? ""),
        target_branch: String(data.get("target") ?? "main"),
        title: String(data.get("title") ?? ""),
        body: String(data.get("body") ?? ""),
      };
      const res = await apiPost<{ number?: number; error?: string }>(
        `/api/forges/${encodeURIComponent(g.forge_id)}/repos/${encodeURIComponent(g.owner)}/${encodeURIComponent(g.name)}/prs`,
        body,
      );
      if (res === null || (res.error !== undefined && res.error !== "")) {
        alert(`Failed: ${res?.error ?? "network error"}`);
        return;
      }
      dlg.close();
      dlg.remove();
      await refreshPRs();
      resolve();
    });
    dlg.addEventListener("close", () => { dlg.remove(); resolve(); });
  });
}

// --- Helpers ---

function escapeHTML(s: string): string {
  const map: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return s.replace(/[&<>"']/g, (c) => map[c] ?? c);
}
