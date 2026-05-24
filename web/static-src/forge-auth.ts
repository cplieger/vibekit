// ---------------------------------------------------------------------------
// Forge authentication UI.
//
// Layout: four always-visible sections (one per supported forge kind:
// GitHub, GitLab, Codeberg, Gitea). Each section shows:
//
//   - Section header: kind name.
//   - Account list: zero or more slim rows. Each row has the email or
//     username, the host, a "Manage" link to the forge's account page,
//     and a "Sign out" button.
//   - Action buttons:
//       [+ Add account]   triggers OAuth (GitHub) or PAT inline form
//                         (GitLab/Gitea/Codeberg).
//       [+ Add a PAT]     extra button on the GitHub section: lets the
//                         user paste a PAT instead of going through the
//                         OAuth device flow. The backend `LoginWithPAT`
//                         is kind-agnostic, so this works without
//                         server changes.
//
// Multi-account note: the data model and UI render a list of N
// accounts per kind, but the underlying CLIs (gh, glab) store one
// user per host in their config files. Today, adding a second account
// on the same host replaces the first via the CLI. Across-different-
// hosts works (e.g. github.com + ghe.example.com).
// ---------------------------------------------------------------------------

import { apiGet, apiPost } from "./api-client.js";
import { confirm as confirmDialog } from "./confirm.js";
import { ICON_DOWNLOAD, ICON_EXTERNAL, ICON_GLOBE, ICON_PLUS_16, ICON_REPO, ICON_TRASH } from "./icons.js";
import { withAsyncFeedback } from "./async-button.js";
import { error as toastError } from "./toast.js";
import type { ConfiguredForge, DeviceFlowResponse, ForgeKind, PollResult, Repo } from "./wire/types.gen.js";
import { HOST_LOCKED_KINDS, DEFAULT_HOST, forgeKindLabel } from "./forge-types.js";
import {
  startDeviceFlow,
  signOut,
  cloneRepo as cloneRepoAction,
  deleteLocal as deleteLocalAction,
  connectPAT,
} from "./actions/forge.js";
import { bindLoadingState } from "./actions/index.js";

interface ForgesListResponse {
  forges: ConfiguredForge[];
  kinds: ForgeKind[];
  oauth?: Partial<Record<ForgeKind, boolean>>;
}

interface RepoListResponse { repos: Repo[] }
interface LocalReposResponse { repos: string[] }

// --- Module state -----------------------------------------------------

/** Cached repo list keyed by forge ID. Populated on every render so
 *  each account's collapsible footer can show "X repos, Y cloned" and
 *  list the repos accessible to that forge. */
let lastReposByForge: Record<string, Repo[]> = {};

/** Names of locally-cloned repos. Used to compute the cloned-count
 *  per-account and to drive the green dot / Trash button per row. */
let lastLocalNames: Set<string> = new Set();

/** Forge IDs whose collapsible footer should render expanded on the
 *  next paint. Populated when the user successfully adds an account
 *  (PAT submit or OAuth complete) so the user lands on the freshly-
 *  added account with its repos visible. Cleared after one paint. */
const expandOnNextPaint: Set<string> = new Set();

/** OAuth availability per kind, populated from the forges list response. */
let oauthByKind: Partial<Record<ForgeKind, boolean>> = {};

const ALL_KINDS: readonly ForgeKind[] = ["github", "gitlab", "codeberg", "gitea"];

/** Brand SVG glyphs from Simple Icons (CC0). 24x24 viewBox; CSS sizes
 *  them down to fit the 22-px kind badge. */
