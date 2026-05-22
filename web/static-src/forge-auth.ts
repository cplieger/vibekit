// ---------------------------------------------------------------------------
// Forge authentication UI.
//
// Two paths per forge kind:
//   - GitHub: OAuth device flow (vibekit handles the full flow + token
//     injection into ~/.config/gh/hosts.yml)
//   - GitLab/Gitea/Codeberg: PAT paste (UI prompts for a token, vibekit
//     writes it into the corresponding CLI config file)
//
// On success the CLI tools are auto-installed via tools.json and
// `<cli> auth setup-git` runs so all git operations are credential-
// helper authenticated.
// ---------------------------------------------------------------------------

import { apiDelete, apiGet, apiPost } from "./api-client.js";
import type { ConfiguredForge, ForgeKind } from "./wire/types.gen.js";

interface ForgesListResponse {
  forges: ConfiguredForge[];
  kinds: ForgeKind[];
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

interface ProbeResponse {
  connected: boolean;
  error?: string;
  forge?: ConfiguredForge;
}

const PAT_HELP_LINKS: Partial<Record<ForgeKind, { url: string; label: string }>> = {
  gitlab:   { url: "https://gitlab.com/-/profile/personal_access_tokens?scopes=api,read_repository,write_repository", label: "Create a GitLab token" },
  codeberg: { url: "https://codeberg.org/user/settings/applications",                                                  label: "Create a Codeberg token" },
  gitea:    { url: "/user/settings/applications",                                                                       label: "Create a Gitea token (your-host/user/settings/applications)" },
};

function patHelpLink(kind: ForgeKind): { url: string; label: string } | undefined {
  switch (kind) {
    case "gitlab":   return PAT_HELP_LINKS.gitlab;
    case "codeberg": return PAT_HELP_LINKS.codeberg;
    case "gitea":    return PAT_HELP_LINKS.gitea;
  }
  return undefined;
}

/** Render the connected-forges list and connect-buttons. Idempotent;
 *  call after every list mutation to refresh. */
export async function renderForgesPanel(): Promise<void> {
  const root = document.getElementById("forges-panel");
  if (root === null) return;

  const data = await apiGet<ForgesListResponse>("/api/forges");
  if (data === null) {
    root.innerHTML = `<div class="forge-error">Failed to load forges.</div>`;
    return;
  }

  root.innerHTML = "";
  root.appendChild(renderConnectedSection(data.forges));
  root.appendChild(renderConnectSection(data.kinds));
}

/** Render the list of currently-connected forges. */
function renderConnectedSection(forges: ConfiguredForge[]): HTMLElement {
  const section = document.createElement("section");
  section.className = "forge-connected";
  const header = document.createElement("h3");
  header.className = "section-title";
  header.textContent = "Connected forges";
  section.appendChild(header);

  if (forges.length === 0) {
    const empty = document.createElement("div");
    empty.className = "forge-empty";
    empty.textContent = "No forges connected. Connect one below to enable PRs, issues, and CI.";
    section.appendChild(empty);
    return section;
  }

  const list = document.createElement("ul");
  list.className = "forge-list";
  for (const f of forges) {
    list.appendChild(renderConnectedRow(f));
  }
  section.appendChild(list);
  return section;
}

function renderConnectedRow(f: ConfiguredForge): HTMLElement {
  const li = document.createElement("li");
  li.className = "forge-row";
  li.dataset["id"] = f.id;

  const info = document.createElement("div");
  info.className = "forge-row-info";
  info.innerHTML =
    `<span class="forge-kind-badge forge-kind-${f.kind}">${forgeKindLabel(f.kind)}</span>` +
    `<span class="forge-host">${escapeHTML(f.host)}</span>` +
    (f.username !== undefined && f.username !== "" ? `<span class="forge-user">@${escapeHTML(f.username)}</span>` : "") +
    `<span class="forge-status ${f.connected ? "ok" : "err"}">${f.connected ? "Connected" : "Auth failed"}</span>`;
  if (f.last_error !== undefined && f.last_error !== "") {
    info.innerHTML += `<span class="forge-error-msg">${escapeHTML(f.last_error)}</span>`;
  }
  li.appendChild(info);

  const actions = document.createElement("div");
  actions.className = "forge-row-actions";

  const probeBtn = document.createElement("button");
  probeBtn.type = "button";
  probeBtn.className = "btn-small";
  probeBtn.textContent = "Verify";
  probeBtn.addEventListener("click", () => void probeForge(f.id));
  actions.appendChild(probeBtn);

  const removeBtn = document.createElement("button");
  removeBtn.type = "button";
  removeBtn.className = "btn-small btn-danger";
  removeBtn.textContent = "Disconnect";
  removeBtn.addEventListener("click", () => void disconnectForge(f.id));
  actions.appendChild(removeBtn);

  li.appendChild(actions);
  return li;
}

/** Render the "connect a new forge" controls. */
function renderConnectSection(kinds: ForgeKind[]): HTMLElement {
  const section = document.createElement("section");
  section.className = "forge-connect";
  const header = document.createElement("h3");
  header.className = "section-title";
  header.textContent = "Connect a forge";
  section.appendChild(header);

  for (const kind of kinds) {
    section.appendChild(renderConnectCard(kind));
  }
  return section;
}

function renderConnectCard(kind: ForgeKind): HTMLElement {
  const card = document.createElement("div");
  card.className = "forge-card";
  card.dataset["kind"] = kind;

  const header = document.createElement("div");
  header.className = "forge-card-header";
  header.innerHTML =
    `<span class="forge-kind-badge forge-kind-${kind}">${forgeKindLabel(kind)}</span>` +
    `<span class="forge-card-title">${forgeKindLabel(kind)}</span>`;
  card.appendChild(header);

  if (kind === "github") {
    card.appendChild(renderGitHubConnect());
  } else {
    card.appendChild(renderPATConnect(kind));
  }
  return card;
}

/** GitHub: OAuth device-flow button. */
function renderGitHubConnect(): HTMLElement {
  const wrap = document.createElement("div");
  wrap.className = "forge-card-body";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small btn-primary";
  btn.textContent = "Sign in with GitHub";
  btn.addEventListener("click", () => void startGitHubDeviceFlow(wrap));
  wrap.appendChild(btn);
  return wrap;
}

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
    `<div class="forge-device-code"><code>${escapeHTML(start.user_code)}</code><button type="button" class="btn-small forge-copy-btn" data-copy="${escapeAttr(start.user_code)}">Copy</button></div>` +
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

/** GitLab/Codeberg/Gitea: PAT paste form. */
function renderPATConnect(kind: ForgeKind): HTMLElement {
  const body = document.createElement("div");
  body.className = "forge-card-body";

  const helpLink = patHelpLink(kind);
  if (helpLink !== undefined) {
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

  // Host input — required for self-hosted Gitea, defaulted otherwise.
  const hostInput = document.createElement("input");
  hostInput.type = "text";
  hostInput.placeholder = "host (e.g. gitlab.com)";
  hostInput.className = "tool-form-input";
  if (kind === "gitlab") hostInput.value = "gitlab.com";
  else if (kind === "codeberg") hostInput.value = "codeberg.org";
  else hostInput.value = "";
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

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    void doPATConnect(kind, hostInput.value.trim(), tokenInput.value, status, () => {
      tokenInput.value = "";
      void renderForgesPanel();
    });
  });

  body.appendChild(form);
  return body;
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

async function probeForge(id: string): Promise<void> {
  const res = await apiPost<ProbeResponse>(`/api/forges/${encodeURIComponent(id)}/probe`, {});
  if (res === null) return;
  void renderForgesPanel();
}

async function disconnectForge(id: string): Promise<void> {
  if (!confirm("Disconnect this forge? Tokens will be removed from the CLI config.")) return;
  await apiDelete(`/api/forges/${encodeURIComponent(id)}`);
  void renderForgesPanel();
}

// --- helpers --------------------------------------------------------

function forgeKindLabel(kind: ForgeKind): string {
  switch (kind) {
    case "github":   return "GitHub";
    case "gitlab":   return "GitLab";
    case "codeberg": return "Codeberg";
    case "gitea":    return "Gitea";
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
