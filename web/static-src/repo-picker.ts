// ---------------------------------------------------------------------------
// Unified repo picker: one list across every local clone + every remote
// repo accessible through a configured forge credential.
//
// DOM contract (declared in index.html):
//   [data-repo-picker]              container: holds the trigger button
//     [data-repo-picker-trigger]    button showing the current selection
//   <dialog id="repo-picker-dialog">full picker dialog
//     <input [data-repo-picker-search]>   search box
//     <div   [data-repo-picker-chips]>    org filter chips
//     <div   [data-repo-picker-list]>     virtualised list of rows
//
// Modules that need the current selection import getSelectedEntry()
// and/or subscribe via onSelectionChange().
// ---------------------------------------------------------------------------

import { apiGet, apiPost } from "./api-client.js";
import { onSSE } from "./bus.js";
import { relativeTime } from "./files-shared.js";
import { kindTitle, FORGE_META } from "./forge-types.js";
import type { RepoEntry, ForgeKind } from "./forge-types.js";
import { ICON_CHEVRON_DOWN_SM, ICON_GLOBE, ICON_REFRESH, ICON_SPINNER, iconEl } from "./icons.js";
import { setBanner, clearBanner } from "./git-status-banner.js";
import { withAsyncFeedback } from "./async-button.js";
import type { ConfiguredForge, Repo } from "./wire/types.gen.js";

interface ForgesListResponse { forges: ConfiguredForge[] }
interface RepoListResponse { repos: Repo[] }
interface LocalReposResponse { repos: string[] }

/** Canonical check: can this entry talk to a forge API? */
export function hasForgeCredential(e: RepoEntry): boolean {
  return e.forge_id !== undefined && e.forge_id !== "";
}

// --- State (encapsulated in RepoPickerController) ---

class RepoPickerController {
  entries: RepoEntry[] = [];
  selected: RepoEntry | null = null;
  search = "";
  orgFilter = "";
  refetchController: AbortController | null = null;
  readonly selectionListeners = new Set<(e: RepoEntry | null) => void>();
  readonly registryListeners = new Set<(s: RegistrySummary) => void>();
  forgeConnectedCount = 0;
  initialized = false;
}

const ctrl = new RepoPickerController();

/** Snapshot of the current registry, fired by onRegistryChange after
 *  every successful refetch. Lets callers like the git empty-state
 *  component decide which variant to show without re-fetching the
 *  forges list themselves. */
export interface RegistrySummary {
  /** Number of configured forges that returned `connected: true`. */
  forgeConnectedCount: number;
  /** Total entries in the unified registry (local + remote). */
  entryCount: number;
  /** Number of entries that are local clones (have local_path). */
  localRepoCount: number;
}

function buildRegistrySummary(): RegistrySummary {
  let local = 0;
  for (const e of ctrl.entries) if (e.is_local === true) local++;
  return {
    forgeConnectedCount: ctrl.forgeConnectedCount,
    entryCount: ctrl.entries.length,
    localRepoCount: local,
  };
}

function notifyRegistryListeners(): void {
  const summary = buildRegistrySummary();
  for (const fn of ctrl.registryListeners) fn(summary);
}

// --- Public API ---

/** Initialise the picker. Safe to call multiple times; subsequent calls
 *  are no-ops. Wires the trigger + dialog events and does the first
 *  fetch. */
export function initRepoPicker(): void {
  if (ctrl.initialized) return;
  ctrl.initialized = true;
  wireTrigger();
  wireDialog();
  onSSE("forges_changed", () => { void refetch(); });
  void refetch();
}

/** Return the currently-selected entry (or null if none). Consumers
 *  like git.ts read this to decide which repo to operate on. */
export function getSelectedEntry(): RepoEntry | null {
  return ctrl.selected;
}

/** Subscribe to selection changes. Fires once immediately with the
 *  current value so callers don't have to bootstrap their UI
 *  separately. Returns an unsubscribe function. */