const KIND_ICONS: Record<ForgeKind, string> = {
  github:
    `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12"/></svg>`,
  gitlab:
    `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m23.6004 9.5927-.0337-.0862L20.3.9814a.851.851 0 0 0-.3362-.405.8748.8748 0 0 0-.9997.0539.8748.8748 0 0 0-.29.4399l-2.2055 6.748H7.5375l-2.2057-6.748a.8573.8573 0 0 0-.29-.4412.8748.8748 0 0 0-.9997-.0537.8585.8585 0 0 0-.3362.4049L.4332 9.5015l-.0325.0862a6.0657 6.0657 0 0 0 2.0119 7.0105l.0113.0087.03.0213 4.976 3.7264 2.462 1.8633 1.4995 1.1321a1.0085 1.0085 0 0 0 1.2197 0l1.4995-1.1321 2.4619-1.8633 5.006-3.7489.0125-.01a6.0682 6.0682 0 0 0 2.0094-7.003z"/></svg>`,
  codeberg:
    `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M11.955.49A12 12 0 0 0 0 12.49a12 12 0 0 0 1.832 6.373L11.838 5.928a.187.14 0 0 1 .324 0l10.006 12.935A12 12 0 0 0 24 12.49a12 12 0 0 0-12-12 12 12 0 0 0-.045 0zm.375 6.467l4.416 16.553a12 12 0 0 0 5.137-4.213z"/></svg>`,
  gitea:
    `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4.209 4.603c-.247 0-.525.02-.84.088-.333.07-1.28.283-2.054 1.027C-.403 7.25.035 9.685.089 10.052c.065.446.263 1.687 1.21 2.768 1.749 2.141 5.513 2.092 5.513 2.092s.462 1.103 1.168 2.119c.955 1.263 1.936 2.248 2.89 2.367 2.406 0 7.212-.004 7.212-.004s.458.004 1.08-.394c.535-.324 1.013-.893 1.013-.893s.492-.527 1.18-1.73c.21-.37.385-.729.538-1.068 0 0 2.107-4.471 2.107-8.823-.042-1.318-.367-1.55-.443-1.627-.156-.156-.366-.153-.366-.153s-4.475.252-6.792.306c-.508.011-1.012.023-1.512.027v4.474l-.634-.301c0-1.39-.004-4.17-.004-4.17-1.107.016-3.405-.084-3.405-.084s-5.399-.27-5.987-.324c-.187-.011-.401-.032-.648-.032zm.354 1.832h.111s.271 2.269.6 3.597C5.549 11.147 6.22 13 6.22 13s-.996-.119-1.641-.348c-.99-.324-1.409-.714-1.409-.714s-.73-.511-1.096-1.52C1.444 8.73 2.021 7.7 2.021 7.7s.32-.859 1.47-1.145c.395-.106.863-.12 1.072-.12zm8.33 2.554c.26.003.509.127.509.127l.868.422-.529 1.075a.686.686 0 0 0-.614.359.685.685 0 0 0 .072.756l-.939 1.924a.69.69 0 0 0-.66.527.687.687 0 0 0 .347.763.686.686 0 0 0 .867-.206.688.688 0 0 0-.069-.882l.916-1.874a.667.667 0 0 0 .237-.02.657.657 0 0 0 .271-.137 8.826 8.826 0 0 1 1.016.512.761.761 0 0 1 .286.282c.073.21-.073.569-.073.569-.087.29-.702 1.55-.702 1.55a.692.692 0 0 0-.676.477.681.681 0 1 0 1.157-.252c.073-.141.141-.282.214-.431.19-.397.515-1.16.515-1.16.035-.066.218-.394.103-.814-.095-.435-.48-.638-.48-.638-.467-.301-1.116-.58-1.116-.58s0-.156-.042-.27a.688.688 0 0 0-.148-.241l.516-1.062 2.89 1.401s.48.218.583.619c.073.282-.019.534-.069.657-.24.587-2.1 4.317-2.1 4.317s-.232.554-.748.588a1.065 1.065 0 0 1-.393-.045l-.202-.08-4.31-2.1s-.417-.218-.49-.596c-.083-.31.104-.691.104-.691l2.073-4.272s.183-.37.466-.497a.855.855 0 0 1 .35-.077z"/></svg>`,
};

const PAT_HELP_LINKS: Record<ForgeKind, { url: string; label: string } | null> = {
  github:   { url: "https://github.com/settings/tokens?type=beta", label: "Create a GitHub fine-grained token" },
  gitlab:   { url: "https://gitlab.com/-/profile/personal_access_tokens?scopes=api,read_repository,write_repository", label: "Create a GitLab token" },
  codeberg: { url: "https://codeberg.org/user/settings/applications", label: "Create a Codeberg token" },
  gitea:    null,
};

/** Manage-account URL on the forge itself, parameterized by host. */
function manageAccountURL(kind: ForgeKind, host: string): string {
  switch (kind) {
    case "github":   return `https://${host}/settings/profile`;
    case "gitlab":   return `https://${host}/-/profile`;
    case "codeberg": return `https://${host}/user/settings`;
    case "gitea":    return host === "" ? "" : `https://${host}/user/settings`;
  }
}

/** Placeholder shown in the host input on the PAT form. For kinds with
 *  a sensible default (e.g. gitlab.com) we echo the default; for kinds
 *  with no canonical host (gitea/forgejo) we show an unambiguous
 *  example so users can't miss that the field needs filling. */
function hostPlaceholder(kind: ForgeKind): string {
  switch (kind) {
    case "github":   return "github.com";
    case "gitlab":   return "gitlab.com";
    case "codeberg": return "codeberg.org";
    case "gitea":    return "your-host.example.com";
  }
}

/** Render the full forges panel. Idempotent; call after every list
 *  mutation to refresh.
 *
 *  When `revalidate` is true (the default), connected accounts are
 *  re-probed in parallel after the initial paint. Tokens can be
 *  silently revoked or expire; this catches that on page open. The
 *  initial paint shows last-known state immediately; the panel
 *  re-renders once when all probes have settled. */
