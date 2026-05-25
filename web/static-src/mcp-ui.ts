// ---------------------------------------------------------------------------
// MCP UI: section scaffold, server list rendering, row sub-components, init.
// ---------------------------------------------------------------------------

import { $, el } from "./dom.js";
import { onSSE } from "./bus.js";
import { closeModal } from "./modals.js";
import { confirm as confirmDialog } from "./confirm.js";
import { ICON_EDIT_14, ICON_TRASH_14, ICON_PLUS_16 } from "./icons.js";
import {
  type Server, type RuntimeStatus, type RuntimeState,
  configured, status, refetchServers, refetchStatus, setRenderCallback,
  setStatus, deleteStatus,
} from "./mcp-state.js";
import { type AddMode, setEditing, initModal, cleanupModal } from "./mcp-panels.js";
import { extractNpxPackage } from "./mcp-panels.js";
import { toggleServer, deleteServer, openEdit } from "./actions/mcp.js";

// --- Section scaffold ---

let sectionBody: HTMLDivElement | null = null;

function buildSectionScaffold(): void {
  const section = document.getElementById("mcp-section");
  if (section === null) return;

  const title = document.createElement("h3");
  title.className = "section-title";
  title.textContent = "MCP integrations";

  const hint = document.createElement("p");
  hint.className = "section-hint";
  hint.textContent =
    "Connect your agent to external systems: GitHub, Linear, Postgres, Sentry, or anything else that speaks the Model Context Protocol. Configuration changes apply on the next new chat. Disabled servers are kept on disk but don't consume context tokens or spawn subprocesses. For servers that need a manual install (pip, binary), add them in the Installed tools section above first (category: MCP server), then fill in credentials here.";

  const actionRow = document.createElement("div");
  actionRow.className = "section-actions-row";

  const actions = document.createElement("div");
  actions.className = "action-bar action-bar-inline";

  const addBtn = document.createElement("button");
  addBtn.type = "button";
  addBtn.className = "action-pill";
  addBtn.setAttribute("data-tooltip", "Connect integration");
  addBtn.setAttribute("aria-label", "Connect integration");
  addBtn.innerHTML = ICON_PLUS_16;
  addBtn.addEventListener("click", () => openAddModal());
  actions.appendChild(addBtn);

  actionRow.appendChild(actions);

  sectionBody = document.createElement("div");
  sectionBody.className = "mcp-server-list";

  section.replaceChildren(title, hint, actionRow, sectionBody);
}

// --- Section render ---

function renderSection(): void {
  if (sectionBody === null) return;
  renderRows();
}

function renderRows(): void {
  if (sectionBody === null) return;
  sectionBody.replaceChildren();

  if (configured.length === 0) {
    const empty = document.createElement("p");
    empty.className = "mcp-empty";
    empty.textContent = "No integrations connected yet. Click + to search the official MCP registry or paste a config.";
    sectionBody.appendChild(empty);
    return;
  }

  for (const s of configured) sectionBody.appendChild(renderRow(s));
}

function renderRow(s: Server): HTMLDivElement {
  const st = status.get(s.name);
  const row = document.createElement("div");
  row.className = "mcp-row";
  row.dataset["serverId"] = s.id;

  const toggle = renderEnableToggle(s);
  const body = document.createElement("div");
  body.className = "mcp-row-body";

  const nameLine = document.createElement("div");
  nameLine.className = "mcp-row-name-line";
  const name = document.createElement("span");
  name.className = "mcp-row-name";
  name.textContent = s.name;
  const dot = renderStatusDot(s, st);
  const transportBadge = document.createElement("span");
  transportBadge.className = `mcp-transport mcp-transport-${s.transport}`;
  transportBadge.textContent = s.transport;
  nameLine.append(dot, name, transportBadge);

  const meta = document.createElement("div");
  meta.className = "mcp-row-meta";
  meta.textContent = renderMeta(s, st);

  body.append(nameLine, meta);

  if (st?.state === "needs_auth") {
    body.appendChild(renderOAuthPill(st.oauth_url));
  }

  const actions = document.createElement("div");
  actions.className = "mcp-row-actions";
  actions.append(renderEditBtn(s), renderDeleteBtn(s));

  row.append(toggle, body, actions);
  return row;
}

// --- Row sub-components ---

const STATUS_META: Readonly<Record<RuntimeState, { css: string; title: string }>> = {
  connected:  { css: "connected",  title: "Connected" },
  needs_auth: { css: "oauth",      title: "Needs authentication" },
  idle:       { css: "idle",        title: "Not yet connected — start a chat to initialise" },
  failed:     { css: "failed",      title: "Failed to initialise" },
};

function renderStatusDot(s: Server, st: RuntimeStatus | undefined): HTMLSpanElement {
  const dot = document.createElement("span");
  dot.className = "mcp-dot";
  if (!s.enabled) {
    dot.classList.add("disabled");
    dot.title = "Disabled";
  } else if (st === undefined) {
    dot.classList.add("idle");
    dot.title = "Not yet connected — start a chat to initialise";
  } else {
    const meta = STATUS_META[st.state] ?? STATUS_META.idle;
    dot.classList.add(meta.css);
    if (st.state === "failed" && st.error !== "") {
      dot.title = `Failed to initialise: ${st.error}`;
    } else {
      dot.title = meta.title;
    }
  }
  return dot;
}