export function onSelectionChange(fn: (e: RepoEntry | null) => void): () => void {
  ctrl.selectionListeners.add(fn);
  fn(ctrl.selected);
  return (): void => { ctrl.selectionListeners.delete(fn); };
}

/** Subscribe to registry changes (after every refetch). Fires once
 *  immediately with the current summary so callers don't need to
 *  bootstrap separately. Returns an unsubscribe function. */
export function onRegistryChange(fn: (s: RegistrySummary) => void): () => void {
  ctrl.registryListeners.add(fn);
  fn(buildRegistrySummary());
  return (): void => { ctrl.registryListeners.delete(fn); };
}

/** Return a snapshot of the current registry — entries, forge count,
 *  local-clone count. Synchronous; caller may also subscribe via
 *  onRegistryChange to be notified of future updates. */
export function getRegistrySummary(): RegistrySummary {
  return buildRegistrySummary();
}

/** Programmatically set the selection by entry id. No-op if the id
 *  isn't in the current cache (e.g. a saved selection from a previous
 *  session whose repo has since been removed). */
export function selectById(id: string): void {
  const match = ctrl.entries.find((e) => e.id === id);
  if (match === undefined) return;
  setSelection(match);
}

/** Open the picker dialog. Exposed so other modules (e.g. an empty
 *  state "Choose a repo" button in the git panel) can trigger the
 *  same UI. */
export function openPickerDialog(): void {
  const dlg = getDialog();
  if (dlg === null) return;
  // Reset filters so the user sees the full list every time they open
  // the picker. Selected-entry highlight still floats to the top.
  ctrl.search = "";
  ctrl.orgFilter = "";
  const input = dlg.querySelector<HTMLInputElement>("[data-repo-picker-search]");
  if (input !== null) input.value = "";
  renderList();
  renderChips();
  dlg.showModal();
  input?.focus();
}

/** Force-refresh the registry from the server. Useful after a forge
 *  credential is added / removed — the Settings panel already calls
 *  this implicitly via the forges_changed SSE. */
async function refreshRepos(): Promise<void> {
  await apiPost("/api/forges/refresh", {});
  await refetch();
}

// --- Fetch ---

/** Builds the unified RepoEntry list by merging:
 *   - /api/git/repos             — local clones (names only)
 *   - /api/forges                — list configured forges
 *   - /api/forges/{id}/repos     — remote repos per forge
 *
 *  Same repo seen locally + remotely collapses to one row.
 */
