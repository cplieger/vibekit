// ---------------------------------------------------------------------------
// MCP UI: section scaffold, server list rendering, row sub-components, init.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

import { $, byId } from "./dom.js";
import { onSSE } from "./bus.js";
import { closeModal, openModal } from "./modals.js";
import { confirm as confirmDialog } from "./confirm.js";
import { ICON_EDIT_14, ICON_TRASH_14, ICON_PLUS_16 } from "./icons.js";
import {
  type Server,
  type RuntimeStatus,
  type RuntimeState,
  configured,
  status,
  mcpState,
} from "./mcp-state.js";
import { type AddMode, setEditing, initModal, cleanupModal } from "./mcp-panels.js";
import { extractNpxPackage } from "./mcp-panels.js";
import { toggleServer, deleteServer, openEdit } from "./actions/mcp.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";

// --- Section scaffold ---

let sectionBody: HTMLDivElement | null = null;
let rowBindingCleanups: (() => void)[] = [];
registerCleanup(() => {
  for (const fn of rowBindingCleanups) {
    fn();
  }
  rowBindingCleanups = [];
});

function buildSectionScaffold(): void {
  const section = document.getElementById("mcp-section");
  if (section === null) {
    return;
  }

  const title = el("h3", { className: "section-title" }, "MCP integrations");

  const hint = el(
    "p",
    { className: "section-hint" },
    "Connect your agent to external systems: GitHub, Linear, Postgres, Sentry, or anything else that speaks the Model Context Protocol. Configuration changes apply on the next new chat. Disabled servers are kept on disk but don't consume context tokens or spawn subprocesses. For servers that need a manual install (pip, binary), add them in the Installed tools section above first (category: MCP server), then fill in credentials here.",
  );

  const addBtn = el("button", {
    type: "button",
    className: "action-pill",
    "data-tooltip": "Connect integration",
    "aria-label": "Connect integration",
  });
  addBtn.innerHTML = ICON_PLUS_16;
  addBtn.addEventListener("click", () => {
    openAddModal();
  });

  const actions = el("div", { className: "action-bar action-bar-inline" }, addBtn);

  const actionRow = el("div", { className: "section-actions-row" }, actions);

  sectionBody = el("div", { className: "mcp-server-list" }) as HTMLDivElement;

  section.replaceChildren(title, hint, actionRow, sectionBody);
}

// --- Section render ---

function renderSection(): void {
  if (sectionBody === null) {
    return;
  }
  renderRows();
}

function renderRows(): void {
  if (sectionBody === null) {
    return;
  }
  for (const fn of rowBindingCleanups) {
    fn();
  }
  rowBindingCleanups = [];
  sectionBody.replaceChildren();

  if (configured.length === 0) {
    sectionBody.appendChild(
      el(
        "p",
        { className: "mcp-empty" },
        "No integrations connected yet. Click + to search the official MCP registry or paste a config.",
      ),
    );
    return;
  }

  for (const s of configured) {
    sectionBody.appendChild(renderRow(s));
  }
}

function renderRow(s: Server): HTMLDivElement {
  const st = status.get(s.name);

  const toggle = renderEnableToggle(s);

  const name = el("span", { className: "mcp-row-name" }, s.name);
  const dot = renderStatusDot(s, st);
  const transportBadge = el(
    "span",
    { className: `mcp-transport mcp-transport-${s.transport}` },
    s.transport,
  );
  const nameLine = el("div", { className: "mcp-row-name-line" }, dot, name, transportBadge);

  const meta = el("div", { className: "mcp-row-meta" }, renderMeta(s, st));

  const body = el("div", { className: "mcp-row-body" }, nameLine, meta);
  if (st?.state === "needs_auth") {
    body.appendChild(renderOAuthPill(st.oauth_url));
  }

  const actions = el("div", { className: "mcp-row-actions" }, renderEditBtn(s), renderDeleteBtn(s));

  return el(
    "div",
    { className: "mcp-row", "data-server-id": s.id },
    toggle,
    body,
    actions,
  ) as HTMLDivElement;
}

// --- Row sub-components ---