export async function renderForgesPanel(opts: { revalidate?: boolean } = {}): Promise<void> {
  const root = document.getElementById("forges-panel");
  if (root === null) return;

  const data = await apiGet<ForgesListResponse>("/api/forges");
  if (data === null) {
    root.innerHTML = `<div class="forge-error">Failed to load forges.</div>`;
    return;
  }

  // Refresh repo + local-clone caches in parallel with the forges
  // list. The per-account collapsible footer renders from these.
  await Promise.all([
    refreshLocalNames(),
    refreshReposByForge(data.forges),
  ]);

  paintForgesData(root, data);

  if (opts.revalidate !== false) {
    const ids = data.forges.filter((f) => f.connected).map((f) => f.id);
    if (ids.length > 0) {
      void revalidateInBackground(root, ids);
    }
  }
}

async function refreshLocalNames(): Promise<void> {
  const r = await apiGet<LocalReposResponse>("/api/git/repos");
  lastLocalNames = new Set((r?.repos ?? []).filter((n) => n !== "."));
}

async function refreshReposByForge(forges: ConfiguredForge[]): Promise<void> {
  const map: Record<string, Repo[]> = {};
  await Promise.all(
    forges.filter((f) => f.connected).map(async (f) => {
      const r = await apiGet<RepoListResponse>(
        `/api/forges/${encodeURIComponent(f.id)}/repos`,
      );
      map[f.id] = r?.repos ?? [];
    }),
  );
  lastReposByForge = map;
}

/** Re-probe every connected account in parallel; on completion, re-fetch
 *  /api/forges and re-paint with the post-probe state. */
async function revalidateInBackground(root: HTMLElement, ids: string[]): Promise<void> {
  await Promise.allSettled(
    ids.map((id) => apiPost(`/api/forges/${encodeURIComponent(id)}/probe`, {})),
  );
  const data = await apiGet<ForgesListResponse>("/api/forges");
  if (data != null && document.body.contains(root)) {
    await Promise.all([
      refreshLocalNames(),
      refreshReposByForge(data.forges),
    ]);
    paintForgesData(root, data);
  }
}

/** Paint the data into the root element. Pure rendering — no API calls. */
function paintForgesData(root: HTMLElement, data: ForgesListResponse): void {
  // Cache OAuth availability for use in add-account pane.
  oauthByKind = data.oauth ?? {};

  // Bucket accounts by kind.
  const byKind = new Map<ForgeKind, ConfiguredForge[]>();
  for (const k of ALL_KINDS) byKind.set(k, []);
  for (const f of data.forges) {
    const list = byKind.get(f.kind);
    if (list !== undefined) list.push(f);
  }

  root.replaceChildren();
  for (const kind of ALL_KINDS) {
    if (!data.kinds.includes(kind)) continue;
    root.appendChild(renderKindSection(kind, byKind.get(kind) ?? []));
  }
}

/** One section per supported forge kind. */
function renderKindSection(kind: ForgeKind, accounts: ConfiguredForge[]): HTMLElement {
  const section = document.createElement("section");
  section.className = "forge-kind-section";
  section.dataset["kind"] = kind;

  // Header row: badge + title on the left, "+" button on the right
  // (vertically centered). The "+" opens a unified pane offering
  // both OAuth (when supported) and PAT — see onAddAccount.
  const header = document.createElement("header");
  header.className = "forge-kind-header";
  const badge = document.createElement("span");
  badge.className = `forge-kind-badge forge-kind-${kind}`;
  badge.innerHTML = KIND_ICONS[kind];
  header.appendChild(badge);
  const title = document.createElement("h3");
  title.className = "forge-kind-title";
  title.textContent = forgeKindLabel(kind);
  header.appendChild(title);

  const addBtn = document.createElement("button");
  addBtn.type = "button";
  addBtn.className = "btn-small forge-kind-add-btn";
  addBtn.dataset["forgeAdd"] = kind;
  addBtn.setAttribute("aria-label", "Add an account");
  addBtn.setAttribute("data-tooltip", "Add an account");
  addBtn.innerHTML = ICON_PLUS_16;
  addBtn.addEventListener("click", () => { onAddAccount(kind, section); });
  header.appendChild(addBtn);

  section.appendChild(header);

  // Account list: only render when there are accounts. An empty list
  // shows nothing (no "No accounts connected" filler) — the section
  // header alone with the + button is enough invitation.
  if (accounts.length > 0) {
    const list = document.createElement("ul");
    list.className = "forge-account-list";
    for (const a of accounts) list.appendChild(renderAccountRow(a));
    section.appendChild(list);
  }

  // Inline mount point for the add-account pane (OAuth + PAT) and
  // status messages.
  const slot = document.createElement("div");
  slot.className = "forge-kind-slot";
  slot.dataset["forgeSlot"] = kind;
  section.appendChild(slot);

  return section;
}