async function refetch(): Promise<void> {
  ctrl.refetchController?.abort();
  ctrl.refetchController = new AbortController();
  const { signal } = ctrl.refetchController;

  // 1. Local clones.
  const local = await apiGet<LocalReposResponse>("/api/git/repos", signal);
  if (signal.aborted) return;
  const localNames = local?.repos ?? [];

  // 2. Configured forges.
  const forgesRes = await apiGet<ForgesListResponse>("/api/forges", signal);
  if (signal.aborted) return;
  const forges = (forgesRes?.forges ?? []).filter((f) => f.connected);
  ctrl.forgeConnectedCount = forges.length;

  // 3. Remote repos per forge (parallel).
  const remoteByForge = await Promise.all(
    forges.map((f) =>
      apiGet<RepoListResponse>(`/api/forges/${encodeURIComponent(f.id)}/repos`, signal)
        .then((r) => ({ forge: f, repos: r?.repos ?? [] }))
        .catch(() => ({ forge: f, repos: [] as Repo[] }))
    ),
  );
  if (signal.aborted) return;

  // Merge into RepoEntry list.
  const byID = new Map<string, RepoEntry>();
  for (const { forge, repos } of remoteByForge) {
    for (const r of repos) {
      const id = (`${forge.host}:${r.full_name}`).toLowerCase();
      const entry: RepoEntry = {
        id,
        kind: forge.kind,
        host: forge.host,
        owner: r.owner,
        name: r.name,
        full_name: r.full_name,
        is_remote: true,
        forge_id: forge.id,
      };
      if (r.default_branch !== undefined) entry.default_branch = r.default_branch;
      if (r.url !== undefined) entry.url = r.url;
      if (r.clone_url !== undefined) entry.clone_url = r.clone_url;
      if (r.description !== undefined) entry.description = r.description;
      if (r.private !== undefined) entry.private = r.private;
      if (r.archived !== undefined) entry.archived = r.archived;
      if (r.fork !== undefined) entry.fork = r.fork;
      if (r.updated_at !== undefined) entry.updated_at = r.updated_at;
      byID.set(id, entry);
    }
  }
  // Layer in local clones (set is_local=true; create entries for
  // local-only repos that aren't on any forge).
  for (const name of localNames) {
    let matched = false;
    for (const e of byID.values()) {
      if (e.name === name || e.full_name.endsWith("/" + name)) {
        e.is_local = true;
        e.local_path = name;
        matched = true;
        break;
      }
    }
    if (!matched) {
      const id = `local:${name}`;
      byID.set(id, {
        id,
        host: "",
        owner: "",
        name,
        full_name: name,
        is_local: true,
        local_path: name,
      });
    }
  }
  ctrl.entries = Array.from(byID.values());
  // Sort: local first, then by updated_at desc, then alphabetically.
  ctrl.entries.sort((a, b) => {
    if ((a.is_local ?? false) !== (b.is_local ?? false)) return (a.is_local ?? false) ? -1 : 1;
    const tA = a.updated_at ?? 0;
    const tB = b.updated_at ?? 0;
    if (tA !== tB) return tB - tA;
    return a.full_name.localeCompare(b.full_name);
  });
  updateForgesBanner();
  // Repoint the current selection by id so we keep showing the same
  // repo even if its ordering shifted. If the repo disappeared, fall
  // back to the first cloned entry, then the first entry overall,
  // then null.
  if (ctrl.selected !== null) {
    const match = ctrl.entries.find((e) => e.id === ctrl.selected?.id);
    if (match !== undefined) {
      setSelection(match, { silent: true });
    } else {
      setSelection(defaultSelection(), { silent: false });
    }
  } else {
    setSelection(defaultSelection(), { silent: false });
  }
  renderTrigger();
  renderList();
  renderChips();
  notifyRegistryListeners();
}

/** Push or clear the "no forge connected" banner state based on
 *  whether any registry entry carries a forge credential. Source of
 *  truth for the forges-not-connected banner key; the unified
 *  git-status-banner module decides priority + render. */
function updateForgesBanner(): void {
  const anyCredentialled = ctrl.entries.some((e) => e.forge_id !== undefined && e.forge_id !== "");
  // No entries at all → suppress; the empty-state UI (PR 2) will own
  // that state. We only fire forges-not-connected when the user has
  // local clones but no forge auth, since that's the case where PR /
  // CI features go missing.
  if (anyCredentialled || ctrl.entries.length === 0) {
    clearBanner("forges-not-connected");
  } else {
    setBanner("forges-not-connected");
  }
}

function defaultSelection(): RepoEntry | null {
  return ctrl.entries.find((e) => e.is_local === true) ?? ctrl.entries[0] ?? null;
}

function setSelection(e: RepoEntry | null, opts: { silent?: boolean } = {}): void {
  ctrl.selected = e;
  renderTrigger();
  if (opts.silent !== true) {
    for (const fn of ctrl.selectionListeners) fn(e);
  }
}

// --- Trigger button ---

function wireTrigger(): void {
  const trigger = document.querySelector<HTMLButtonElement>("[data-repo-picker-trigger]");
  if (trigger === null) return;
  trigger.addEventListener("click", openPickerDialog);
}

