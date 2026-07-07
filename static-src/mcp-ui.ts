// ---------------------------------------------------------------------------
// MCP UI: section scaffold, server list rendering, row sub-components, init.
//
// Rows are rendered by a single bindList over the `servers` collection
// (keyed by id), so add/remove/reorder touch the DOM minimally and a row
// element persists across changes (no full-rebuild that loses toggle focus or
// re-binds bindLoadingState every render). Each row owns one effect that reads
// its server signal + the per-server runtime-status + prewarm signals and
// surgically patches the dot / meta / oauth pill / prewarm badge / toggle.
// ---------------------------------------------------------------------------

import { el, bindList, effect } from "@cplieger/reactive";

import { $ } from "./dom.js";
import { onSSE } from "./bus.js";
import { onModalClose, openModal } from "./modals.js";
import { confirm as confirmDialog } from "./confirm.js";
import { ICON_EDIT_14, ICON_TRASH_14, ICON_PLUS_16 } from "./icons.js";
import {
  type Server,
  type RuntimeStatus,
  type RuntimeState,
  type PrewarmState,
  servers,
  mcpState,
  statusSignalFor,
  prewarmSignalFor,
  setPrewarm,
  configuredServers,
} from "./mcp-state.js";
import { type AddMode, setEditing, initModal, cleanupModal } from "./mcp-panels.js";
import { extractNpxPackage } from "./mcp-panels.js";
import { toggleServer, deleteServer, openEdit } from "./actions/mcp.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";

// --- Section scaffold ---

let sectionBody: HTMLDivElement | null = null;
let emptyMsg: HTMLParagraphElement | null = null;
let listBound = false;