function renderMeta(s: Server, st: RuntimeStatus | undefined): string {
  if (!s.enabled) return "Disabled";
  const origin = s.transport === "stdio"
    ? (s.command ?? "")
    : (s.url ?? "");
  if (st?.state === "failed" && st.error !== "") {
    return `${origin} — ${st.error}`;
  }
  return origin;
}

function renderEnableToggle(s: Server): HTMLLabelElement {
  const label = document.createElement("label");
  label.className = "toggle mcp-toggle";
  const input = document.createElement("input");
  input.type = "checkbox";
  input.checked = s.enabled;
  input.setAttribute("aria-label", `${s.enabled ? "Disable" : "Enable"} ${s.name}`);
  input.addEventListener("change", () => {
    // input.checked is already the NEW value (browser flipped it).
    // Pass the previous state explicitly so rollback restores correctly.
    void toggleServer.dispatch({ id: s.id, enabled: input.checked }, {
      onSuccess: () => { void refetchServers(); },
    });
  });
  const slider = document.createElement("span");
  slider.className = "toggle-slider";
  label.append(input, slider);
  return label;
}

function renderOAuthPill(url: string): HTMLAnchorElement {
  const link = document.createElement("a");
  link.className = "mcp-oauth-pill";
  const safe = isSafeURL(url);
  link.href = safe ? url : "#";
  link.target = "_blank";
  link.rel = "noopener noreferrer";
  link.textContent = safe ? "Finish sign-in →" : "Invalid OAuth URL";
  link.title = safe
    ? "Open the server's authorisation page in a new tab."
    : "The server sent an unsafe URL (not http or https).";
  return link;
}

function isSafeURL(url: string): boolean {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:";
  } catch {
    return false;
  }
}

function renderEditBtn(s: Server): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "icon-btn";
  btn.setAttribute("data-tooltip", "Edit");
  btn.setAttribute("aria-label", `Edit ${s.name}`);
  btn.innerHTML = ICON_EDIT_14;
  btn.addEventListener("click", () => { void openEditModal(s.id); });
  return btn;
}

function renderDeleteBtn(s: Server): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "icon-btn danger";
  btn.setAttribute("data-tooltip", "Remove");
  btn.setAttribute("aria-label", `Remove ${s.name}`);
  btn.innerHTML = ICON_TRASH_14;
  btn.addEventListener("click", () => {
    void (async () => {
      const ok = await confirmDialog(
        `Remove "${s.name}"? The agent loses access to this integration on the next new chat.`,
        "Remove",
        "destructive",
      );
      if (!ok) return;
      void deleteServer.dispatch({ id: s.id }, {
        onSuccess: () => { void refetchServers(); },
      });
    })();
  });
  return btn;
}

// --- Modal openers ---

function openAddModal(): void {
  setEditing({ id: "" });
  initModal({ mode: "search", server: null });
  $.mcpModal.classList.remove("hidden");
}

async function openEditModal(id: string): Promise<void> {
  const s = await openEdit.dispatch(id);
  if (s === null) return;
  setEditing({ id });
  const mode: AddMode = s.transport === "stdio" ? "npm" : "remote";
  initModal({ mode, server: s });
  $.mcpModal.classList.remove("hidden");
}

// --- Init ---

export function initMCP(): void {
  buildSectionScaffold();
  setRenderCallback(renderSection);

  const close = el<HTMLButtonElement>("mcp-modal-close");
  close.addEventListener("click", () => { cleanupModal(); closeModal($.mcpModal); });

  onSSE("mcp_config_changed", () => { void refetchServers(); });
  onSSE("mcp_connected", (_chat, p) => {
    setStatus(p.server, { name: p.server, state: "connected" });
    renderSection();
  });
  onSSE("mcp_oauth_needed", (_chat, p) => {
    setStatus(p.server, {
      name: p.server,
      state: "needs_auth",
      oauth_url: p.url,
    });
    renderSection();
  });
  onSSE("mcp_failed", (_chat, p) => {
    setStatus(p.server, {
      name: p.server,
      state: "failed",
      error: p.error,
    });
    renderSection();
  });
  onSSE("mcp_disconnected", (_chat, p) => {
    deleteStatus(p.server);
    renderSection();
  });
  onSSE("mcp_prewarm", (_chat, p) => {
    updatePrewarmStatus(p.package, p.state);
  });

  refetchServers();
  refetchStatus();
}

// --- Prewarm progress indicator ---

/** Show/hide a prewarm status badge on the server row matching the package name. */
function updatePrewarmStatus(pkg: string, state: string): void {
  // Find the server row whose configured command's npx package matches exactly.
  const rows = document.querySelectorAll<HTMLElement>(".mcp-row");
  for (const row of rows) {
    const serverId = row.dataset["serverId"] ?? "";
    const server = configured.find((s) => s.id === serverId);
    if (server === undefined) continue;
    const serverPkg = extractNpxPackage(server);
    if (serverPkg !== pkg && server.name !== pkg) continue;

    let badge = row.querySelector(".prewarm-badge") as HTMLElement | null;
    if (state === "done") {
      if (badge !== null) badge.remove();
      return;
    }
    if (badge === null) {
      badge = document.createElement("span");
      badge.className = "prewarm-badge";
      const nameEl = row.querySelector(".mcp-row-name");
      if (nameEl !== null) nameEl.after(badge);
      else row.prepend(badge);
    }
    badge.textContent = state === "installing" ? "Installing…" : "Install failed";
    badge.classList.toggle("prewarm-failed", state === "failed");
    return;
  }
}
