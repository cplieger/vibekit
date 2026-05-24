// ---------------------------------------------------------------------------
// Git Sources tab: per-forge sections combining forge-auth (accounts
// management) with that forge's cloneable repositories. Mounted into
// #git-sources-mount.
//
// Layout: each connected forge kind gets a section. The section
// renders a forge-auth account block (existing renderForgesPanel
// logic, scoped to one kind here) followed by a list of repos
// available under that account, each with Clone / Trash / Open ↗
// actions.
//
// This replaces both the old Settings → Git & forges page and the
// old single-select repo picker dialog.
// ---------------------------------------------------------------------------

import { apiGet, apiPost } from "./api-client.js";
import { onSSE } from "./bus.js";
import { renderForgesPanel } from "./forge-auth.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import { ICON_GLOBE, ICON_TRASH, ICON_EXTERNAL } from "./icons.js";
import { kindTitle, FORGE_META } from "./forge-types.js";
import { preserveGitScroll } from "./git-scroll.js";
import type { ForgeKind } from "./forge-types.js";
import type { ConfiguredForge, Repo } from "./wire/types.gen.js";

interface ForgesListResponse {
  forges: ConfiguredForge[];
  kinds: ForgeKind[];
  oauth?: Partial<Record<ForgeKind, boolean>>;
}
interface RepoListResponse { repos: Repo[] }
interface LocalReposResponse { repos: string[] }

const ALL_KINDS: readonly ForgeKind[] = ["github", "gitlab", "codeberg", "gitea"];

let lastForges: ConfiguredForge[] = [];
let lastReposByForge: Record<string, Repo[]> = {};
let lastLocalNames: Set<string> = new Set();

export function initSourcesTab(): void {
  // Refetch when forges change (login / logout / clone).
  onSSE("forges_changed", () => { void refreshSources(); });
  void refreshSources();
}

export async function refreshSources(): Promise<void> {
  const [forgesRes, localRes] = await Promise.all([
    apiGet<ForgesListResponse>("/api/forges"),
    apiGet<LocalReposResponse>("/api/git/repos"),
  ]);
  lastForges = forgesRes?.forges ?? [];
  lastLocalNames = new Set((localRes?.repos ?? []).filter((n) => n !== "."));

  // Per-forge repo lists (parallel).
  const reposByForge: Record<string, Repo[]> = {};
  await Promise.all(
    lastForges.filter((f) => f.connected).map(async (f) => {
      const r = await apiGet<RepoListResponse>(
        `/api/forges/${encodeURIComponent(f.id)}/repos`,
      );
      reposByForge[f.id] = r?.repos ?? [];
    }),
  );
  lastReposByForge = reposByForge;
  paint();
}

// --- Render ---

function paint(): void {
  preserveGitScroll(paintInner);
}

function paintInner(): void {
  const root = document.getElementById("git-sources-mount");
  if (root === null) return;

  // Bucket forges by kind.
  const byKind = new Map<ForgeKind, ConfiguredForge[]>();
  for (const k of ALL_KINDS) byKind.set(k, []);
  for (const f of lastForges) {
    const list = byKind.get(f.kind);
    if (list !== undefined) list.push(f);
  }

  root.replaceChildren();

  // Mount the existing forge-auth accounts panel as the top of the
  // tab. We let it own the per-kind account UI (Add account, Add a
  // PAT, sign-out, manage link, etc.) so we don't duplicate that
  // logic. Below it we render our per-kind repo lists.
  const accountsHost = document.createElement("div");
  accountsHost.id = "forges-panel"; // forge-auth.ts targets this id
  root.appendChild(accountsHost);
  void renderForgesPanel();

  // Per-kind repo list section. We render it below the account UI
  // for the kinds where there's at least one connected forge — empty
  // forges have nothing to list.
  for (const kind of ALL_KINDS) {
    const forges = (byKind.get(kind) ?? []).filter((f) => f.connected);
    if (forges.length === 0) continue;
    root.appendChild(renderRepoListForKind(kind, forges));
  }
}

