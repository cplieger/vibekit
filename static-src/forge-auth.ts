// ---------------------------------------------------------------------------
// Forge authentication UI.
//
// Layout: four always-visible sections (one per supported forge kind:
// GitHub, GitLab, Codeberg, Gitea). Each section shows, top to bottom:
//
//   - Section header: kind badge, kind name, and the "+" button that
//     opens the add-account pane.
//   - The add-account pane (empty until "+" is clicked): OAuth device
//     flow for GitHub plus a PAT paste form, or the PAT form alone for
//     GitLab/Gitea/Codeberg. It is ABOVE the account list on purpose,
//     so it opens next to the button that asked for it.
//   - Account list: zero or more slim rows. Each row has the email or
//     username, the host, a "Manage" link to the forge's account page,
//     and a "Sign out" button.
//
// Multi-account note: the data model and UI render a list of N
// accounts per kind, but the underlying CLIs (gh, glab) store one
// user per host in their config files. Today, adding a second account
// on the same host replaces the first via the CLI. Across-different-
// hosts works (e.g. github.com + ghe.example.com).
// ---------------------------------------------------------------------------

// signal.aborted / generation-counter defensive guards: same rationale
// as forge-auth-oauth.ts — the value can flip between awaited microtasks
// even though the type system sees it as always-defined boolean.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import { apiGetTyped, apiPost, CancellableSlot } from "./api-client.js";
import type { Decoder } from "./validators.js";
import { asObject, decodeArray } from "./validators.js";
import { decodeRepo } from "./wire/decoders.gen.js";
import { confirm as confirmDialog } from "./confirm.js";
import { FORGE_ICONS, ICON_EXTERNAL, ICON_PLUS_16 } from "./icons.js";
import type { ConfiguredForge, ForgeKind, Repo } from "./wire/types.gen.js";
import { DEFAULT_HOST, FORGE_META, FORGE_URLS, kindTitle } from "./forge-types.js";
import { signOut } from "./actions/forge.js";
import { refreshForges, type ForgesListResponse } from "./forge-store.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { signal, effect, el, touch } from "@cplieger/reactive";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { startGitHubDeviceFlow, abortPoll, type OAuthFlowDeps } from "./forge-auth-oauth.js";
import { renderPATForm, type PATFormDeps } from "./forge-auth-pat.js";
import { getToolsStatus } from "./actions/tools.js";
import { installToolAndWait } from "./tools.js";
import { RollingOutput } from "./modals.js";
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

interface RepoListResponse {
  repos: Repo[];
}
interface LocalReposResponse {
  repos: string[];
}

// --- Response decoders ------------------------------------------------

const decodeRepoListResponse: Decoder<RepoListResponse> = (v) => {
  const o = asObject(v, "$.repo_list");
  return { repos: decodeArray(o["repos"], decodeRepo, "$.repo_list.repos") };
};

const decodeLocalReposResponse: Decoder<LocalReposResponse> = (v) => {
  const o = asObject(v, "$.local_repos");
  return {
    repos: decodeArray(
      o["repos"],
      (el) => {
        if (typeof el !== "string") {
          throw new TypeError(`expected string, got ${typeof el}`);
        }
        return el;
      },
      "$.local_repos.repos",
    ),
  };
};

