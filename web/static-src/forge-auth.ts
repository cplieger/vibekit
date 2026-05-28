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

import { apiGetTyped, apiPost, CancellableSlot } from "./api-client.js";
import type { Decoder } from "./validators.js";
import { asObject, decodeArray } from "./validators.js";
import { decodeConfiguredForge, decodeRepo } from "./wire/decoders.gen.js";
import { confirm as confirmDialog } from "./confirm.js";
import {
  ICON_EXTERNAL,
  ICON_PLUS_16,
} from "./icons.js";
import type {
  ConfiguredForge,
  ForgeKind,
  Repo,
} from "./wire/types.gen.js";
import { HOST_LOCKED_KINDS, DEFAULT_HOST, FORGE_META, FORGE_URLS, kindTitle } from "./forge-types.js";
import {
  signOut,
} from "./actions/forge.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { signal, effect } from "./lib/reactive/index.js";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { startGitHubDeviceFlow, abortPoll, type OAuthFlowDeps } from "./forge-auth-oauth.js";
import { renderPATForm, type PATFormDeps } from "./forge-auth-pat.js";
import {
  renderRepoRow,
  renderRepoState,
  renderRepoActions,
  type RepoDeps,
} from "./forge-auth-repos.js";
import {
  buildAccountReposDetails as buildAccountReposDetailsImpl,
  updateAccountReposDetails as updateAccountReposDetailsImpl,
  type ReposRenderDeps,
} from "./forge-auth-repos-render.js";

interface ForgesListResponse {
  forges: ConfiguredForge[];
  kinds: ForgeKind[];
  oauth?: Partial<Record<ForgeKind, boolean>>;
}

interface RepoListResponse {
  repos: Repo[];
}
interface LocalReposResponse {
  repos: string[];
}

// --- Response decoders ------------------------------------------------

const FORGE_KINDS: readonly ForgeKind[] = ["github", "gitlab", "codeberg", "gitea"];

const decodeForgesListResponse: Decoder<ForgesListResponse> = (v) => {
  const o = asObject(v, "$.forges_list");
  const out: ForgesListResponse = {
    forges: decodeArray(o["forges"], decodeConfiguredForge, "$.forges_list.forges"),
    kinds: decodeArray(o["kinds"], (el) => {
      if (typeof el !== "string" || !(FORGE_KINDS as readonly string[]).includes(el)) {
        throw new TypeError(`expected ForgeKind, got ${JSON.stringify(el)}`);
      }
      return el as ForgeKind;
    }, "$.forges_list.kinds"),
  };
  if (o["oauth"] !== undefined) {
    const oauthObj = asObject(o["oauth"], "$.forges_list.oauth");
    const partial: Partial<Record<ForgeKind, boolean>> = {};
    for (const [k, val] of Object.entries(oauthObj)) {
      if ((FORGE_KINDS as readonly string[]).includes(k) && typeof val === "boolean") {
        partial[k as ForgeKind] = val;
      }
    }
    out.oauth = partial;
  }
  return out;
};

const decodeRepoListResponse: Decoder<RepoListResponse> = (v) => {
  const o = asObject(v, "$.repo_list");
  return { repos: decodeArray(o["repos"], decodeRepo, "$.repo_list.repos") };
};

const decodeLocalReposResponse: Decoder<LocalReposResponse> = (v) => {
  const o = asObject(v, "$.local_repos");
  return {
    repos: decodeArray(o["repos"], (el) => {
      if (typeof el !== "string") {
        throw new TypeError(`expected string, got ${typeof el}`);
      }
      return el;
    }, "$.local_repos.repos"),
  };
};

// --- iconEl helper (template-based SVG cloning) -----------------------

import { iconEl } from "./icon-el.js";
export { iconEl };

// --- Module state -----------------------------------------------------

/** Cached repo list keyed by forge ID. Populated on every render so
 *  each account's collapsible footer can show "X repos, Y cloned" and
 *  list the repos accessible to that forge. */