/** Slim row for one connected account. */
function renderAccountRow(a: ConfiguredForge): HTMLElement {
  const li = document.createElement("li");
  li.className = "forge-account-row";
  li.dataset["id"] = a.id;
  if (!a.connected) li.classList.add("forge-account-row-error");

  // Top row: identity (left) + actions (right). Same columns as
  // before; just wrapped in a div so the collapsible repos footer
  // below it stacks on its own row.
  const top = document.createElement("div");
  top.className = "forge-account-row-top";

  const id = document.createElement("div");
  id.className = "forge-account-identity";
  const primary = document.createElement("span");
  primary.className = "forge-account-primary";
  const hasEmail = a.email !== undefined && a.email !== "";
  const hasUsername = a.username !== undefined && a.username !== "";
  if (hasEmail || hasUsername) {
    primary.textContent = hasEmail ? a.email! : a.username!;
  } else {
    // No identity data yet (this is the first paint right after a
    // PAT submit / OAuth complete; the background probe hasn't
    // populated email or username yet). Show a skeleton bar
    // instead of falling back to the host string — the host is
    // already shown on the meta line below.
    primary.classList.add("skeleton", "forge-account-primary-skeleton");
    primary.setAttribute("aria-label", "Loading account identity…");
  }
  id.appendChild(primary);
  const meta = document.createElement("span");
  meta.className = "forge-account-meta";
  const parts: string[] = [];
  if (hasEmail && hasUsername) parts.push("@" + a.username!);
  if (a.host !== "") parts.push(a.host);
  meta.textContent = parts.join(" · ");
  id.appendChild(meta);
  if (!a.connected && a.last_error !== undefined && a.last_error !== "") {
    const err = document.createElement("span");
    err.className = "forge-account-error";
    err.textContent = a.last_error;
    id.appendChild(err);
  }
  top.appendChild(id);

  const actions = document.createElement("div");
  actions.className = "forge-account-actions";

  const manageURL = manageAccountURL(a.kind, a.host);
  if (manageURL !== "") {
    const manage = document.createElement("a");
    manage.href = manageURL;
    manage.target = "_blank";
    manage.rel = "noreferrer";
    manage.className = "btn-small forge-account-manage";
    manage.innerHTML = `<span>Manage</span>${ICON_EXTERNAL}`;
    manage.setAttribute("data-tooltip", "Manage account on forge");
    manage.setAttribute("aria-label", "Manage account on forge");
    actions.appendChild(manage);
  }

  const out = document.createElement("button");
  out.type = "button";
  out.className = "btn-small btn-danger";
  out.textContent = "Sign out";
  out.addEventListener("click", () => { void onSignOut(a); });
  actions.appendChild(out);

  top.appendChild(actions);
  li.appendChild(top);

  // Collapsible repos footer. Defaults closed; freshly-added accounts
  // open automatically (via expandOnNextPaint set populated on
  // successful add).
  if (a.connected) {
    const reposEl = renderAccountRepos(a);
    if (reposEl !== null) li.appendChild(reposEl);
  }

  return li;
}

/** Render the per-account collapsible list of repos accessible to
 *  this forge. Summary line: "X repos, Y cloned locally". Each repo
 *  row carries Clone / Trash / Open ↗ actions depending on its
 *  cloned state. Returns null if we have no repo data yet (still
 *  loading) so the row stays compact. */
function renderAccountRepos(a: ConfiguredForge): HTMLElement | null {
  const repos = lastReposByForge[a.id];
  if (repos === undefined) return null;
  const cloned = repos.filter((r) => lastLocalNames.has(r.name)).length;

  const details = document.createElement("details");
  details.className = "forge-account-repos";
  if (expandOnNextPaint.has(a.id)) {
    details.open = true;
    expandOnNextPaint.delete(a.id);
  }

  const summary = document.createElement("summary");
  summary.className = "forge-account-repos-summary";
  const total = repos.length;
  summary.innerHTML = `
    <span class="forge-account-repos-chevron" aria-hidden="true">▸</span>
    <span class="forge-account-repos-icon" aria-hidden="true">${ICON_REPO}</span>
    <span class="forge-account-repos-label">${total} repo${total === 1 ? "" : "s"}, ${cloned} cloned locally</span>
  `;

  // Right-aligned actions inside the summary. Click stops
  // propagation so they don't toggle the <details>.
  const cloneable = repos.filter((r) =>
    !lastLocalNames.has(r.name) &&
    typeof r.clone_url === "string" && r.clone_url !== "");
  const cloned_repos = repos.filter((r) => lastLocalNames.has(r.name));

  if (cloneable.length > 0) {
    const cloneAllBtn = document.createElement("button");
    cloneAllBtn.type = "button";
    cloneAllBtn.className = "btn-small forge-account-repos-clone-all";
    cloneAllBtn.innerHTML = `${ICON_DOWNLOAD}<span>${cloneable.length}</span>`;
    cloneAllBtn.setAttribute("data-tooltip", `Clone every uncloned repo on this account (${cloneable.length})`);
    cloneAllBtn.setAttribute("aria-label", `Clone ${cloneable.length} uncloned repos`);
    cloneAllBtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      ev.preventDefault();
      void withAsyncFeedback(cloneAllBtn, () => cloneAllForAccount(cloneable, cloneAllBtn));
    });
    summary.appendChild(cloneAllBtn);
  }

  if (cloned_repos.length > 0) {
    const deleteAllBtn = document.createElement("button");
    deleteAllBtn.type = "button";
    deleteAllBtn.className = "btn-small btn-danger forge-account-repos-delete-all";
    deleteAllBtn.innerHTML = `${ICON_TRASH}<span>${cloned_repos.length}</span>`;
    deleteAllBtn.setAttribute("data-tooltip", `Remove every locally-cloned repo on this account (${cloned_repos.length})`);
    deleteAllBtn.setAttribute("aria-label", `Delete ${cloned_repos.length} local clones`);
    deleteAllBtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      ev.preventDefault();
      void withAsyncFeedback(deleteAllBtn, () => deleteAllForAccount(cloned_repos, deleteAllBtn));
    });
    summary.appendChild(deleteAllBtn);
  }

  details.appendChild(summary);

  if (repos.length === 0) {
    const none = document.createElement("div");
    none.className = "forge-account-repos-empty";
    none.textContent = "No repositories accessible to this account.";
    details.appendChild(none);
    return details;
  }

  const list = document.createElement("ul");
  list.className = "forge-account-repos-list";
  // Sort: cloned first, then alpha by full_name.
  const sorted = [...repos].sort((x, y) => {
    const xc = lastLocalNames.has(x.name);
    const yc = lastLocalNames.has(y.name);
    if (xc !== yc) return xc ? -1 : 1;
    return x.full_name.localeCompare(y.full_name);
  });
  for (const r of sorted) list.appendChild(renderRepoRow(r));
  details.appendChild(list);

  return details;
}