const STATUS_META: Readonly<Record<RuntimeState, { css: string; title: string }>> = {
  connected: { css: "connected", title: "Connected" },
  needs_auth: { css: "oauth", title: "Needs authentication" },
  idle: { css: "idle", title: "Not yet connected — start a chat to initialise" },
  failed: { css: "failed", title: "Failed to initialise" },
};

/** Type-narrowing guard for the "failed" RuntimeStatus variant. */
function isFailedWithError(
  st: RuntimeStatus,
): st is RuntimeStatus & { state: "failed"; error: string } {
  const FAILED: RuntimeState = "failed";
  return st.state === FAILED && st.error !== "";
}

function renderStatusDot(s: Server, st: RuntimeStatus | undefined): HTMLSpanElement {
  const dot = el("span", { className: "mcp-dot", role: "img" });
  if (!s.enabled) {
    dot.classList.add("disabled");
    dot.title = "Disabled";
    dot.setAttribute("aria-label", `${s.name}: disabled`);
  } else if (st === undefined) {
    dot.classList.add("idle");
    dot.title = "Not yet connected — start a chat to initialise";
    dot.setAttribute("aria-label", `${s.name}: idle`);
  } else {
    const meta = STATUS_META[st.state] ?? STATUS_META.idle; // eslint-disable-line @typescript-eslint/no-unnecessary-condition
    dot.classList.add(meta.css);
    if (isFailedWithError(st)) {
      dot.title = `Failed to initialise: ${st.error}`;
      dot.setAttribute("aria-label", `${s.name}: failed — ${st.error}`);
    } else {
      dot.title = meta.title;
      dot.setAttribute("aria-label", `${s.name}: ${meta.title.toLowerCase()}`);
    }
  }
  return dot;
}

function renderMeta(s: Server, st: RuntimeStatus | undefined): string {
  if (!s.enabled) {
    return "Disabled";
  }
  const origin = s.transport === "stdio" ? (s.command ?? "") : (s.url ?? "");
  if (st !== undefined && isFailedWithError(st)) {
    return `${origin} — ${st.error}`;
  }
  return origin;
}

function renderEnableToggle(s: Server): HTMLLabelElement {
  const input = el("input", {
    type: "checkbox",
    checked: s.enabled,
    "aria-label": `${s.enabled ? "Disable" : "Enable"} ${s.name}`,
  }) as HTMLInputElement;
  input.addEventListener("change", () => {
    // input.checked is already the NEW value (browser flipped it).
    input.setAttribute("aria-label", `${input.checked ? "Disable" : "Enable"} ${s.name}`);
    // Pass the previous state explicitly so rollback restores correctly.
    void toggleServer.dispatch(
      { id: s.id, enabled: input.checked },
      {
        silent: true,
        onSuccess: () => {
          mcpState.refetchServers();
        },
      },
    );
  });
  const slider = el("span", { className: "toggle-slider" });
  const label = el("label", { className: "toggle mcp-toggle" }, input, slider) as HTMLLabelElement;
  rowBindingCleanups.push(bindLoadingState("mcp.toggle_server", input));
  return label;
}