function renderRepoListForKind(kind: ForgeKind, forges: ConfiguredForge[]): HTMLElement {
  const section = document.createElement("section");
  section.className = "git-sources-repos";
  section.dataset["kind"] = kind;

  // Heading row: title on the left + "Clone all" affordance on the
  // right. Clone all is disabled when there are no uncloned repos
  // in this section.
  const headingRow = document.createElement("div");
  headingRow.className = "git-sources-repos-heading-row";
  const heading = document.createElement("h3");
  heading.className = "git-sources-repos-heading";
  heading.textContent = `${kindTitle(kind)} repositories`;
  headingRow.appendChild(heading);

  const allRepos = forges.flatMap((f) =>
    (lastReposByForge[f.id] ?? []).map((r) => ({ forge: f, repo: r })),
  );
  const cloneable = allRepos.filter(
    ({ repo }) =>
      !lastLocalNames.has(repo.name) &&
      typeof repo.clone_url === "string" &&
      repo.clone_url !== "",
  );

  const cloneAllBtn = document.createElement("button");
  cloneAllBtn.type = "button";
  cloneAllBtn.className = "btn-small";
  cloneAllBtn.textContent = "Clone all";
  cloneAllBtn.disabled = cloneable.length === 0;
  cloneAllBtn.title = cloneable.length === 0
    ? "No remote-only repos to clone"
    : `Clone ${cloneable.length} repo${cloneable.length === 1 ? "" : "s"}`;
  cloneAllBtn.addEventListener("click", () => {
    void withAsyncFeedback(cloneAllBtn, () => cloneAllForKind(kind, cloneable, cloneAllBtn));
  });
  headingRow.appendChild(cloneAllBtn);
  section.appendChild(headingRow);

  const list = document.createElement("ul");
  list.className = "git-sources-repo-list";

  if (allRepos.length === 0) {
    const empty = document.createElement("li");
    empty.className = "git-sources-repo-empty";
    empty.textContent = "No repositories accessible to the connected accounts.";
    list.appendChild(empty);
  } else {
    // Sort: cloned first, then by recency / alpha.
    allRepos.sort((a, b) => {
      const aCloned = lastLocalNames.has(a.repo.name);
      const bCloned = lastLocalNames.has(b.repo.name);
      if (aCloned !== bCloned) return aCloned ? -1 : 1;
      return a.repo.full_name.localeCompare(b.repo.full_name);
    });
    for (const { forge, repo } of allRepos) list.appendChild(renderRepoRow(forge, repo));
  }

  section.appendChild(list);
  return section;
}

function renderRepoRow(forge: ConfiguredForge, repo: Repo): HTMLElement {
  const li = document.createElement("li");
  li.className = "git-sources-repo-row";
  li.dataset["repoId"] = `${forge.host}:${repo.full_name}`;
  const cloned = lastLocalNames.has(repo.name);
  if (cloned) li.classList.add("cloned");

  // State glyph (left)
  const state = document.createElement("span");
  state.className = "git-sources-repo-state";
  if (cloned) {
    state.innerHTML = `<span class="repo-picker-state-synced" aria-label="Cloned" title="Cloned and tracked">●</span>`;
  } else {
    state.innerHTML = ICON_GLOBE;
    state.title = "Remote, not cloned";
    state.setAttribute("aria-label", "Remote, not cloned");
  }
  li.appendChild(state);

  // Identity
  const id = document.createElement("div");
  id.className = "git-sources-repo-identity";
  const primary = document.createElement("span");
  primary.className = "git-sources-repo-name";
  primary.textContent = repo.full_name;
  id.appendChild(primary);
  const meta = document.createElement("span");
  meta.className = "git-sources-repo-meta";
  const metaParts: string[] = [];
  if (repo.private === true) metaParts.push("private");
  if (repo.archived === true) metaParts.push("archived");
  if (repo.fork === true) metaParts.push("fork");
  if (repo.default_branch !== undefined && repo.default_branch !== "") {
    metaParts.push(repo.default_branch);
  }
  meta.textContent = metaParts.join(" · ");
  id.appendChild(meta);
  li.appendChild(id);

  // Actions
  const actions = document.createElement("div");
  actions.className = "git-sources-repo-actions";

  if (repo.url !== undefined && repo.url !== "") {
    const open = document.createElement("a");
    open.href = repo.url;
    open.target = "_blank";
    open.rel = "noreferrer";
    open.className = "icon-btn";
    open.innerHTML = ICON_EXTERNAL;
    open.title = "Open on forge";
    open.setAttribute("aria-label", "Open on forge");
    actions.appendChild(open);
  }

  if (cloned) {
    const trash = document.createElement("button");
    trash.type = "button";
    trash.className = "icon-btn danger";
    trash.innerHTML = ICON_TRASH;
    trash.title = "Remove local copy";
    trash.setAttribute("aria-label", "Remove local copy");
    trash.addEventListener("click", () => {
      // Default keepLabel=false so the spinner replaces the trash
      // icon while in flight (then ✓/✗ on completion). For an
      // icon-only button there's no label to keep.
      void withAsyncFeedback(trash, () => removeLocal(repo, !!repo.url));
    });
    actions.appendChild(trash);
  } else if (repo.clone_url !== undefined && repo.clone_url !== "") {
    const clone = document.createElement("button");
    clone.type = "button";
    clone.className = "btn-small btn-primary";
    clone.textContent = "Clone";
    clone.addEventListener("click", () => {
      void withAsyncFeedback(clone, () => cloneRepo(repo.clone_url ?? ""));
    });
    actions.appendChild(clone);
  }

  li.appendChild(actions);
  return li;
}