let lastReposByForge: Record<string, Repo[]> = {};

/** Names of locally-cloned repos. Used to compute the cloned-count
 *  per-account and to drive the green dot / Trash button per row. */
let lastLocalNames = new Set<string>();

/** Forge IDs whose collapsible footer should render expanded on the
 *  next paint. Populated when the user successfully adds an account
 *  (PAT submit or OAuth complete) so the user lands on the freshly-
 *  added account with its repos visible. Cleared after one paint. */
const expandOnNextPaint = new Set<string>();

/** OAuth availability per kind, populated from the forges list response. */
let oauthByKind: Partial<Record<ForgeKind, boolean>> = {};

/** Per-render cleanup: unbind functions from bindLoadingState on sign-out
 *  buttons. Drained at the top of each paintIntoRoot call. */
let signOutUnbinds: (() => void)[] = [];

/** Per-render cleanup: unbind functions from bindLoadingState on PAT
 *  form submit buttons. Drained at the top of each paintIntoRoot call. */
let patFormUnbinds: (() => void)[] = [];

/** True when the last /api/forges fetch failed; the effect renders an
 *  error UI with a Retry button instead of the kind sections. */
let lastForgesError = false;

/** Last-known forges payload. Effect paints from this when non-null
 *  and `lastForgesError` is false. */
let lastForgesData: ForgesListResponse | null = null;

/** Generation counter to prevent stale concurrent renderForgesPanel
 *  calls from overwriting a newer render. */
let renderGen = 0;

/** Monotonic state-version signal. Every mutation to lastForgesData /
 *  lastReposByForge / lastLocalNames / lastForgesError bumps this; the
 *  paint effect subscribes to it and reconciles the panel. */
const stateVersion = signal(0);

function bumpState(): void {
  stateVersion.value = stateVersion.peek() + 1;
}

/** Lazy-initialized paint effect. First renderForgesPanel call sets it
 *  up; subsequent calls are no-ops. The effect runs on every bumpState
 *  and re-acquires #forges-panel each run, so tab close/reopen is
 *  handled transparently (the next bump after re-mounting paints into
 *  the new root). */
let panelEffectStarted = false;
function ensurePanelEffect(): void {
  if (panelEffectStarted) {
    return;
  }
  panelEffectStarted = true;
  effect(() => {
    void stateVersion.value; // subscribe
    const root = document.getElementById("forges-panel");
    if (root === null) {
      return;
    }
    paintIntoRoot(root);
  });
}

// --- In-flight handles for cancel-on-navigate -------------------------

/** AbortController for the background revalidation probes. */
let revalidateController: AbortController | null = null;

/** CancellableSlot for the primary renderForgesPanel fetch path.
 *  Each call aborts the previous in-flight fetch. */
const panelSlot = new CancellableSlot();

registerCleanup(() => {
  abortPoll();
  revalidateController?.abort();
  panelSlot.abort();
});

const ALL_KINDS = Object.keys(FORGE_META) as ForgeKind[];

/** Brand SVG glyphs from Simple Icons (CC0). 24x24 viewBox; CSS sizes
 *  them down to fit the 22-px kind badge. */