function renderTrigger(): void {
  const trigger = document.querySelector<HTMLButtonElement>("[data-repo-picker-trigger]");
  if (trigger === null) return;
  trigger.replaceChildren();

  if (ctrl.selected === null) {
    trigger.appendChild(textSpan("repo-picker-trigger-empty", "Choose a repository"));
    trigger.appendChild(chevronIcon());
    return;
  }

  const badge = forgeBadge(ctrl.selected.kind);
  if (badge !== null) trigger.appendChild(badge);

  const stack = document.createElement("span");
  stack.className = "repo-picker-trigger-stack";
  stack.appendChild(textSpan("repo-picker-trigger-name", ctrl.selected.full_name || ctrl.selected.name));
  const secondary = triggerSecondary(ctrl.selected);
  if (secondary !== "") stack.appendChild(textSpan("repo-picker-trigger-meta", secondary));
  trigger.appendChild(stack);

  const glyph = stateGlyph(ctrl.selected);
  if (glyph !== null) trigger.appendChild(glyph);
  trigger.appendChild(chevronIcon());
}

function triggerSecondary(e: RepoEntry): string {
  const parts: string[] = [];
  if (e.host !== "") parts.push(e.host);
  return parts.join(" · ");
}

// --- Dialog ---

function getDialog(): HTMLDialogElement | null {
  return document.getElementById("repo-picker-dialog") as HTMLDialogElement | null;
}

function wireDialog(): void {
  const dlg = getDialog();
  if (dlg === null) return;
  const input = dlg.querySelector<HTMLInputElement>("[data-repo-picker-search]");
  input?.addEventListener("input", () => {
    ctrl.search = input.value.trim().toLowerCase();
    renderList();
  });
  input?.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      dlg.close();
      e.preventDefault();
    }
    if (e.key === "Enter") {
      const first = filtered()[0];
      if (first !== undefined) {
        pick(first);
        e.preventDefault();
      }
    }
  });
  // Close on backdrop click.
  dlg.addEventListener("click", (e) => {
    if (e.target === dlg) dlg.close();
  });
  // Close button inside the dialog header.
  const closeBtn = dlg.querySelector<HTMLButtonElement>("[data-repo-picker-close]");
  closeBtn?.addEventListener("click", () => dlg.close());
  // Refresh button — forces a server-side refresh, useful when a
  // freshly-created repo on the forge hasn't shown up yet. Renders
  // ICON_REFRESH; the HTML element is empty until we paint it here so
  // the icon-btn class can size it.
  const refreshBtn = dlg.querySelector<HTMLButtonElement>("[data-repo-picker-refresh]");
  if (refreshBtn !== null) {
    refreshBtn.innerHTML = ICON_REFRESH;
    refreshBtn.addEventListener("click", () => {
      refreshBtn.disabled = true;
      void refreshRepos().finally(() => { refreshBtn.disabled = false; });
    });
  }

  // Clone all button — clone every remote-only entry that has a
  // clone_url, sequentially to avoid overwhelming the workspace's
  // git clone capacity and to keep failures attributable. Disabled
  // when there are no clonable entries (toggled in renderList()).
  const cloneAllBtn = dlg.querySelector<HTMLButtonElement>("[data-repo-picker-clone-all]");
  cloneAllBtn?.addEventListener("click", () => {
    void cloneAllRemoteOnly(cloneAllBtn);
  });
  refreshCloneAllState();
}

/** Enable/disable the Clone all button based on whether there are
 *  any remote-only entries left to clone. Called from renderList()
 *  so the button state stays in sync with the list. */
function refreshCloneAllState(): void {
  const btn = document.querySelector<HTMLButtonElement>("[data-repo-picker-clone-all]");
  if (btn === null) return;
  const candidates = remoteOnlyClonable();
  btn.disabled = candidates.length === 0;
  btn.title = candidates.length === 0
    ? "No remote-only repos to clone"
    : `Clone ${candidates.length} repo${candidates.length === 1 ? "" : "s"}`;
}

/** All remote-only entries with a clone_url — the universe Clone all
 *  operates on. */