/** One row inside an account's collapsible repo list. */
function renderRepoRow(repo: Repo): HTMLElement {
  const li = document.createElement("li");
  li.className = "forge-account-repo-row";
  const cloned = lastLocalNames.has(repo.name);

  // State indicator: pulsing green dot if cloned, globe if remote-only.
  const state = document.createElement("span");
  state.className = "forge-account-repo-state";
  if (cloned) {
    state.innerHTML = `<span class="git-sources-cloned-dot" aria-label="Cloned" data-tooltip="Cloned and tracked"></span>`;
  } else {
    state.innerHTML = ICON_GLOBE;
    state.setAttribute("data-tooltip", "Remote, not cloned");
    state.setAttribute("aria-label", "Remote, not cloned");
  }
  li.appendChild(state);

  // Identity
  const idEl = document.createElement("div");
  idEl.className = "forge-account-repo-identity";
  const name = document.createElement("span");
  name.className = "forge-account-repo-name";
  name.textContent = repo.full_name;
  idEl.appendChild(name);
  const tags: string[] = [];
  if (repo.private === true) tags.push("private");
  if (repo.archived === true) tags.push("archived");
  if (repo.fork === true) tags.push("fork");
  if (repo.default_branch !== undefined && repo.default_branch !== "") tags.push(repo.default_branch);
  if (tags.length > 0) {
    const tagSpan = document.createElement("span");
    tagSpan.className = "forge-account-repo-tags";
    tagSpan.textContent = tags.join(" · ");
    idEl.appendChild(tagSpan);
  }
  li.appendChild(idEl);

  // Actions
  const actions = document.createElement("span");
  actions.className = "forge-account-repo-actions";

  if (repo.url !== undefined && repo.url !== "") {
    const open = document.createElement("a");
    open.href = repo.url;
    open.target = "_blank";
    open.rel = "noreferrer";
    open.className = "btn-small icon-only";
    open.innerHTML = ICON_EXTERNAL;
    open.setAttribute("data-tooltip", "Open on forge");
    open.setAttribute("aria-label", "Open on forge");
    actions.appendChild(open);
  }

  if (cloned) {
    const trash = document.createElement("button");
    trash.type = "button";
    trash.className = "btn-small btn-danger icon-only";
    trash.innerHTML = ICON_TRASH;
    trash.setAttribute("data-tooltip", "Remove local copy");
    trash.setAttribute("aria-label", "Remove local copy");
    trash.addEventListener("click", () => {
      void withAsyncFeedback(trash, () => removeLocalRepo(repo));
    });
    actions.appendChild(trash);
  } else if (repo.clone_url !== undefined && repo.clone_url !== "") {
    const clone = document.createElement("button");
    clone.type = "button";
    clone.className = "btn-small icon-only";
    clone.innerHTML = ICON_DOWNLOAD;
    clone.setAttribute("data-tooltip", "Clone into workspace");
    clone.setAttribute("aria-label", "Clone into workspace");
    clone.addEventListener("click", () => {
      void withAsyncFeedback(clone, () => cloneRepo(repo.clone_url ?? ""));
    });
    actions.appendChild(clone);
  }

  li.appendChild(actions);
  return li;
}