// --- API helpers ---

async function cloneRepo(url: string): Promise<void> {
  if (url === "") throw new Error("no clone URL");
  const res = await apiPost<{ output?: string; error?: string }>(
    `/api/git/clone`,
    { url },
  );
  // Trust the post-clone state: refetch and check is_local.
  await refreshSources();
  if (res === null) throw new Error("network error");
  if (res.error !== undefined && res.error !== "") throw new Error(res.error);
}

/** Clone every uncloned remote-only repo under this forge kind,
 *  sequentially. Visible feedback is on the Clone all button itself
 *  (disabled + count label). Per-repo failures don't abort the
 *  batch — partial success beats none. After the loop, a single
 *  refresh redraws the section in its new cloned state, so failed
 *  rows still show as remote-only with their Clone button. */
async function cloneAllForKind(
  kind: ForgeKind,
  candidates: Array<{ forge: ConfiguredForge; repo: Repo }>,
  btn: HTMLButtonElement,
): Promise<void> {
  if (candidates.length === 0) return;
  void kind; // Reserved for future per-kind hooks (e.g. logging).
  const originalLabel = btn.textContent ?? "Clone all";
  let done = 0;
  let failed = 0;
  for (const { repo } of candidates) {
    btn.textContent = `Cloning ${done + 1}/${candidates.length}…`;
    try {
      const url = repo.clone_url ?? "";
      if (url === "") {
        failed++;
        continue;
      }
      const res = await apiPost<{ output?: string; error?: string }>(
        `/api/git/clone`,
        { url },
      );
      if (res === null || (res.error !== undefined && res.error !== "")) {
        failed++;
      }
    } catch {
      failed++;
    }
    done++;
  }
  btn.textContent = originalLabel;
  await refreshSources();
  if (failed > 0) {
    btn.title = `Cloned ${candidates.length - failed}, ${failed} failed`;
  }
}

async function removeLocal(repo: Repo, hasRemote: boolean): Promise<void> {
  const remoteHint = hasRemote
    ? "The remote copy stays intact; you can re-clone it later."
    : "This is a local-only repository — deletion is permanent.";
  const ok = await confirmDialog(
    `Delete the local copy of ${repo.name}? ${remoteHint}`,
    "Delete",
    "destructive",
  );
  if (!ok) return;
  const res = await apiPost<{ status?: string; error?: string }>(
    `/api/git/remove`,
    { repo: repo.name },
  );
  if (res === null) throw new Error("network error");
  if (res.error !== undefined && res.error !== "") throw new Error(res.error);
  await refreshSources();
}

// FORGE_META is referenced via forge-auth.ts internals — keep the import
// live so a future per-section forge icon next to the heading has zero
// import churn.
void FORGE_META;