function remoteOnlyClonable(): RepoEntry[] {
  return ctrl.entries.filter(
    (e) => e.is_local !== true && typeof e.clone_url === "string" && e.clone_url !== "",
  );
}

/** Clone every remote-only entry sequentially. Each row gets a
 *  spinner up front; we run the clone API calls one at a time
 *  (avoids fighting for the same workdir + makes failures
 *  attributable). After all calls complete we do a single refetch
 *  and trust the resulting `is_local` flag as the authoritative
 *  success signal — apiPost can return an apparent error even when
 *  the clone landed (transient stderr noise). Rows that didn't
 *  end up cloned get marked "Clone failed" with a tooltip. */
async function cloneAllRemoteOnly(btn: HTMLButtonElement): Promise<void> {
  const candidates = remoteOnlyClonable();
  if (candidates.length === 0) return;
  const originalLabel = btn.textContent ?? "Clone all";
  btn.disabled = true;

  // Phase 1: paint a spinner on every candidate row up front so the
  // user sees that ALL of them are queued, not just the active one.
  for (const entry of candidates) {
    const row = findRowById(entry.id);
    if (row !== null) markRowCloning(row);
  }

  // Phase 2: serial clones, capturing any apparent errors per id.
  const errorById = new Map<string, string>();
  let done = 0;
  for (const entry of candidates) {
    btn.textContent = `Cloning ${done + 1}/${candidates.length}…`;
    const row = findRowById(entry.id) ?? document.createElement("div");
    try {
      // skipRefetch + skipPick: the batch handles refetch once at
      // the end. The spinner stays visible the whole time.
      await cloneAndSelect(entry, row, { skipPick: true, skipRefetch: true });
    } catch (err) {
      errorById.set(entry.id, err instanceof Error ? err.message : String(err));
    }
    done++;
  }

  // Phase 3: single authoritative refetch + per-row status check.
  // After this call the rows are re-rendered fresh, so we re-locate
  // by id and only stamp errors on the ones that didn't land.
  await refetch();
  let failed = 0;
  for (const entry of candidates) {
    const fresh = ctrl.entries.find((x) => x.id === entry.id);
    if (fresh?.is_local === true) continue; // success, row already shows synced
    const row = findRowById(entry.id);
    if (row !== null) markRowCloneFailed(row, errorById.get(entry.id) ?? "clone failed");
    failed++;
  }

  btn.textContent = originalLabel;
  refreshCloneAllState();
  btn.title = failed > 0
    ? `Cloned ${candidates.length - failed}, ${failed} failed`
    : `Cloned ${candidates.length} repo${candidates.length === 1 ? "" : "s"}`;
}

/** Tiny CSS.escape polyfill — we only use it to safely build a
 *  selector around a RepoEntry id, which has limited charset. Not
 *  exhaustive; just covers the chars our IDs may contain. */