async function cloneRepo(url: string): Promise<void> {
  if (url === "") throw new Error("no clone URL");
  const res = await cloneRepoAction.dispatch({ url });
  if (res === null) {
    toastError("Couldn't clone repo");
    throw new Error("clone failed");
  }
  if (res.error !== undefined && res.error !== "") {
    toastError(`Couldn't clone repo: ${res.error}`);
    throw new Error(res.error);
  }
  await renderForgesPanel({ revalidate: false });
}

/** Clone every uncloned remote repo accessible to one account.
 *  Iterates sequentially (avoids hammering the workspace's git-clone
 *  capacity and keeps failures attributable to a single repo).
 *  Visible feedback rides on the button label "Cloning N/total…".
 *  Per-repo failures don't abort the batch — partial success > none.
 *  After the batch we trigger a single renderForgesPanel so each
 *  repo row redraws with its new state. */
async function cloneAllForAccount(
  candidates: Repo[],
  btn: HTMLButtonElement,
): Promise<void> {
  if (candidates.length === 0) return;
  let done = 0;
  let failed = 0;
  for (const repo of candidates) {
    btn.textContent = `Cloning ${done + 1}/${candidates.length}…`;
    const url = repo.clone_url ?? "";
    if (url !== "") {
      const res = await cloneRepoAction.dispatch({ url });
      if (res === null) { failed++; }
      else if (res.error !== undefined && res.error !== "") { failed++; }
    } else {
      failed++;
    }
    done++;
  }
  if (failed > 0) {
    toastError(`Clone failed for ${String(failed)} of ${String(candidates.length)} repos`);
  }
  await renderForgesPanel({ revalidate: false });
}

/** Remove the local copy of every cloned repo accessible to one
 *  account. One bulk confirm up-front (this is destructive); then
 *  sequential per-repo /api/git/remove. Same partial-success
 *  semantics as cloneAllForAccount. */
async function deleteAllForAccount(
  candidates: Repo[],
  btn: HTMLButtonElement,
): Promise<void> {
  if (candidates.length === 0) return;
  const ok = await confirmDialog(
    `Delete the local copy of ${candidates.length} repo${candidates.length === 1 ? "" : "s"}? The remotes stay intact; you can re-clone any of them later.`,
    "Delete all",
    "destructive",
  );
  if (!ok) return;

  let done = 0;
  let failed = 0;
  for (const repo of candidates) {
    btn.textContent = `Deleting ${done + 1}/${candidates.length}…`;
    const res = await deleteLocalAction.dispatch({ repoName: repo.name });
    if (res === null) { failed++; }
    else if (res.error !== undefined && res.error !== "") { failed++; }
    done++;
  }
  if (failed > 0) {
    toastError(`Delete failed for ${String(failed)} of ${String(candidates.length)} repos`);
  }
  await renderForgesPanel({ revalidate: false });
}

async function removeLocalRepo(repo: Repo): Promise<void> {
  const ok = await confirmDialog(
    `Delete the local copy of ${repo.name}? The remote stays intact; you can re-clone later.`,
    "Delete",
    "destructive",
  );
  if (!ok) return;

  // Optimistic: flip clone state to not-cloned and re-render.
  lastLocalNames.delete(repo.name);
  await renderForgesPanel({ revalidate: false });

  const res = await deleteLocalAction.dispatch({ repoName: repo.name });
  if (res === null || (res.error !== undefined && res.error !== "")) {
    // Rollback: restore clone state and re-render.
    lastLocalNames.add(repo.name);
    await renderForgesPanel({ revalidate: false });
    const msg = res?.error ?? "Couldn't remove local repo";
    toastError(msg);
    throw new Error(msg);
  }
}

// --- Add-account flow dispatch ---

/** Toggle the add-account pane on a forge section. The pane offers
 *  every supported method side-by-side (or stacked) so the user
 *  picks one without flipping between buttons. For GitHub that's
 *  OAuth device flow + PAT paste; for GitLab/Codeberg/Gitea it's
 *  PAT paste only (no OAuth supported yet by those CLIs). Clicking
 *  the same `+` button twice closes the pane. */
function onAddAccount(kind: ForgeKind, section: HTMLElement): void {
  const slot = slotOf(section);
  if (slot.dataset["mode"] === "add") {
    closeSlot(slot);
    return;
  }
  slot.dataset["mode"] = "add";
  showAddPane(section, kind);
}

