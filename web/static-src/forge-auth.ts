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

import { apiDelete, apiGet, apiPost } from "./api-client.js";
import type { ConfiguredForge, ForgeKind } from "./wire/types.gen.js";

interface ForgesListResponse {
  forges: ConfiguredForge[];
  kinds: ForgeKind[];
  oauth?: Partial<Record<ForgeKind, boolean>>;
}

interface DeviceFlowResponse {
  user_code: string;
  verification_uri: string;
  device_code: string;
  interval: number;
  expires_in: number;
}

interface PollResult {
  status: "pending" | "complete" | "expired" | "error";
  error?: string;
}

const ALL_KINDS: readonly ForgeKind[] = ["github", "gitlab", "codeberg", "gitea"];

const PAT_HELP_LINKS: Record<ForgeKind, { url: string; label: string } | null> = {
  github:   { url: "https://github.com/settings/tokens?type=beta", label: "Create a GitHub fine-grained token" },
  gitlab:   { url: "https://gitlab.com/-/profile/personal_access_tokens?scopes=api,read_repository,write_repository", label: "Create a GitLab token" },
  codeberg: { url: "https://codeberg.org/user/settings/applications", label: "Create a Codeberg token" },
  gitea:    { url: "/user/settings/applications", label: "Create a Gitea token (your-host/user/settings/applications)" },
};