const KIND_ICONS: Record<ForgeKind, string> = {
  github: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12"/></svg>`,
  gitlab: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m23.6004 9.5927-.0337-.0862L20.3.9814a.851.851 0 0 0-.3362-.405.8748.8748 0 0 0-.9997.0539.8748.8748 0 0 0-.29.4399l-2.2055 6.748H7.5375l-2.2057-6.748a.8573.8573 0 0 0-.29-.4412.8748.8748 0 0 0-.9997-.0537.8585.8585 0 0 0-.3362.4049L.4332 9.5015l-.0325.0862a6.0657 6.0657 0 0 0 2.0119 7.0105l.0113.0087.03.0213 4.976 3.7264 2.462 1.8633 1.4995 1.1321a1.0085 1.0085 0 0 0 1.2197 0l1.4995-1.1321 2.4619-1.8633 5.006-3.7489.0125-.01a6.0682 6.0682 0 0 0 2.0094-7.003z"/></svg>`,
  codeberg: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M11.955.49A12 12 0 0 0 0 12.49a12 12 0 0 0 1.832 6.373L11.838 5.928a.187.14 0 0 1 .324 0l10.006 12.935A12 12 0 0 0 24 12.49a12 12 0 0 0-12-12 12 12 0 0 0-.045 0zm.375 6.467l4.416 16.553a12 12 0 0 0 5.137-4.213z"/></svg>`,
  gitea: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4.209 4.603c-.247 0-.525.02-.84.088-.333.07-1.28.283-2.054 1.027C-.403 7.25.035 9.685.089 10.052c.065.446.263 1.687 1.21 2.768 1.749 2.141 5.513 2.092 5.513 2.092s.462 1.103 1.168 2.119c.955 1.263 1.936 2.248 2.89 2.367 2.406 0 7.212-.004 7.212-.004s.458.004 1.08-.394c.535-.324 1.013-.893 1.013-.893s.492-.527 1.18-1.73c.21-.37.385-.729.538-1.068 0 0 2.107-4.471 2.107-8.823-.042-1.318-.367-1.55-.443-1.627-.156-.156-.366-.153-.366-.153s-4.475.252-6.792.306c-.508.011-1.012.023-1.512.027v4.474l-.634-.301c0-1.39-.004-4.17-.004-4.17-1.107.016-3.405-.084-3.405-.084s-5.399-.27-5.987-.324c-.187-.011-.401-.032-.648-.032zm.354 1.832h.111s.271 2.269.6 3.597C5.549 11.147 6.22 13 6.22 13s-.996-.119-1.641-.348c-.99-.324-1.409-.714-1.409-.714s-.73-.511-1.096-1.52C1.444 8.73 2.021 7.7 2.021 7.7s.32-.859 1.47-1.145c.395-.106.863-.12 1.072-.12zm8.33 2.554c.26.003.509.127.509.127l.868.422-.529 1.075a.686.686 0 0 0-.614.359.685.685 0 0 0 .072.756l-.939 1.924a.69.69 0 0 0-.66.527.687.687 0 0 0 .347.763.686.686 0 0 0 .867-.206.688.688 0 0 0-.069-.882l.916-1.874a.667.667 0 0 0 .237-.02.657.657 0 0 0 .271-.137 8.826 8.826 0 0 1 1.016.512.761.761 0 0 1 .286.282c.073.21-.073.569-.073.569-.087.29-.702 1.55-.702 1.55a.692.692 0 0 0-.676.477.681.681 0 1 0 1.157-.252c.073-.141.141-.282.214-.431.19-.397.515-1.16.515-1.16.035-.066.218-.394.103-.814-.095-.435-.48-.638-.48-.638-.467-.301-1.116-.58-1.116-.58s0-.156-.042-.27a.688.688 0 0 0-.148-.241l.516-1.062 2.89 1.401s.48.218.583.619c.073.282-.019.534-.069.657-.24.587-2.1 4.317-2.1 4.317s-.232.554-.748.588a1.065 1.065 0 0 1-.393-.045l-.202-.08-4.31-2.1s-.417-.218-.49-.596c-.083-.31.104-.691.104-.691l2.073-4.272s.183-.37.466-.497a.855.855 0 0 1 .35-.077z"/></svg>`,
};

/** Manage-account URL on the forge itself, parameterized by host. */
function manageAccountURL(kind: ForgeKind, host: string): string {
  return FORGE_URLS[kind](host);
}

/** Placeholder shown in the host input on the PAT form. For kinds with
 *  a sensible default (e.g. gitlab.com) we echo the default; for kinds
 *  with no canonical host (gitea/forgejo) we show an unambiguous
 *  example so users can't miss that the field needs filling. */