function cssEscape(s: string): string {
  return s.replace(/[\s"'\\#.>+~*[\]:=]/g, "\\$&");
}

function renderChips(): void {
  const container = document.querySelector<HTMLDivElement>("[data-repo-picker-chips]");
  if (container === null) return;
  const owners = Array.from(new Set(ctrl.entries.map((e) => `${e.host}/${e.owner}`)))
    .filter((k) => k !== "/").sort();
  container.replaceChildren();
  if (owners.length <= 1) return; // no point showing a single-chip row

  const all = document.createElement("button");
  all.type = "button";
  all.className = `repo-picker-chip${ctrl.orgFilter === "" ? " repo-picker-chip-active" : ""}`;
  all.textContent = "All";
  all.addEventListener("click", () => { ctrl.orgFilter = ""; renderChips(); renderList(); });
  container.appendChild(all);

  for (const key of owners) {
    const chip = document.createElement("button");
    chip.type = "button";
    chip.className = `repo-picker-chip${ctrl.orgFilter === key ? " repo-picker-chip-active" : ""}`;
    chip.textContent = key;
    chip.addEventListener("click", () => {
      ctrl.orgFilter = ctrl.orgFilter === key ? "" : key;
      renderChips();
      renderList();
    });
    container.appendChild(chip);
  }
}

function renderList(): void {
  const list = document.querySelector<HTMLDivElement>("[data-repo-picker-list]");
  if (list === null) return;
  const rows = filtered();
  list.replaceChildren();
  if (rows.length === 0) {
    const empty = document.createElement("div");
    empty.className = "repo-picker-empty";
    empty.textContent = ctrl.entries.length === 0
      ? "No repositories yet. Add a forge credential in Settings → Git, or clone a repo into the workspace."
      : "No repositories match your filters.";
    list.appendChild(empty);
    refreshCloneAllState();
    return;
  }
  for (const e of rows) list.appendChild(buildRow(e));
  refreshCloneAllState();
}

/** @internal Exported for unit testing. */
export function filtered(): RepoEntry[] {
  const haystack = ctrl.entries.filter((e) => {
    if (ctrl.orgFilter !== "" && `${e.host}/${e.owner}` !== ctrl.orgFilter) return false;
    if (ctrl.search === "") return true;
    if (e.full_name.toLowerCase().includes(ctrl.search)) return true;
    if (e.host.toLowerCase().includes(ctrl.search)) return true;
    return false;
  });
  // Same ordering rule as the server: cloned entries first, then by
  // recency. Server already returns them sorted, so we just filter.
  return haystack;
}

function buildRow(e: RepoEntry): HTMLElement {
  const row = document.createElement("button");
  row.type = "button";
  row.className = "repo-picker-row";
  if (ctrl.selected !== null && ctrl.selected.id === e.id) {
    row.classList.add("repo-picker-row-selected");
  }

  const badge = forgeBadge(e.kind);
  if (badge !== null) row.appendChild(badge);

  const stack = document.createElement("span");
  stack.className = "repo-picker-row-stack";
  const line1 = document.createElement("span");
  line1.className = "repo-picker-row-name";
  line1.textContent = e.full_name || e.name || e.local_path || "(unnamed)";
  stack.appendChild(line1);
  const line2 = document.createElement("span");
  line2.className = "repo-picker-row-meta";
  line2.textContent = rowSecondary(e);
  stack.appendChild(line2);
  row.appendChild(stack);

  // Right side: state + age + action buttons.
  const right = document.createElement("span");
  right.className = "repo-picker-row-right";
  if (e.updated_at !== undefined && e.updated_at > 0) {
    right.appendChild(textSpan("repo-picker-row-age", relativeTime(e.updated_at)));
  }
  const glyph = stateGlyph(e);
  if (glyph !== null) right.appendChild(glyph);

  // Inline Clone affordance for remote-only entries. The row body is
  // still clickable as a fallback, but a visible button is the primary
  // path so users don't have to guess that "click row" means "clone".
  if (e.is_local !== true && typeof e.clone_url === "string" && e.clone_url !== "") {
    const cloneBtn = document.createElement("button");
    cloneBtn.type = "button";
    cloneBtn.className = "btn-small repo-picker-row-clone-btn";
    cloneBtn.textContent = "Clone";
    cloneBtn.dataset["repoPickerCloneBtn"] = e.id;
    cloneBtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      void withAsyncFeedback(cloneBtn, () => cloneAndSelect(e, row), { keepLabel: true });
    });
    right.appendChild(cloneBtn);
  }
  row.appendChild(right);

  row.addEventListener("click", () => {
    if (e.is_local !== true && e.clone_url !== undefined) {
      // Remote-only: clone into workspace before selecting. Re-uses
      // the row body as the visible target for feedback (so the
      // existing repo-picker-row-cloning class still applies); the
      // dedicated Clone button gets its own withAsyncFeedback above.
      void cloneAndSelect(e, row).catch(() => undefined);
      return;
    }
    pick(e);
  });
  return row;
}