import { iconEl } from "./icon-el.js";

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
    touch(stateVersion);
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

  // Through the shared store rather than this module's own fetch: a sign-in or a
  // sign-out has to reach the sidebar badge and the PRs tab too, which a private
  // copy could never do. It carries no signal, deliberately — see forge-store.ts:
  // one consumer navigating away must not abort a fetch two others await. The
  // guards below are what make a stale answer harmless here.
  const data = await refreshForges();
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
  const data = await refreshForges();
  if (signal.aborted) {
    return;
  }
  if (data === null || data === undefined) {
    return;
  }
  const [localNames, reposByForge] = await Promise.all([
    refreshLocalNames(signal),
    refreshReposByForge(data.forges, signal),
  ]);
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
  const retryBtn = el("button", { type: "button", className: "btn-small" }, "Retry");
  retryBtn.addEventListener("click", () => {
    void renderForgesPanel();
  });
  const errDiv = el("div", { className: "forge-error" }, "Failed to load forges.", retryBtn);
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
    const li = el("li", { className: "forge-account-row", "data-id": a.id });
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
  const section = el("section", { className: "forge-kind-section", "data-kind": kind });

  // Header: badge + title + add button.
  const badge = el(
    "span",
    { className: `forge-kind-badge forge-kind-${kind}` },
    iconEl(FORGE_ICONS[kind] ?? ""),
  );
  const title = el("h3", { className: "forge-kind-title" }, kindTitle(kind));

  const addBtn = el(
    "button",
    {
      type: "button",
      className: "btn-small forge-kind-add-btn",
      "data-forge-add": kind,
      "aria-label": "Add an account",
      "data-tooltip": "Add an account",
    },
    iconEl(ICON_PLUS_16),
  );
  addBtn.addEventListener("click", () => {
    onAddAccount(kind, section);
  });

  const header = el("header", { className: "forge-kind-header" }, badge, title, addBtn);
  section.appendChild(header);

  // Inline mount point for the add-account pane (OAuth + PAT) and
  // status messages. Non-keyed sibling — survives reconcile.
  //
  // It sits DIRECTLY under the header, above the account list, because
  // the "+" that opens it is in that header. Below the list, the pane
  // opened one account row plus one expanded repo list away from the
  // click that asked for it, so on a kind that already has an account
  // the button read as doing nothing. Same rule the knowledge-base add
  // form follows (`knowledge-list`.before(form)).
  const slot = el("div", { className: "forge-kind-slot", "data-forge-slot": kind });
  section.appendChild(slot);

  // Always present an account list container so reconcile has a
  // deterministic mount point. Empty list renders nothing.
  const list = el("ul", { className: "forge-account-list" });
  section.appendChild(list);
  reconcile(list, accountsForKind(kind), accountSpec);

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
  li.classList.toggle("forge-account-row-missing", a.cli_missing === true);

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
  const cliMissing = a.cli_missing === true;
  const primary = el("span", { className: "forge-account-primary" });
  const hasEmail = a.email !== undefined && a.email !== "";
  const hasUsername = a.username !== undefined && a.username !== "";
  if (hasEmail || hasUsername) {
    primary.textContent = hasEmail ? a.email! : a.username!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  } else if (cliMissing) {
    // Terminal state, not a loading one: the CLI that could resolve
    // the identity is uninstalled (kind-level cli_missing rows carry
    // no username). A skeleton here would shimmer forever.
    primary.textContent = "Saved connection";
  } else {
    // No identity data yet (this is the first paint right after a
    // PAT submit / OAuth complete; the background probe hasn't
    // populated email or username yet). Show a skeleton bar instead
    // of falling back to the host string — the host is already shown
    // on the meta line below.
    primary.classList.add("skeleton", "forge-account-primary-skeleton");
    primary.setAttribute("aria-label", "Loading account identity…");
  }

  const meta = el("span", { className: "forge-account-meta" });
  const parts: string[] = [];
  if (hasEmail && hasUsername) {
    parts.push("@" + a.username!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  if (a.host !== "") {
    parts.push(a.host);
  }
  meta.textContent = parts.join(" · ");

  const id = el("div", { className: "forge-account-identity" }, primary, meta);
  if (!a.connected && a.last_error !== undefined && a.last_error !== "") {
    id.appendChild(el("span", { className: "forge-account-error" }, a.last_error));
  }

  const actions = el("div", { className: "forge-account-actions" });
  if (cliMissing) {
    // No Manage / Sign out: both act through the CLI that is absent.
    // The last_error line (rendered above) carries the reinstall
    // pointer; the row becomes actionable again once the CLI returns
    // and discovery promotes it back to a live account.
    return el("div", { className: "forge-account-row-top" }, id, actions);
  }

  const manageURL = manageAccountURL(a.kind, a.host);
  if (manageURL !== "") {
    const manageLabel = el("span", null, "Manage");
    const manage = el(
      "a",
      {
        href: manageURL,
        target: "_blank",
        rel: "noreferrer",
        className: "btn-small forge-account-manage",
        "data-tooltip": "Manage account on forge",
        "aria-label": "Manage account on forge",
      },
      manageLabel,
      iconEl(ICON_EXTERNAL),
    );
    actions.appendChild(manage);
  }

  const out = el(
    "button",
    { type: "button", className: "btn-small btn-danger" },
    "Sign out",
  ) as HTMLButtonElement;
  out.addEventListener("click", () => {
    void onSignOut(a);
  });
  signOutUnbinds.push(bindLoadingState("forge.sign_out", out));
  actions.appendChild(out);

  return el("div", { className: "forge-account-row-top" }, id, actions);
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
  addCloned: (name) => {
    lastLocalNames.add(name);
  },
  removeCloned: (name) => {
    lastLocalNames.delete(name);
  },
  bumpState,
};

const reposRenderDeps: ReposRenderDeps = {
  get lastLocalNames() {
    return lastLocalNames;
  },
  expandOnNextPaint,
  bumpState,
  repoDeps,
  repoSpec,
};

const oauthDeps: OAuthFlowDeps = {
  setStatus,
  expandOnNextPaint: (id) => {
    expandOnNextPaint.add(id);
  },
  renderForgesPanel: () => {
    void renderForgesPanel();
  },
};

const patDeps: PATFormDeps = {
  hostPlaceholder,
  closeSlot,
  addPatFormUnbind: (fn) => {
    patFormUnbinds.push(fn);
  },
  expandOnNextPaint: (id) => {
    expandOnNextPaint.add(id);
  },
  renderForgesPanel: () => {
    void renderForgesPanel();
  },
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

/** Map a forge kind to the CLI binary it drives. */
const CLI_BY_KIND: Record<ForgeKind, { name: string }> = {
  github: { name: "gh" },
  gitlab: { name: "glab" },
  codeberg: { name: "tea" },
  gitea: { name: "tea" },
};

/**
 * Probe whether the forge CLI for `kind` is installed; if not, prepend
 * an install banner to the add-pane. The banner installs the CLI on
 * click with visible progress. The login forms below stay usable —
 * backend EnsureCLI is the ultimate guarantee — but installing up
 * front avoids a silent stall on submit.
 */
async function gateAddPaneOnCLI(pane: HTMLElement, kind: ForgeKind): Promise<void> {
  const cli = CLI_BY_KIND[kind];
  const status = await getToolsStatus.dispatch();
  if (status !== null && status[cli.name] === true) {
    return; // CLI already present.
  }

  const banner = el("div", { className: "forge-cli-banner inline-install-banner" });
  const msg = el(
    "p",
    { className: "section-hint" },
    `The ${cli.name} CLI powers ${kindTitle(kind)} integration and isn't installed yet. It installs automatically when you sign in, or install it now:`,
  );
  const btn = el(
    "button",
    { type: "button", className: "btn-small" },
    `Install ${cli.name}`,
  ) as HTMLButtonElement;
  const out = el("div", {
    className: "rolling-output hidden",
    role: "log",
    "aria-live": "polite",
    "aria-label": `${cli.name} install progress`,
  }) as HTMLDivElement;

  // Disable the button while the install job runs (auto re-enabled on
  // settle); replaces the manual btn.disabled toggles.
  bindLoadingState("tools.ensure", btn);
  btn.addEventListener("click", () => {
    void (async () => {
      const roll = new RollingOutput(out, "git-output-modal");
      out.classList.remove("hidden");
      roll.append(`Installing ${cli.name}…`);
      const res = await installToolAndWait(cli.name, (line) => {
        roll.append(line);
      });
      if (!res.ok) {
        roll.append(`Install failed${res.error !== undefined ? `: ${res.error}` : ""}`);
        return;
      }
      const after = await getToolsStatus.dispatch();
      if (after !== null && after[cli.name] === true) {
        banner.remove();
      }
    })();
  });

  banner.append(msg, btn, out);
  pane.prepend(banner);
}

function showAddPane(section: HTMLElement, kind: ForgeKind): void {
  const slot = slotOf(section);
  slot.innerHTML = "";

  const pane = el("div", { className: "forge-add-pane" });

  // The forge CLI (gh/glab/tea) is opt-in and installed on demand.
  // Backend EnsureCLI also installs it on login submit, but probing
  // up front lets us show progress instead of a silent ~10s stall
  // when the user clicks Sign in / Save token.
  void gateAddPaneOnCLI(pane, kind);

  // Lead-in text. Phrasing depends on whether OAuth is offered.
  const hasOAuth = oauthByKind[kind] === true;
  const intro = el(
    "p",
    { className: "forge-add-pane-intro" },
    hasOAuth
      ? "Sign in with the one-time browser flow, or paste a personal access token. Either path produces a token the CLI uses for git + API operations."
      : "Paste a personal access token. The CLI uses it for git + API operations.",
  );
  pane.appendChild(intro);

  // For kinds with OAuth: OAuth section on top, divider, PAT below.
  if (hasOAuth) {
    const oauthHeading = el("h4", { className: "forge-add-pane-heading" }, "Browser-based sign-in");
    const oauthBody = el("div", { className: "forge-add-pane-body" });
    const oauth = el("div", { className: "forge-add-pane-section" }, oauthHeading, oauthBody);
    pane.appendChild(oauth);
    void startGitHubDeviceFlow(oauthBody, oauthDeps);

    const divider = el("hr", { className: "forge-add-pane-divider" });
    pane.appendChild(divider);
  }

  // PAT form (works for every kind via the kind-agnostic backend).
  const patHeading = el("h4", { className: "forge-add-pane-heading" }, "Personal access token");
  const patBody = el("div", { className: "forge-add-pane-body" });
  const patSection = el("div", { className: "forge-add-pane-section" }, patHeading, patBody);
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
    const div = el("div");
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

function setStatus(host: HTMLElement, text: string, kind: "ok" | "err" | "" = ""): void {
  host.innerHTML = "";
  const div = el("div", { className: `forge-card-status ${kind}` }, text);
  host.appendChild(div);
}