// Per-row cleanups (the row effect + its bindLoadingState bindings), disposed
// when the row leaves the list.
const rowCleanups = new Map<string, (() => void)[]>();
registerCleanup(() => {
  for (const cs of rowCleanups.values()) {
    for (const fn of cs) {
      fn();
    }
  }
  rowCleanups.clear();
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

  emptyMsg = el(
    "p",
    { className: "mcp-empty" },
    "No integrations connected yet. Click + to search the official MCP registry or paste a config.",
  ) as HTMLParagraphElement;
  sectionBody = el("div", { className: "mcp-server-list" }) as HTMLDivElement;

  section.replaceChildren(title, hint, actionRow, emptyMsg, sectionBody);
  bindServerList();
}

// --- Reactive server list ---

function bindServerList(): void {
  if (listBound || sectionBody === null) {
    return;
  }
  listBound = true;
  const empty = emptyMsg;
  bindList(sectionBody, servers, {
    mount: (s, id) => mountRow(s, id),
    onRemove: (_el, id) => {
      cleanupRow(id);
    },
  });
  // Empty-state toggle (sibling of the bindList container, not reconciled).
  effect(() => {
    if (empty !== null) {
      empty.hidden = servers.ids.value.length > 0;
    }
  });
}

function cleanupRow(id: string): void {
  const cs = rowCleanups.get(id);
  if (cs !== undefined) {
    for (const fn of cs) {
      fn();
    }
    rowCleanups.delete(id);
  }
}

function mountRow(s: Server, id: string): HTMLElement {
  const cleanups: (() => void)[] = [];

  // Toggle (built once; the effect only syncs .checked so user focus survives).
  const input = el("input", { type: "checkbox" }) as HTMLInputElement;
  input.addEventListener("change", () => {
    input.setAttribute("aria-label", `${input.checked ? "Disable" : "Enable"} ${s.name}`);
    void toggleServer.dispatch(
      { id, enabled: input.checked },
      {
        silent: true,
        onSuccess: () => {
          mcpState.refetchServers();
        },
      },
    );
  });
  cleanups.push(bindLoadingState("mcp.toggle_server", input));
  const toggle = el(
    "label",
    { className: "toggle mcp-toggle" },
    input,
    el("span", { className: "toggle-slider" }),
  );

  const dot = el("span", { className: "mcp-dot", role: "img" });
  const nameEl = el("span", { className: "mcp-row-name" });
  const transportBadge = el("span", { className: "mcp-transport" });
  const nameLine = el("div", { className: "mcp-row-name-line" }, dot, nameEl, transportBadge);
  const meta = el("div", { className: "mcp-row-meta" });
  const body = el("div", { className: "mcp-row-body" }, nameLine, meta);

  const editBtn = renderEditBtn(s, cleanups);
  const deleteBtn = renderDeleteBtn(s, cleanups);
  const actions = el("div", { className: "mcp-row-actions" }, editBtn, deleteBtn);

  const row = el(
    "div",
    { className: "mcp-row", "data-server-id": id },
    toggle,
    body,
    actions,
  ) as HTMLDivElement;

  // Per-row content tier: react to this server's config + runtime status +
  // prewarm, patching surgically (no replaceChildren -> focus/identity kept).
  let oauthPill: HTMLAnchorElement | null = null;
  let oauthUrl: string | null = null;
  let prewarmBadge: HTMLSpanElement | null = null;
  cleanups.push(
    effect(() => {
      const cur = servers.signalFor(id)?.value ?? s;
      const st = statusSignalFor(cur.name).value;
      const pw = prewarmSignalFor(id).value;

      input.checked = cur.enabled;
      input.setAttribute("aria-label", `${cur.enabled ? "Disable" : "Enable"} ${cur.name}`);
      nameEl.textContent = cur.name;
      transportBadge.className = `mcp-transport mcp-transport-${cur.transport}`;
      transportBadge.textContent = cur.transport;
      applyStatusDot(dot, cur, st);
      meta.textContent = renderMeta(cur, st);

      if (cur.enabled && st.state === "needs_auth") {
        if (oauthPill === null || oauthUrl !== st.oauth_url) {
          oauthPill?.remove();
          oauthPill = renderOAuthPill(st.oauth_url);
          oauthUrl = st.oauth_url;
          body.appendChild(oauthPill);
        }
      } else if (oauthPill !== null) {
        oauthPill.remove();
        oauthPill = null;
        oauthUrl = null;
      }

      prewarmBadge = applyPrewarm(prewarmBadge, nameEl, pw);
    }),
  );

  rowCleanups.set(id, cleanups);
  return row;
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

function applyStatusDot(dot: HTMLSpanElement, s: Server, st: RuntimeStatus): void {
  dot.className = "mcp-dot";
  if (!s.enabled) {
    dot.classList.add("disabled");
    dot.title = "Disabled";
    dot.setAttribute("aria-label", `${s.name}: disabled`);
    return;
  }
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

function renderMeta(s: Server, st: RuntimeStatus): string {
  if (!s.enabled) {
    return "Disabled";
  }
  const origin = s.transport === "stdio" ? (s.command ?? "") : (s.url ?? "");
  if (isFailedWithError(st)) {
    return `${origin} — ${st.error}`;
  }
  return origin;
}

/** Add/update/remove the prewarm badge after the name. Returns the badge (or
 *  null when cleared) so the caller tracks it across effect runs. */
function applyPrewarm(
  badge: HTMLSpanElement | null,
  nameEl: HTMLElement,
  pw: PrewarmState,
): HTMLSpanElement | null {
  if (pw === "none") {
    badge?.remove();
    return null;
  }
  let b = badge;
  if (b === null) {
    b = el("span", { className: "prewarm-badge" });
    nameEl.after(b);
  }
  b.textContent = pw === "installing" ? "Installing…" : "Install failed";
  b.classList.toggle("prewarm-failed", pw !== "installing");
  return b;
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

function renderEditBtn(s: Server, cleanups: (() => void)[]): HTMLButtonElement {
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
  cleanups.push(bindLoadingState("mcp.open_edit", btn));
  return btn;
}

function renderDeleteBtn(s: Server, cleanups: (() => void)[]): HTMLButtonElement {
  const btn = el("button", {
    type: "button",
    className: "icon-btn danger",
    "data-tooltip": "Remove",
    "aria-label": `Remove ${s.name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_TRASH_14;
  cleanups.push(bindLoadingState("mcp.delete_server", btn));
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

  // The modal's Close button is wired generically by initAllModals; the
  // add/edit-form cleanup must run on EVERY close path (Close button, backdrop
  // drag-safe click, Escape), so hang it off the controller's onClose. This
  // replaces the old MutationObserver that watched for the `.hidden` class the
  // overlay system toggled (the native <dialog> no longer uses it).
  onModalClose($.mcpModal, cleanupModal);

  // Runtime status / config changes flow through the per-server signals; the
  // row effects re-render reactively (no explicit re-render call needed).
  onSSE("mcp_config_changed", () => {
    mcpState.refetchServers();
  });
  onSSE("mcp_connected", (_chat, p) => {
    mcpState.setStatus(p.server, { name: p.server, state: "connected" });
  });
  onSSE("mcp_oauth_needed", (_chat, p) => {
    mcpState.setStatus(p.server, { name: p.server, state: "needs_auth", oauth_url: p.url });
  });
  onSSE("mcp_failed", (_chat, p) => {
    mcpState.setStatus(p.server, { name: p.server, state: "failed", error: p.error });
  });
  onSSE("mcp_disconnected", (_chat, p) => {
    mcpState.deleteStatus(p.server);
  });
  onSSE("mcp_prewarm", (_chat, p) => {
    updatePrewarmStatus(p.package, p.state as "installing" | "done" | "failed");
  });

  mcpState.refetchServers();
  mcpState.refetchStatus();
}

// --- Prewarm progress indicator ---

/** Map an npx prewarm event (keyed by package) to its server's prewarm signal. */
function updatePrewarmStatus(pkg: string, state: "installing" | "done" | "failed"): void {
  for (const server of configuredServers()) {
    const serverPkg = extractNpxPackage(server);
    if (serverPkg !== pkg && server.name !== pkg) {
      continue;
    }
    setPrewarm(server.id, state);
    return;
  }
}