function rowSecondary(e: RepoEntry): string {
  const parts: string[] = [];
  if (e.is_local === true && e.is_remote !== true) {
    parts.push("local only");
  }
  if (e.host !== "") parts.push(e.host);
  if (e.archived === true) parts.push("archived");
  if (e.private === true) parts.push("private");
  return parts.join(" · ");
}

async function cloneAndSelect(
  e: RepoEntry,
  row: HTMLElement,
  opts: { skipPick?: boolean; skipRefetch?: boolean } = {},
): Promise<void> {
  if (e.clone_url === undefined || e.clone_url === "") {
    return;
  }
  // Guard: if a clone is already in flight for this row, ignore the
  // duplicate trigger (e.g. user clicks both the Clone button and the
  // row body, or clicks Clone twice rapidly).
  if (row.classList.contains("repo-picker-row-cloning")) return;
  markRowCloning(row);

  // Clone via the local git endpoint (credential helper is now
  // configured globally by each forge CLI's setup-git, so plain
  // git clone works for private repos).
  //
  // We capture any apparent error from the API response but do NOT
  // immediately surface it. `git clone` can spit non-empty stderr
  // (warnings, credential helper noise) that gets routed into our
  // error field even when the clone has actually landed on disk.
  // The truth is whether the entry shows up as is_local on the
  // next /api/git/repos refresh; trust that over the API's view.
  let cloneError: string | null = null;
  try {
    const res = await apiPost<{ output?: string; error?: string }>(
      `/api/git/clone`,
      { url: e.clone_url },
    );
    if (res === null) {
      cloneError = "network error";
    } else if (res.error !== undefined && res.error !== "") {
      cloneError = res.error;
    }
  } catch (err) {
    cloneError = err instanceof Error ? err.message : String(err);
  }

  if (opts.skipRefetch === true) {
    // Caller (e.g. cloneAllRemoteOnly batch) will refetch + check
    // status itself after all clones complete. We stay in cloning
    // (spinner) state until then. Surface any apparent error so
    // the batch can record it per-id.
    if (cloneError !== null) throw new Error(cloneError);
    return;
  }

  // Refetch + verify the entry actually landed locally. This is the
  // authoritative success check: api response can be misleading.
  await refetch();
  const fresh = ctrl.entries.find((x) => x.id === e.id);
  const actuallyCloned = fresh?.is_local === true;

  // refetch() re-renders all rows; find the fresh element for this
  // entry to layer post-clone state on.
  const newRow = findRowById(e.id);

  if (!actuallyCloned) {
    if (newRow !== null) markRowCloneFailed(newRow, cloneError ?? "clone failed");
    throw new Error(cloneError ?? "clone failed");
  }

  // Success — the row already re-rendered with the synced (green)
  // glyph via stateGlyph(). Pick if requested.
  if (opts.skipPick !== true && fresh !== undefined) pick(fresh);
}

/** Replace the row's age column with a spinner and mark the row as
 *  cloning. Idempotent. */
function markRowCloning(row: HTMLElement): void {
  row.classList.add("repo-picker-row-cloning");
  const ageEl = row.querySelector<HTMLSpanElement>(".repo-picker-row-age");
  if (ageEl !== null) {
    ageEl.innerHTML = ICON_SPINNER;
    ageEl.classList.add("repo-picker-row-spinner");
    ageEl.removeAttribute("title");
  } else {
    // No age element yet — append a spinner to the right region so
    // the user still sees an in-flight indicator.
    const right = row.querySelector<HTMLElement>(".repo-picker-row-right");
    if (right !== null) {
      const sp = document.createElement("span");
      sp.className = "repo-picker-row-age repo-picker-row-spinner";
      sp.innerHTML = ICON_SPINNER;
      right.prepend(sp);
    }
  }
}

/** After refetch we know the entry didn't land locally — show
 *  "Clone failed" with a tooltip carrying the actual error. */