const DEFAULT_HOST: Record<ForgeKind, string> = {
  github:   "github.com",
  gitlab:   "gitlab.com",
  codeberg: "codeberg.org",
  gitea:    "",
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

function forgeKindLabel(kind: ForgeKind): string {
  switch (kind) {
    case "github":   return "GitHub";
    case "gitlab":   return "GitLab";
    case "codeberg": return "Codeberg";
    case "gitea":    return "Gitea";
  }
}

/** Render the full forges panel. Idempotent; call after every list
 *  mutation to refresh. */
export async function renderForgesPanel(): Promise<void> {
  const root = document.getElementById("forges-panel");
  if (root === null) return;

  const data = await apiGet<ForgesListResponse>("/api/forges");
  if (data === null) {
    root.innerHTML = `<div class="forge-error">Failed to load forges.</div>`;
    return;
  }

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

  const header = document.createElement("header");
  header.className = "forge-kind-header";
  header.innerHTML =
    `<span class="forge-kind-badge forge-kind-${kind}">${kindBadge(kind)}</span>` +
    `<h3 class="forge-kind-title">${forgeKindLabel(kind)}</h3>`;
  section.appendChild(header);

  const list = document.createElement("ul");
  list.className = "forge-account-list";
  if (accounts.length === 0) {
    const empty = document.createElement("li");
    empty.className = "forge-account-empty";
    empty.textContent = "No accounts connected.";
    list.appendChild(empty);
  } else {
    for (const a of accounts) list.appendChild(renderAccountRow(a));
  }
  section.appendChild(list);

  // Action row: Add account, plus Add PAT for GitHub.
  const actions = document.createElement("div");
  actions.className = "forge-kind-actions";

  const addBtn = document.createElement("button");
  addBtn.type = "button";
  addBtn.className = "btn-small btn-primary";
  addBtn.dataset["forgeAdd"] = kind;
  addBtn.textContent = "+ Add an account";
  addBtn.addEventListener("click", () => { void onAddAccount(kind, section); });
  actions.appendChild(addBtn);

  if (kind === "github") {
    const patBtn = document.createElement("button");
    patBtn.type = "button";
    patBtn.className = "btn-small";
    patBtn.dataset["forgeAddPat"] = kind;
    patBtn.textContent = "+ Add a PAT";
    patBtn.addEventListener("click", () => { showPATForm(section, kind); });
    actions.appendChild(patBtn);
  }

  section.appendChild(actions);

  // Inline mount point for OAuth-flow / PAT-form / status messages.
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

  // Identity column: prefer email, fallback to username; always show host.
  const id = document.createElement("div");
  id.className = "forge-account-identity";
  const primary = document.createElement("span");
  primary.className = "forge-account-primary";
  primary.textContent = a.email ?? a.username ?? a.host;
  id.appendChild(primary);
  const meta = document.createElement("span");
  meta.className = "forge-account-meta";
  const parts: string[] = [];
  if (a.username !== undefined && a.username !== "") parts.push("@" + a.username);
  if (a.host !== "" && a.host !== a.email) parts.push(a.host);
  meta.textContent = parts.join(" · ");
  id.appendChild(meta);
  if (!a.connected && a.last_error !== undefined && a.last_error !== "") {
    const err = document.createElement("span");
    err.className = "forge-account-error";
    err.textContent = a.last_error;
    id.appendChild(err);
  }
  li.appendChild(id);

  // Action column: Manage link (opens forge in new tab) + Sign out.
  const actions = document.createElement("div");
  actions.className = "forge-account-actions";

  const manageURL = manageAccountURL(a.kind, a.host);
  if (manageURL !== "") {
    const manage = document.createElement("a");
    manage.href = manageURL;
    manage.target = "_blank";
    manage.rel = "noreferrer";
    manage.className = "btn-small forge-account-manage";
    manage.textContent = "Manage ↗";
    actions.appendChild(manage);
  }

  const out = document.createElement("button");
  out.type = "button";
  out.className = "btn-small btn-danger";
  out.textContent = "Sign out";
  out.addEventListener("click", () => { void onSignOut(a); });
  actions.appendChild(out);

  li.appendChild(actions);
  return li;
}

// --- Add-account flow dispatch ---

function onAddAccount(kind: ForgeKind, section: HTMLElement): void {
  if (kind === "github") {
    void startGitHubDeviceFlow(slotOf(section));
    return;
  }
  // GitLab / Codeberg / Gitea: PAT is the primary path today.
  showPATForm(section, kind);
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
  const start = await apiPost<DeviceFlowResponse>("/api/forges/oauth/github/start", {});
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
  container.innerHTML =
    `<p>Open <a class="forge-device-link" target="_blank" rel="noreferrer" href="${escapeAttr(start.verification_uri)}">${escapeHTML(start.verification_uri)}</a> and enter:</p>` +
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
  const tick = async (): Promise<void> => {
    const res = await apiPost<PollResult>("/api/forges/oauth/github/poll", { device_code: deviceCode });
    if (res === null) {
      if (statusEl !== null) statusEl.textContent = "Network error. Retrying…";
      return;
    }
    if (res.status === "complete") {
      if (statusEl !== null) statusEl.textContent = "Connected.";
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

function showPATForm(section: HTMLElement, kind: ForgeKind): void {
  const slot = slotOf(section);
  slot.innerHTML = "";
  const body = document.createElement("div");
  body.className = "forge-pat-body";

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
    body.appendChild(help);
  }

  const form = document.createElement("form");
  form.className = "forge-pat-form";

  const hostInput = document.createElement("input");
  hostInput.type = "text";
  hostInput.placeholder = `host (e.g. ${DEFAULT_HOST[kind] === "" ? "your-gitea.example.com" : DEFAULT_HOST[kind]})`;
  hostInput.className = "tool-form-input";
  hostInput.value = DEFAULT_HOST[kind];
  hostInput.required = true;
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
  cancel.addEventListener("click", () => { slot.replaceChildren(); });
  form.appendChild(cancel);

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    void doPATConnect(kind, hostInput.value.trim(), tokenInput.value, status, () => {
      tokenInput.value = "";
      slot.replaceChildren();
      void renderForgesPanel();
    });
  });

  body.appendChild(form);
  slot.appendChild(body);
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
  const id = `${kind}:${host}`;
  const res = await apiPost<{ status?: string; error?: string }>(`/api/forges/${encodeURIComponent(id)}/login/pat`, {
    token,
  });
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
  if (!confirm(`Sign out of ${label}? The token will be removed from the CLI config.`)) return;
  await apiDelete(`/api/forges/${encodeURIComponent(f.id)}`);
  void renderForgesPanel();
}

// --- helpers ---

function kindBadge(kind: ForgeKind): string {
  switch (kind) {
    case "github":   return "GH";
    case "gitlab":   return "GL";
    case "codeberg": return "CB";
    case "gitea":    return "GT";
  }
}

function escapeHTML(s: string): string {
  const map: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return s.replace(/[&<>"']/g, (c) => map[c] ?? c);
}

function escapeAttr(s: string): string {
  return s.replace(/["']/g, (c) => (c === '"' ? "&quot;" : "&#39;"));
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