function hostPlaceholder(kind: ForgeKind): string {
  return DEFAULT_HOST[kind] || "your-host.example.com";
}

/** Render the full forges panel. Idempotent; call after every list
 *  mutation to refresh.
 *
 *  When `revalidate` is true (the default), connected accounts are
 *  re-probed in parallel after the initial paint. Tokens can be
 *  silently revoked or expire; this catches that on page open. The
 *  initial paint shows last-known state immediately; the panel
 *  re-renders once when all probes have settled.
 *
 *  When `skipRepos` is true, the local-repos fetch is skipped so that
 *  an optimistic in-memory mutation (e.g. removeLocalRepo) is not
 *  overwritten by a stale server response before the action completes. */
export async function renderForgesPanel(
  opts: { revalidate?: boolean; skipRepos?: boolean } = {},
): Promise<void> {
  const root = document.getElementById("forges-panel");
  if (root === null) {
    return;
  }

  ensurePanelEffect();

  const myGen = ++renderGen;
  const signal = panelSlot.start();

  const data = await apiGetTyped("/api/forges", decodeForgesListResponse, signal);
  if (signal.aborted || myGen !== renderGen) {
    return;
  }
  if (data === null) {
    lastForgesError = true;
    bumpState();
    return;
  }

  // Refresh repo + local-clone caches in parallel with the forges
  // list. The per-account collapsible footer renders from these.
  // When skipRepos is true (optimistic updates), skip the local-repos
  // fetch so the caller's in-memory mutation isn't overwritten.
  if (opts.skipRepos !== true) {
    const [localNames, reposByForge] = await Promise.all([
      refreshLocalNames(signal),
      refreshReposByForge(data.forges, signal),
    ]);
    if (signal.aborted || myGen !== renderGen) {
      return;
    }
    lastLocalNames = localNames;
    lastReposByForge = reposByForge;
  } else {
    const reposByForge = await refreshReposByForge(data.forges, signal);
    if (signal.aborted || myGen !== renderGen) {
      return;
    }
    lastReposByForge = reposByForge;
  }

  lastForgesError = false;
  lastForgesData = data;
  oauthByKind = data.oauth ?? {};
  bumpState();

  if (opts.revalidate !== false) {
    const ids = data.forges.filter((f) => f.connected).map((f) => f.id);
    if (ids.length > 0) {
      void revalidateInBackground(ids);
    }
  }
}

async function refreshLocalNames(signal?: AbortSignal): Promise<Set<string>> {
  const r = await apiGetTyped("/api/git/repos", decodeLocalReposResponse, signal);
  return new Set((r?.repos ?? []).filter((n) => n !== "."));
}

async function refreshReposByForge(
  forges: ConfiguredForge[],
  signal?: AbortSignal,
): Promise<Record<string, Repo[]>> {
  const map: Record<string, Repo[]> = {};
  await Promise.all(
    forges
      .filter((f) => f.connected)
      .map(async (f) => {
        const r = await apiGetTyped(
          `/api/forges/${encodeURIComponent(f.id)}/repos`,
          decodeRepoListResponse,
          signal,
        );
        map[f.id] = r?.repos ?? [];
      }),
  );
  return map;
}

/** Re-probe every connected account in parallel; on completion, re-fetch
 *  /api/forges and bump state with the post-probe results. The paint
 *  effect surgically reconciles the panel; an open add-account slot is
 *  preserved as a non-keyed sibling so the user's mid-interaction is
 *  not disrupted. */