function renderOAuthPill(url: string): HTMLAnchorElement {
  const safe = isSafeURL(url);
  return el(
    "a",
    {
      className: "mcp-oauth-pill",
      href: safe ? url : "#",
      target: "_blank",
      rel: "noopener noreferrer",
      title: safe
        ? "Open the server's authorisation page in a new tab."
        : "The server sent an unsafe URL (not http or https).",
    },
    safe ? "Finish sign-in →" : "Invalid OAuth URL",
  ) as HTMLAnchorElement;
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
  const btn = el("button", {
    type: "button",
    className: "icon-btn",
    "data-tooltip": "Edit",
    "aria-label": `Edit ${s.name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_EDIT_14;
  btn.addEventListener("click", () => {
    void openEditModal(s.id);
  });
  rowBindingCleanups.push(bindLoadingState("mcp.open_edit", btn));
  return btn;
}

function renderDeleteBtn(s: Server): HTMLButtonElement {
  const btn = el("button", {
    type: "button",
    className: "icon-btn danger",
    "data-tooltip": "Remove",
    "aria-label": `Remove ${s.name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_TRASH_14;
  rowBindingCleanups.push(bindLoadingState("mcp.delete_server", btn));
  btn.addEventListener("click", () => {
    void (async () => {
      const ok = await confirmDialog(
        `Remove "${s.name}"? The agent loses access to this integration on the next new chat.`,
        "Remove",
        "destructive",
      );
      if (!ok) {
        return;
      }
      void deleteServer.dispatch(
        { id: s.id },
        {
          onSuccess: () => {
            mcpState.refetchServers();
          },
        },
      );
    })();
  });
  return btn;
}

// --- Modal openers ---

function openAddModal(): void {
  setEditing({ id: "" });
  initModal({ mode: "search", server: null });
  openModal($.mcpModal);
}

async function openEditModal(id: string): Promise<void> {
  const s = await openEdit.dispatch(id);
  if (s === null) {
    return;
  }
  setEditing({ id });
  const mode: AddMode = s.transport === "stdio" ? "npm" : "remote";
  initModal({ mode, server: s });
  openModal($.mcpModal);
}

// --- Init ---

export function initMCP(): void {
  buildSectionScaffold();
  mcpState.setRenderCallback(renderSection);

  const close = byId<HTMLButtonElement>("mcp-modal-close");
  close.addEventListener("click", () => {
    cleanupModal();
    closeModal($.mcpModal);
  });

  // Hook into Escape and overlay-dismiss paths: closeModal toggles the
  // 'hidden' class on the modal element. Watch for that transition and
  // call cleanupModal regardless of which code path closed the modal.
  // (cleanupModal is idempotent so the close-button path is harmless.)
  const observer = new MutationObserver((mutations) => {
    for (const m of mutations) {
      if (m.attributeName === "class" && $.mcpModal.classList.contains("hidden")) {
        cleanupModal();
      }
    }
  });
  observer.observe($.mcpModal, { attributes: true, attributeFilter: ["class"] });

  onSSE("mcp_config_changed", () => {
    mcpState.refetchServers();
  });
  onSSE("mcp_connected", (_chat, p) => {
    mcpState.setStatus(p.server, { name: p.server, state: "connected" });
    renderSection();
  });
  onSSE("mcp_oauth_needed", (_chat, p) => {
    mcpState.setStatus(p.server, {
      name: p.server,
      state: "needs_auth",
      oauth_url: p.url,
    });
    renderSection();
  });
  onSSE("mcp_failed", (_chat, p) => {
    mcpState.setStatus(p.server, {
      name: p.server,
      state: "failed",
      error: p.error,
    });
    renderSection();
  });
  onSSE("mcp_disconnected", (_chat, p) => {
    mcpState.deleteStatus(p.server);
    renderSection();
  });
  onSSE("mcp_prewarm", (_chat, p) => {
    updatePrewarmStatus(p.package, p.state as "installing" | "done" | "failed");
  });

  mcpState.refetchServers();
  mcpState.refetchStatus();
}

// --- Prewarm progress indicator ---

/** Show/hide a prewarm status badge on the server row matching the package name. */
function updatePrewarmStatus(pkg: string, state: "installing" | "done" | "failed"): void {
  // Find the server row whose configured command's npx package matches exactly.
  const rows = document.querySelectorAll<HTMLElement>(".mcp-row");
  for (const row of rows) {
    const serverId = row.dataset["serverId"] ?? "";
    const server = configured.find((s) => s.id === serverId);
    if (server === undefined) {
      continue;
    }
    const serverPkg = extractNpxPackage(server);
    if (serverPkg !== pkg && server.name !== pkg) {
      continue;
    }

    let badge = row.querySelector(".prewarm-badge");
    if (state === "done") {
      if (badge !== null) {
        badge.remove();
      }
      return;
    }
    if (badge === null) {
      badge = el("span", { className: "prewarm-badge" });
      const nameEl = row.querySelector(".mcp-row-name");
      if (nameEl !== null) {
        nameEl.after(badge);
      } else {
        row.prepend(badge);
      }
    }
    badge.textContent = state === "installing" ? "Installing…" : "Install failed";
    badge.classList.toggle("prewarm-failed", state !== "installing");
    return;
  }
}