function showAddPane(section: HTMLElement, kind: ForgeKind): void {
  const slot = slotOf(section);
  slot.innerHTML = "";

  const pane = document.createElement("div");
  pane.className = "forge-add-pane";

  // Lead-in text. Phrasing depends on whether OAuth is offered.
  const intro = document.createElement("p");
  intro.className = "forge-add-pane-intro";
  const hasOAuth = oauthByKind[kind] === true;
  intro.textContent = hasOAuth
    ? "Sign in with the one-time browser flow, or paste a personal access token. Either path produces a token the CLI uses for git + API operations."
    : "Paste a personal access token. The CLI uses it for git + API operations.";
  pane.appendChild(intro);

  // For kinds with OAuth: OAuth section on top, divider, PAT below.
  if (hasOAuth) {
    const oauth = document.createElement("div");
    oauth.className = "forge-add-pane-section";
    const oauthHeading = document.createElement("h4");
    oauthHeading.className = "forge-add-pane-heading";
    oauthHeading.textContent = "Browser-based sign-in";
    oauth.appendChild(oauthHeading);
    const oauthBody = document.createElement("div");
    oauthBody.className = "forge-add-pane-body";
    oauth.appendChild(oauthBody);
    pane.appendChild(oauth);
    void startGitHubDeviceFlow(oauthBody);

    const divider = document.createElement("hr");
    divider.className = "forge-add-pane-divider";
    pane.appendChild(divider);
  }

  // PAT form (works for every kind via the kind-agnostic backend).
  const patSection = document.createElement("div");
  patSection.className = "forge-add-pane-section";
  const patHeading = document.createElement("h4");
  patHeading.className = "forge-add-pane-heading";
  patHeading.textContent = "Personal access token";
  patSection.appendChild(patHeading);
  const patBody = document.createElement("div");
  patBody.className = "forge-add-pane-body";
  patSection.appendChild(patBody);
  pane.appendChild(patSection);

  slot.appendChild(pane);
  renderPATForm(patBody, kind, slot);
}

function closeSlot(slot: HTMLElement): void {
  slot.replaceChildren();
  delete slot.dataset["mode"];
}

function slotOf(section: HTMLElement): HTMLElement {
  const slot = section.querySelector<HTMLElement>("[data-forge-slot]");
  if (slot === null) {
    const div = document.createElement("div");
    section.appendChild(div);
    return div;
  }
  return slot;
}

// --- GitHub OAuth device flow ---

async function startGitHubDeviceFlow(host: HTMLElement): Promise<void> {
  setStatus(host, "Contacting GitHub…");
  const start = await startDeviceFlow.dispatch({});
  if (start === null) {
    setStatus(host, "Failed to start device flow.", "err");
    return;
  }
  renderDevicePrompt(host, start);
  pollGitHubDevice(host, start.device_code, Math.max(start.interval, 5));
}

function renderDevicePrompt(host: HTMLElement, start: DeviceFlowResponse): void {
  host.innerHTML = "";
  const container = document.createElement("div");
  container.className = "forge-device-prompt";
  const safeLink = /^https?:\/\//i.test(start.verification_uri);
  container.innerHTML =
    `<p>Open ${safeLink ? `<a class="forge-device-link" target="_blank" rel="noreferrer" href="${escapeAttr(start.verification_uri)}">` : ""}${escapeHTML(start.verification_uri)}${safeLink ? "</a>" : ""} and enter:</p>` +
    `<div class="forge-device-code-row"><code class="forge-device-code">${escapeHTML(start.user_code)}</code><button type="button" class="btn-small forge-copy-btn" data-copy="${escapeAttr(start.user_code)}">Copy</button></div>` +
    `<div class="forge-device-status">Waiting for approval…</div>`;
  host.appendChild(container);
  const copyBtn = container.querySelector<HTMLButtonElement>(".forge-copy-btn");
  copyBtn?.addEventListener("click", () => {
    const code = copyBtn.dataset["copy"] ?? "";
    void navigator.clipboard.writeText(code);
    copyBtn.textContent = "Copied";
    setTimeout(() => { copyBtn.textContent = "Copy"; }, 2000);
  });
}

function pollGitHubDevice(host: HTMLElement, deviceCode: string, intervalSec: number): void {
  const statusEl = host.querySelector<HTMLDivElement>(".forge-device-status");
  let attempts = 0;
  let backoff = intervalSec;
  const MAX_ATTEMPTS = 60;
  const tick = async (): Promise<void> => {
    if (!host.isConnected) return;
    attempts++;
    if (attempts > MAX_ATTEMPTS) {
      if (statusEl !== null) statusEl.textContent = "Timed out waiting for approval. Try again.";
      return;
    }
    const res = await apiPost<PollResult>("/api/forges/oauth/github/poll", { device_code: deviceCode });
    if (res === null) {
      if (statusEl !== null) statusEl.textContent = "Network error. Retrying…";
      backoff = Math.min(backoff * 2, 60);
      setTimeout(() => void tick(), backoff * 1000);
      return;
    }
    // Reset backoff on successful network response.
    backoff = intervalSec;
    if (res.status === "complete") {
      if (statusEl !== null) statusEl.textContent = "Connected.";
      expandOnNextPaint.add("github:github.com");
      void renderForgesPanel();
      return;
    }
    if (res.status === "expired") {
      if (statusEl !== null) statusEl.textContent = "Device code expired. Try again.";
      return;
    }
    if (res.status === "error") {
      if (statusEl !== null) statusEl.textContent = `Error: ${res.error ?? "unknown"}`;
      return;
    }
    setTimeout(() => void tick(), intervalSec * 1000);
  };
  setTimeout(() => void tick(), intervalSec * 1000);
}