async function revalidateInBackground(ids: string[]): Promise<void> {
  // Cancel any prior in-flight revalidation; we always want the most
  // recent paint's state to win.
  revalidateController?.abort();
  revalidateController = new AbortController();
  const signal = revalidateController.signal;
  const myGen = renderGen;
  await Promise.allSettled(
    ids.map((id) => apiPost(`/api/forges/${encodeURIComponent(id)}/probe`, {}, signal)),
  );
  if (signal.aborted) {
    return;
  }
  const data = await apiGetTyped("/api/forges", decodeForgesListResponse, signal);
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check
  if (signal.aborted) {
    return;
  }
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check
  if (data === null || data === undefined) {
    return;
  }
  const [localNames, reposByForge] = await Promise.all([
    refreshLocalNames(signal),
    refreshReposByForge(data.forges, signal),
  ]);
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check
  if (signal.aborted) {
    return;
  }
  if (myGen !== renderGen) {
    return;
  }
  lastLocalNames = localNames;
  lastReposByForge = reposByForge;
  lastForgesData = data;
  oauthByKind = data.oauth ?? {};
  bumpState();
}

// --- Effect-driven paint ----------------------------------------------

/** Called by the paint effect on every state change. Reconciles the
 *  panel root to match the latest forges/repos/local-names state. */
function paintIntoRoot(root: HTMLElement): void {
  // Drain per-render cleanup from previous paint.
  for (const fn of signOutUnbinds) {
    fn();
  }
  signOutUnbinds = [];
  for (const fn of patFormUnbinds) {
    fn();
  }
  patFormUnbinds = [];

  if (lastForgesError) {
    paintErrorState(root);
    return;
  }
  if (lastForgesData === null) {
    return;
  }

  // Clear any leftover error UI before reconciling kind sections.
  const errEl = root.querySelector(":scope > .forge-error");
  errEl?.remove();

  const supportedKinds = ALL_KINDS.filter((k) => lastForgesData!.kinds.includes(k)); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  reconcile(root, supportedKinds, kindSpec);
}

function paintErrorState(root: HTMLElement): void {
  // Remove existing keyed kind sections first; the error UI stands alone.
  for (const child of [...root.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") !== null) {
      child.remove();
    }
  }
  if (root.querySelector(":scope > .forge-error") !== null) {
    return;
  } // already shown
  const errDiv = document.createElement("div");
  errDiv.className = "forge-error";
  errDiv.textContent = "Failed to load forges.";
  const retryBtn = document.createElement("button");
  retryBtn.type = "button";
  retryBtn.className = "btn-small";
  retryBtn.textContent = "Retry";
  retryBtn.addEventListener("click", () => {
    void renderForgesPanel();
  });
  errDiv.appendChild(retryBtn);
  root.appendChild(errDiv);
}

// --- Reconcile specs --------------------------------------------------

const kindSpec: ReconcileSpec<ForgeKind> = {
  key: (k) => k,
  mount: (k) => buildKindSection(k),
  update: (el, k) => {
    updateKindSection(el, k);
  },
};

const accountSpec: ReconcileSpec<ConfiguredForge> = {
  key: (a) => a.id,
  mount: (a) => {
    const li = document.createElement("li");
    li.className = "forge-account-row";
    li.dataset["id"] = a.id;
    paintAccountRow(li, a);
    return li;
  },
  update: (li, a) => {
    paintAccountRow(li, a);
  },
};

const repoSpec: ReconcileSpec<Repo> = {
  key: (r) => r.name,
  mount: (r) => renderRepoRow(r, repoDeps),
  update: (li, r) => {
    const cloned = lastLocalNames.has(r.name);
    li.querySelector(":scope > .forge-account-repo-state")?.replaceWith(renderRepoState(cloned));
    li.querySelector(":scope > .forge-account-repo-actions")?.replaceWith(
      renderRepoActions(r, cloned, repoDeps),
    );
  },
};

// --- Kind section -----------------------------------------------------

/** Build a fresh section element for one forge kind. The header,
 *  account list container, and slot are static (their identity is
 *  preserved across re-paints); the account list is reconciled on
 *  every update. */
