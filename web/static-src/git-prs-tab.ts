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
import { kindTitle, FORGE_META } from "./forge-types.js";
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
    root.innerHTML = renderEmptyState({
      icon: ICON_PR_EMPTY,
      title: "No connected forges",
      hint: "Open the <strong>Sources</strong> tab to add a forge account.",
    });
    return;
  }

  const visible: RepoGroup[] = [];
  for (const g of lastGroups) {
    const matchesFilter = filterText === ""
      || g.full_name.toLowerCase().includes(filterText)
      || g.prs.some((pr) => pr.title.toLowerCase().includes(filterText));
    if (matchesFilter) visible.push(g);
  }

  // Aggregate: any open PRs at all? If not, show a centered empty
  // state instead of N collapsed sections.
  const totalOpen = visible.reduce((acc, g) => acc + g.prs.length, 0);
  if (totalOpen === 0 && filterText === "") {
    root.innerHTML = renderEmptyState({
      icon: ICON_PR_EMPTY,
      title: "All caught up",
      hint: "No open pull requests across your connected forges.",
    });
    return;
  }

  root.replaceChildren();
  if (visible.length === 0) {
    root.innerHTML = renderEmptyState({
      icon: ICON_FILTER,
      title: "No matching pull requests",
      hint: "Adjust your filter to see more.",
    });
    return;
  }

  for (const g of visible) root.appendChild(renderGroup(g));
}

// --- Empty-state markup helpers ---

const ICON_PR_EMPTY =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<circle cx="6" cy="6" r="3"/>' +
  '<circle cx="6" cy="18" r="3"/>' +
  '<line x1="6" y1="9" x2="6" y2="15"/>' +
  '<circle cx="18" cy="18" r="3"/>' +
  '<path d="M18 9a9 9 0 00-9-9"/>' +
  '</svg>';

const ICON_FILTER =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<polygon points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3"/>' +
  '</svg>';

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

  const section = document.createElement("section");
  section.className = "git-repo-section";
  section.dataset["repo"] = g.full_name;
  if (expandedDefault) section.classList.add("expanded");

  // Header is a flex container that hosts: chevron + forge icon +
  // name + count (left side, click-to-toggle), and a right-aligned
  // [+ New PR] button. The button's stopPropagation keeps the
  // toggle from firing when the user clicks New PR.
  const header = document.createElement("div");
  header.className = "git-repo-section-header git-repo-section-header-row";

  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "git-repo-section-header-toggle";
  toggle.setAttribute("aria-expanded", expandedDefault ? "true" : "false");
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

  const newBtn = document.createElement("button");
  newBtn.type = "button";
  newBtn.className = "btn-small btn-primary";
  newBtn.textContent = "+ New PR";
  newBtn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    void openNewPRDialog(g);
  });
  header.appendChild(newBtn);

  section.appendChild(header);

  const body = document.createElement("div");
  body.className = "git-repo-section-body";

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

/** Open the New PR dialog targeting a specific local repo by name.
 *  Used by the contextual "Open PR" hint on the Changes tab. If we
 *  haven't fetched groups yet, refetch first; the source branch is
 *  pre-filled from the call site so the user doesn't have to retype. */
export async function openNewPRForRepo(repoName: string, sourceBranch: string): Promise<void> {
  if (lastGroups.length === 0) await refreshPRs();
  const group = lastGroups.find((g) => g.name === repoName);
  if (group === undefined) {
    // Repo not in any forge group (probably not on a connected
    // forge). Render a small inline error in the mount.
    const root = document.getElementById("git-prs-mount");
    if (root !== null) {
      root.innerHTML = `<div class="git-multirepo-error">No connected forge knows about <strong>${escapeHTML(repoName)}</strong> — connect one in Sources.</div>`;
    }
    return;
  }
  await openNewPRDialog(group, sourceBranch);
}

async function openNewPRDialog(g: RepoGroup, sourceBranch = ""): Promise<void> {
  const dlg = document.getElementById("pr-create-dialog") as HTMLDialogElement | null;
  if (dlg === null) {
    // Fallback (shouldn't happen — the dialog is in index.html).
    alert("PR dialog not available");
    return;
  }

  const baseInput = document.getElementById("pr-base") as HTMLInputElement;
  const headInput = document.getElementById("pr-head") as HTMLInputElement;
  const titleInput = document.getElementById("pr-title") as HTMLInputElement;
  const bodyInput = document.getElementById("pr-body") as HTMLTextAreaElement;
  const draftInput = document.getElementById("pr-draft") as HTMLInputElement;
  const status = document.getElementById("pr-dialog-status");
  const submitBtn = document.getElementById("pr-submit-btn") as HTMLButtonElement;
  const generateBtn = document.getElementById("pr-generate-btn") as HTMLButtonElement;

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
  const newGenerate = generateBtn.cloneNode(true) as HTMLButtonElement;
  generateBtn.replaceWith(newGenerate);
  // The static close buttons (data-pr-close) keep their close handler
  // wired in index.html-side via this closure too.
  for (const btn of dlg.querySelectorAll<HTMLButtonElement>("[data-pr-close]")) {
    const fresh = btn.cloneNode(true) as HTMLButtonElement;
    btn.replaceWith(fresh);
    fresh.addEventListener("click", () => dlg.close());
  }

  const generate = async (): Promise<void> => {
    if (status !== null) status.textContent = "Generating description…";
    const res = await apiPost<{ title?: string; body?: string; error?: string }>(
      `/api/git/pr-description`,
      { repo: g.name, branch: baseInput.value.trim() || "main" },
    );
    if (res === null) {
      if (status !== null) { status.textContent = "Network error."; status.className = "forge-status err"; }
      return;
    }
    if (res.error !== undefined && res.error !== "") {
      if (status !== null) { status.textContent = res.error; status.className = "forge-status err"; }
      return;
    }
    if (res.title !== undefined && titleInput.value === "") titleInput.value = res.title;
    if (res.body !== undefined && bodyInput.value === "") bodyInput.value = res.body;
    if (status !== null) { status.textContent = "Description generated. Edit and submit."; status.className = "forge-status ok"; }
  };

  newGenerate.addEventListener("click", () => { void generate(); });

  // Stage 2: review + submit.
  newSubmit.addEventListener("click", async () => {
    if (status !== null) { status.textContent = "Opening PR…"; status.className = "forge-status"; }
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
      if (status !== null) { status.textContent = "Network error."; status.className = "forge-status err"; }
      return;
    }
    if (res.error !== undefined && res.error !== "") {
      if (status !== null) { status.textContent = res.error; status.className = "forge-status err"; }
      return;
    }
    dlg.close();
    await refreshPRs();
  });

  dlg.showModal();
  // Kick off the AI generation immediately so it overlaps with the
  // user picking up the form. Errors are non-fatal — they just leave
  // the title/body blank for the user to fill manually.
  void generate();
}

// --- Helpers ---

function escapeHTML(s: string): string {
  const map: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return s.replace(/[&<>"']/g, (c) => map[c] ?? c);
}