// --- PAT paste form (works for ALL kinds; backend is kind-agnostic) ---

/** Render a PAT entry form into a host element. The slot reference
 *  is needed by the Cancel button so it can close the entire add
 *  pane (not just the form). Used by the unified add pane on every
 *  kind, including GitHub where it sits below the OAuth section. */
function renderPATForm(hostEl: HTMLElement, kind: ForgeKind, slot: HTMLElement): void {
  hostEl.innerHTML = "";

  const helpLink = PAT_HELP_LINKS[kind];
  if (helpLink !== null) {
    const help = document.createElement("p");
    help.className = "forge-help";
    const a = document.createElement("a");
    a.href = helpLink.url;
    a.target = "_blank";
    a.rel = "noreferrer";
    a.textContent = helpLink.label;
    help.appendChild(a);
    hostEl.appendChild(help);
  } else if (kind === "gitea") {
    const help = document.createElement("p");
    help.className = "forge-help";
    help.textContent = "Create a token at /user/settings/applications on your Gitea or Forgejo host.";
    hostEl.appendChild(help);
  }

  const form = document.createElement("form");
  form.className = "forge-pat-form";

  const hostLocked = HOST_LOCKED_KINDS.includes(kind);
  const hostInput = document.createElement("input");
  if (hostLocked) {
    hostInput.type = "hidden";
  } else {
    hostInput.type = "text";
    hostInput.placeholder = hostPlaceholder(kind);
    hostInput.className = "tool-form-input";
    hostInput.required = true;
  }
  hostInput.value = DEFAULT_HOST[kind];
  form.appendChild(hostInput);

  const tokenInput = document.createElement("input");
  tokenInput.type = "password";
  tokenInput.placeholder = "token";
  tokenInput.className = "tool-form-input";
  tokenInput.required = true;
  form.appendChild(tokenInput);

  const status = document.createElement("div");
  status.className = "forge-card-status";
  form.appendChild(status);

  const submit = document.createElement("button");
  submit.type = "submit";
  submit.className = "btn-small btn-primary";
  submit.textContent = "Connect";
  form.appendChild(submit);

  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.className = "btn-small";
  cancel.textContent = "Cancel";
  cancel.addEventListener("click", () => { closeSlot(slot); });
  form.appendChild(cancel);

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const hostVal = hostInput.value.trim();
    void doPATConnect(kind, hostVal, tokenInput.value.trim(), status, () => {
      tokenInput.value = "";
      closeSlot(slot);
      // Auto-expand the freshly-added account on the next paint.
      expandOnNextPaint.add(`${kind}:${hostVal}`);
      void renderForgesPanel();
    });
  });

  bindLoadingState("forge.connect_pat", submit);

  hostEl.appendChild(form);
}

async function doPATConnect(
  kind: ForgeKind,
  host: string,
  token: string,
  status: HTMLElement,
  onSuccess: () => void,
): Promise<void> {
  if (host === "" || token === "") {
    status.textContent = "Both host and token are required.";
    status.className = "forge-card-status err";
    return;
  }
  status.textContent = "Validating…";
  status.className = "forge-card-status";
  const res = await connectPAT.dispatch({ kind, host, token });
  if (res === null) {
    status.textContent = "Network error.";
    status.className = "forge-card-status err";
    return;
  }
  if (res.error !== undefined) {
    status.textContent = res.error;
    status.className = "forge-card-status err";
    return;
  }
  status.textContent = "Connected.";
  status.className = "forge-card-status ok";
  onSuccess();
}

// --- Sign out ---

async function onSignOut(f: ConfiguredForge): Promise<void> {
  const label = f.email ?? f.username ?? f.host;
  const ok = await confirmDialog(
    `Sign out of ${label}? The token will be removed from the CLI config.`,
    "Sign out",
    "destructive",
  );
  if (!ok) return;

  // Optimistic: hide the account row immediately.
  const row = document.querySelector<HTMLElement>(`.forge-account-row[data-id="${CSS.escape(f.id)}"]`);
  if (row !== null) row.hidden = true;

  const res = await signOut.dispatch({ forgeId: f.id });
  if (res === null) {
    // Rollback: show the row again.
    if (row !== null) row.hidden = false;
    return;
  }
  void renderForgesPanel();
}

// --- helpers ---

function escapeHTML(s: string): string {
  const map: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return s.replace(/[&<>"']/g, (c) => map[c] ?? c);
}

function escapeAttr(s: string): string {
  return escapeHTML(s);
}

function setStatus(host: HTMLElement, text: string, kind: "ok" | "err" | "" = ""): void {
  host.innerHTML = "";
  const div = document.createElement("div");
  div.className = `forge-card-status ${kind}`;
  div.textContent = text;
  host.appendChild(div);
}

/** Public entry point: initialize the forge connection panel.
 *  Alias for renderForgesPanel — kept under this name because
 *  settings.ts imports it that way at boot. */
export function initForgeAuth(): Promise<void> {
  return renderForgesPanel();
}