function buildKindSection(kind: ForgeKind): HTMLElement {
  const section = document.createElement("section");
  section.className = "forge-kind-section";
  section.dataset["kind"] = kind;

  // Header: badge + title + add button.
  const header = document.createElement("header");
  header.className = "forge-kind-header";
  const badge = document.createElement("span");
  badge.className = `forge-kind-badge forge-kind-${kind}`;
  badge.replaceChildren(iconEl(KIND_ICONS[kind]));
  header.appendChild(badge);
  const title = document.createElement("h3");
  title.className = "forge-kind-title";
  title.textContent = kindTitle(kind);
  header.appendChild(title);

  const addBtn = document.createElement("button");
  addBtn.type = "button";
  addBtn.className = "btn-small forge-kind-add-btn";
  addBtn.dataset["forgeAdd"] = kind;
  addBtn.setAttribute("aria-label", "Add an account");
  addBtn.setAttribute("data-tooltip", "Add an account");
  addBtn.replaceChildren(iconEl(ICON_PLUS_16));
  addBtn.addEventListener("click", () => {
    onAddAccount(kind, section);
  });
  header.appendChild(addBtn);

  section.appendChild(header);

  // Always present an account list container so reconcile has a
  // deterministic mount point. Empty list renders nothing.
  const list = document.createElement("ul");
  list.className = "forge-account-list";
  section.appendChild(list);
  reconcile(list, accountsForKind(kind), accountSpec);

  // Inline mount point for the add-account pane (OAuth + PAT) and
  // status messages. Non-keyed sibling — survives reconcile.
  const slot = document.createElement("div");
  slot.className = "forge-kind-slot";
  slot.dataset["forgeSlot"] = kind;
  section.appendChild(slot);

  return section;
}

function updateKindSection(section: HTMLElement, kind: ForgeKind): void {
  const list = section.querySelector<HTMLElement>(":scope > .forge-account-list");
  if (list === null) {
    return;
  }
  reconcile(list, accountsForKind(kind), accountSpec);
}

function accountsForKind(kind: ForgeKind): ConfiguredForge[] {
  if (lastForgesData === null) {
    return [];
  }
  return lastForgesData.forges.filter((f) => f.kind === kind);
}

// --- Account row ------------------------------------------------------

/** Paint or repaint the contents of one account <li>. Used both as
 *  the spec's mount body (li freshly created, empty) and as update
 *  (li already in DOM, may have stale children). */
function paintAccountRow(li: HTMLElement, a: ConfiguredForge): void {
  li.classList.toggle("forge-account-row-error", !a.connected);

  // Top row: identity + actions.
  const newTop = renderAccountTopRow(a);
  const oldTop = li.querySelector<HTMLElement>(":scope > .forge-account-row-top");
  if (oldTop !== null) {
    oldTop.replaceWith(newTop);
  } else {
    li.appendChild(newTop);
  }

  // Repos details (only when connected and we have repo data).
  const oldDetails = li.querySelector<HTMLElement>(":scope > .forge-account-repos");
  const repos = lastReposByForge[a.id];
  if (a.connected && repos !== undefined) {
    if (oldDetails === null) {
      li.appendChild(buildAccountReposDetails(a, repos));
    } else {
      updateAccountReposDetails(oldDetails, a, repos);
    }
  } else {
    oldDetails?.remove();
  }
}