function markRowCloneFailed(row: HTMLElement, msg: string): void {
  row.classList.remove("repo-picker-row-cloning");
  const ageEl = row.querySelector<HTMLSpanElement>(".repo-picker-row-age");
  if (ageEl !== null) {
    ageEl.classList.remove("repo-picker-row-spinner");
    ageEl.textContent = "Clone failed";
    ageEl.title = msg;
  }
}

/** Find a freshly-rendered row by entry id. After refetch() the
 *  previous row reference is stale; we re-locate by stable id via
 *  the per-row Clone button's data attribute (rows that have been
 *  cloned no longer carry one — for those the row already shows the
 *  synced glyph and we don't need to touch it). */
function findRowById(id: string): HTMLElement | null {
  const btn = document.querySelector<HTMLElement>(
    `[data-repo-picker-clone-btn="${cssEscape(id)}"]`,
  );
  return btn?.closest<HTMLElement>(".repo-picker-row") ?? null;
}

function pick(e: RepoEntry): void {
  setSelection(e);
  const dlg = getDialog();
  dlg?.close();
}

// --- Icons + small DOM helpers ---

function textSpan(cls: string, text: string): HTMLSpanElement {
  const s = document.createElement("span");
  s.className = cls;
  s.textContent = text;
  return s;
}

function forgeBadge(kind: ForgeKind | undefined): HTMLSpanElement | null {
  if (kind === undefined) return null;
  const b = document.createElement("span");
  b.className = `repo-picker-badge repo-picker-badge-${kind}`;
  b.innerHTML = FORGE_META[kind].icon;
  b.setAttribute("aria-label", kindTitle(kind));
  return b;
}

/** @internal Exported for unit testing. */
export function badgeGlyph(kind: ForgeKind): string {
  return FORGE_META[kind].badge;
}

/** @internal Exported for unit testing. */
export function stateGlyph(e: RepoEntry): HTMLSpanElement | null {
  if (e.is_local === true && e.is_remote === true) {
    return iconSpan("repo-picker-state repo-picker-state-synced", "●", "Cloned and tracked");
  }
  if (e.is_local === true) {
    return iconSpan("repo-picker-state repo-picker-state-local", "📁", "Local only");
  }
  if (e.is_remote === true) {
    // Striped globe — "remote, not cloned" should read as
    // "lives on the web, hasn't landed locally yet". The previous
    // cloud glyph was a font emoji that rendered inconsistently
    // across platforms.
    const span = document.createElement("span");
    span.className = "repo-picker-state repo-picker-state-remote";
    span.innerHTML = ICON_GLOBE;
    span.setAttribute("aria-label", "Remote, not cloned");
    span.setAttribute("title", "Remote, not cloned");
    return span;
  }
  return null;
}

function iconSpan(cls: string, glyph: string, title: string): HTMLSpanElement {
  const s = document.createElement("span");
  s.className = cls;
  s.textContent = glyph;
  s.setAttribute("aria-label", title);
  s.setAttribute("title", title);
  return s;
}

function chevronIcon(): HTMLSpanElement {
  const s = document.createElement("span");
  s.className = "repo-picker-chevron";
  s.appendChild(iconEl(ICON_CHEVRON_DOWN_SM));
  s.setAttribute("aria-hidden", "true");
  return s;
}

// --- Test helpers (used by repo-picker.test.ts) ---

/** @internal Set controller entries for testing filtered(). */
export function __testSetEntries(entries: RepoEntry[]): void { ctrl.entries = entries; }
/** @internal Set controller search for testing filtered(). */
export function __testSetSearch(s: string): void { ctrl.search = s; }
/** @internal Set controller orgFilter for testing filtered(). */
export function __testSetOrgFilter(f: string): void { ctrl.orgFilter = f; }
/** @internal Build a single row for testing the Clone button affordance. */
export function __testBuildRow(e: RepoEntry): HTMLElement { return buildRow(e); }
/** @internal Expose remoteOnlyClonable() to tests. */
export function __testRemoteOnlyClonable(): RepoEntry[] { return remoteOnlyClonable(); }