function renderAccountTopRow(a: ConfiguredForge): HTMLElement {
  const top = document.createElement("div");
  top.className = "forge-account-row-top";

  const id = document.createElement("div");
  id.className = "forge-account-identity";
  const primary = document.createElement("span");
  primary.className = "forge-account-primary";
  const hasEmail = a.email !== undefined && a.email !== "";
  const hasUsername = a.username !== undefined && a.username !== "";
  if (hasEmail || hasUsername) {
    primary.textContent = hasEmail ? a.email! : a.username!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  } else {
    // No identity data yet (this is the first paint right after a
    // PAT submit / OAuth complete; the background probe hasn't
    // populated email or username yet). Show a skeleton bar instead
    // of falling back to the host string — the host is already shown
    // on the meta line below.
    primary.classList.add("skeleton", "forge-account-primary-skeleton");
    primary.setAttribute("aria-label", "Loading account identity…");
  }
  id.appendChild(primary);
  const meta = document.createElement("span");
  meta.className = "forge-account-meta";
  const parts: string[] = [];
  if (hasEmail && hasUsername) {
    parts.push("@" + a.username!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  if (a.host !== "") {
    parts.push(a.host);
  }
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
    const manageLabel = document.createElement("span");
    manageLabel.textContent = "Manage";
    manage.replaceChildren(manageLabel, iconEl(ICON_EXTERNAL));
    manage.setAttribute("data-tooltip", "Manage account on forge");
    manage.setAttribute("aria-label", "Manage account on forge");
    actions.appendChild(manage);
  }

  const out = document.createElement("button");
  out.type = "button";
  out.className = "btn-small btn-danger";
  out.textContent = "Sign out";
  out.addEventListener("click", () => {
    void onSignOut(a);
  });
  signOutUnbinds.push(bindLoadingState("forge.sign_out", out));
  actions.appendChild(out);

  top.appendChild(actions);
  return top;
}

// --- Account repos (details) ------------------------------------------

function buildAccountReposDetails(a: ConfiguredForge, repos: Repo[]): HTMLElement {
  return buildAccountReposDetailsImpl(a, repos, reposRenderDeps);
}

function updateAccountReposDetails(details: HTMLElement, a: ConfiguredForge, repos: Repo[]): void {
  updateAccountReposDetailsImpl(details, a, repos, reposRenderDeps);
}

// --- Repo row ---------------------------------------------------------
// --- Deps wiring for extracted modules --------------------------------

const repoDeps: RepoDeps = {
  isCloned: (name) => lastLocalNames.has(name),
  addCloned: (name) => { lastLocalNames.add(name); },
  removeCloned: (name) => { lastLocalNames.delete(name); },
  bumpState,
};

const reposRenderDeps: ReposRenderDeps = {
  get lastLocalNames() { return lastLocalNames; },
  expandOnNextPaint,
  bumpState,
  repoDeps,
  repoSpec,
};

const oauthDeps: OAuthFlowDeps = {
  setStatus,
  escapeHTML,
  escapeAttr,
  expandOnNextPaint: (id) => { expandOnNextPaint.add(id); },
  renderForgesPanel: () => { void renderForgesPanel(); },
};

const patDeps: PATFormDeps = {
  hostPlaceholder,
  closeSlot,
  addPatFormUnbind: (fn) => { patFormUnbinds.push(fn); },
  expandOnNextPaint: (id) => { expandOnNextPaint.add(id); },
  renderForgesPanel: () => { void renderForgesPanel(); },
};

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
    void startGitHubDeviceFlow(oauthBody, oauthDeps);

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
  renderPATForm(patBody, kind, slot, patDeps);
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


// --- Sign out ---

async function onSignOut(f: ConfiguredForge): Promise<void> {
  const label = f.email ?? f.username ?? f.host;
  const ok = await confirmDialog(
    `Sign out of ${label}? The token will be removed from the CLI config.`,
    "Sign out",
    "destructive",
  );
  if (!ok) {
    return;
  }

  // Optimistic: hide the account row immediately.
  const row = document.querySelector<HTMLElement>(
    `.forge-account-row[data-id="${CSS.escape(f.id)}"]`,
  );
  if (row !== null) {
    row.hidden = true;
  }

  const res = await signOut.dispatch({ forgeId: f.id });
  if (res === null) {
    // Rollback: re-query the DOM since revalidateInBackground may have
    // replaced the original row while the dispatch was in-flight.
    const freshRow = document.querySelector<HTMLElement>(
      `.forge-account-row[data-id="${CSS.escape(f.id)}"]`,
    );
    if (freshRow !== null) {
      freshRow.hidden = false;
    } else {
      void renderForgesPanel({ revalidate: false });
    }
    return;
  }
  void renderForgesPanel();
}

// --- helpers ---

function escapeHTML(s: string): string {
  const map: Record<string, string> = {
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  };
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
