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
import { isSafeURL } from "./url-safety.js";
import { onSSE } from "./bus.js";
import { onGovernanceChange } from "./governance.js";
import type { GovernanceStatePayload } from "./types.js";
import { onModalClose, openModal } from "./modals.js";
import { confirm as confirmDialog } from "./confirm.js";
import { showToast } from "./toast.js";
import { ICON_EDIT_14, ICON_TRASH_14, ICON_PLUS_16, ICON_REFRESH, ICON_SPINNER } from "./icons.js";
import {
  type Server,
  type RuntimeStatus,
  type RuntimeState,
  type PrewarmState,
  type ServerDiscovery,
  type MCPPromptInfo,
  type MCPPromptArg,
  type MCPResourceInfo,
  servers,
  mcpState,
  statusSignalFor,
  prewarmSignalFor,
  discoverySignalFor,
  setPrewarm,
  configuredServers,
} from "./mcp-state.js";
import { type AddMode, setEditing, initModal, cleanupModal } from "./mcp-panels.js";
import { extractNpxPackage } from "./mcp-panels.js";
import {
  toggleServer,
  deleteServer,
  openEdit,
  reconnectServer,
  getPromptContent,
  getResourceContent,
} from "./actions/mcp.js";
import { promptResultToText, resourceResultToText } from "./mcp-content.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";

// --- Section scaffold ---

let sectionBody: HTMLDivElement | null = null;
let emptyMsg: HTMLParagraphElement | null = null;
let listBound = false;
// Held so the governance effect can disable the add affordance and toggle the
// "disabled by your organization" notice when mcp_enabled is off.
let addBtn: HTMLButtonElement | null = null;
let govDisabledMsg: HTMLParagraphElement | null = null;

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

  const btn = el("button", {
    type: "button",
    className: "action-pill",
    "data-tooltip": "Connect integration",
    "aria-label": "Connect integration",
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_PLUS_16;
  btn.addEventListener("click", () => {
    openAddModal();
  });
  addBtn = btn;

  const actions = el("div", { className: "action-bar action-bar-inline" }, btn);
  const actionRow = el("div", { className: "section-actions-row" }, actions);

  // Org-governance notice: shown only when governance says MCP is disabled.
  govDisabledMsg = el("p", {
    className: "mcp-gov-disabled",
    hidden: true,
  }) as HTMLParagraphElement;

  emptyMsg = el(
    "p",
    { className: "mcp-empty" },
    "No integrations connected yet. Click + to search the official MCP registry or paste a config.",
  ) as HTMLParagraphElement;
  sectionBody = el("div", { className: "mcp-server-list" }) as HTMLDivElement;

  section.replaceChildren(title, hint, actionRow, govDisabledMsg, emptyMsg, sectionBody);
  bindServerList();
}

// applyGovernance reflects the org/account MCP policy into the section's held
// add button + notice element. Thin wrapper over the pure applyMcpGovernance so
// the DOM effect is unit-testable without initMCP's network side effects.
function applyGovernance(g: GovernanceStatePayload): void {
  if (addBtn !== null && govDisabledMsg !== null) {
    applyMcpGovernance(g, addBtn, govDisabledMsg);
  }
}

/** Apply the MCP governance policy to a given add-button + notice element: when
 *  governance is KNOWN and mcp_enabled is false, the add-server affordance is
 *  disabled and the notice (with disabledReason when present) is shown. An
 *  unknown policy leaves the affordance enabled (permissive default). Exported
 *  for focused testing. */
export function applyMcpGovernance(
  g: GovernanceStatePayload,
  add: HTMLButtonElement,
  notice: HTMLElement,
): void {
  const disabled = g.known && !g.features.mcp_enabled;
  add.disabled = disabled;
  add.setAttribute(
    "data-tooltip",
    disabled ? "MCP is disabled by your organization" : "Connect integration",
  );
  notice.hidden = !disabled;
  if (disabled) {
    const reason = (g.disabled_reason ?? "").trim();
    notice.textContent =
      reason !== ""
        ? `MCP integrations are disabled by your organization: ${reason}`
        : "MCP integrations are disabled by your organization.";
  }
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

  const reconnectBtn = renderReconnectBtn(id, s);
  const editBtn = renderEditBtn(s, cleanups);
  const deleteBtn = renderDeleteBtn(s, cleanups);
  const actions = el("div", { className: "mcp-row-actions" }, reconnectBtn, editBtn, deleteBtn);

  // Discovery disclosure (prompts & resources) — lives in the row body,
  // rendered by its own effect so it re-renders only when THIS server's
  // discovery changes (independent of the status/prewarm tier below).
  const discoveryBox = el("div", { className: "mcp-discovery" }) as HTMLDivElement;
  body.appendChild(discoveryBox);
  cleanups.push(
    effect(() => {
      const cur = servers.signalFor(id)?.value ?? s;
      const disc = discoverySignalFor(cur.name).value;
      renderDiscovery(discoveryBox, cur.name, disc, cur.enabled);
    }),
  );

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

      // Reconnect only makes sense when a live bridge could hold this
      // server's connection: enabled and past the "idle" (no-bridge) state.
      reconnectBtn.hidden = !cur.enabled || st.state === "idle";

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

function renderReconnectBtn(id: string, initial: Server): HTMLButtonElement {
  const btn = el("button", {
    type: "button",
    className: "icon-btn",
    "data-tooltip": "Reconnect",
    "aria-label": `Reconnect ${initial.name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_REFRESH;
  btn.addEventListener("click", () => {
    if (btn.disabled) {
      return;
    }
    const cur = servers.signalFor(id)?.value ?? initial;
    btn.disabled = true;
    btn.setAttribute("aria-busy", "true");
    btn.innerHTML = ICON_SPINNER;
    void reconnectServer
      .dispatch(
        { server: cur.name },
        {
          onSuccess: (res) => {
            // Server-canonical: the reconnect re-emits _kiro/mcp/status on
            // every bridge; refetch to pull the refreshed dot + discovery.
            mcpState.refetchStatus();
            if (res.reconnected === 0) {
              showToast("No active chat to reconnect through — open a chat first.", "info");
            }
          },
        },
      )
      .finally(() => {
        btn.disabled = false;
        btn.removeAttribute("aria-busy");
        btn.innerHTML = ICON_REFRESH;
      });
  });
  return btn;
}

// --- Discovery (prompts & resources) surface ---

/** Pick the display string, falling back when the primary is empty. */
function orFallback(primary: string, fallback: string): string {
  return primary !== "" ? primary : fallback;
}

/** (Re)render the per-server prompts/resources disclosure. Hidden when the
 *  server is disabled or advertises nothing. */
function renderDiscovery(
  box: HTMLDivElement,
  serverName: string,
  disc: ServerDiscovery,
  enabled: boolean,
): void {
  box.replaceChildren();
  const count = disc.prompts.length + disc.resources.length;
  if (!enabled || count === 0) {
    box.hidden = true;
    return;
  }
  box.hidden = false;

  const details = el("details", { className: "mcp-discovery-details" });
  details.appendChild(
    el("summary", { className: "mcp-discovery-summary" }, `Prompts & resources (${String(count)})`),
  );
  if (disc.prompts.length > 0) {
    details.appendChild(el("div", { className: "mcp-disc-group" }, "Prompts"));
    for (const p of disc.prompts) {
      details.appendChild(buildPromptItem(serverName, p));
    }
  }
  if (disc.resources.length > 0) {
    details.appendChild(el("div", { className: "mcp-disc-group" }, "Resources"));
    for (const res of disc.resources) {
      details.appendChild(buildResourceItem(serverName, res));
    }
  }
  box.appendChild(details);
}

/** Build the label (name + optional description) shared by disc items. */
function discItemLabel(name: string, description: string | undefined): HTMLElement {
  const label = el(
    "div",
    { className: "mcp-disc-item-label" },
    el("span", { className: "mcp-disc-item-name" }, name),
  );
  if (description !== undefined && description !== "") {
    label.appendChild(el("span", { className: "mcp-disc-item-desc" }, description));
  }
  return label;
}

function buildResourceItem(serverName: string, res: MCPResourceInfo): HTMLElement {
  const label = discItemLabel(orFallback(res.name, res.uri), res.description);
  const btn = el(
    "button",
    { type: "button", className: "mcp-disc-insert", "data-tooltip": "Insert into prompt" },
    "Insert",
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    void insertResource(serverName, res, btn);
  });
  return el("div", { className: "mcp-disc-item" }, label, btn);
}

function buildPromptItem(serverName: string, p: MCPPromptInfo): HTMLElement {
  const displayName = orFallback(p.name, p.prompt_name);
  const args = p.arguments ?? [];
  const item = el("div", { className: "mcp-disc-item" }, discItemLabel(displayName, p.description));

  if (args.length === 0) {
    const btn = el(
      "button",
      { type: "button", className: "mcp-disc-insert", "data-tooltip": "Insert into prompt" },
      "Insert",
    ) as HTMLButtonElement;
    btn.addEventListener("click", () => {
      void insertPrompt(serverName, p, {}, btn);
    });
    item.appendChild(btn);
    return item;
  }

  // Prompt with arguments: a toggle reveals an inline form; submit inserts.
  const form = buildArgForm(serverName, p, args);
  form.hidden = true;
  const toggleBtn = el(
    "button",
    { type: "button", className: "mcp-disc-insert", "data-tooltip": "Fill in arguments" },
    "Fill in…",
  ) as HTMLButtonElement;
  toggleBtn.addEventListener("click", () => {
    form.hidden = !form.hidden;
  });
  item.appendChild(toggleBtn);
  const wrap = el("div", { className: "mcp-disc-prompt-wrap" }, item, form);
  return wrap;
}

function buildArgForm(serverName: string, p: MCPPromptInfo, args: MCPPromptArg[]): HTMLFormElement {
  const form = el("form", { className: "mcp-disc-arg-form" }) as HTMLFormElement;
  const inputs = new Map<string, HTMLInputElement>();
  for (const a of args) {
    const input = el("input", {
      type: "text",
      className: "mcp-disc-arg-input",
      placeholder: orFallback(a.description ?? "", a.name),
    }) as HTMLInputElement;
    if (a.required === true) {
      input.required = true;
    }
    inputs.set(a.name, input);
    form.appendChild(
      el(
        "label",
        { className: "mcp-disc-arg-row" },
        el(
          "span",
          { className: "mcp-disc-arg-name" },
          a.required === true ? `${a.name} *` : a.name,
        ),
        input,
      ),
    );
  }
  const submit = el(
    "button",
    { type: "submit", className: "mcp-disc-insert" },
    "Insert",
  ) as HTMLButtonElement;
  form.appendChild(submit);
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const values: Record<string, string> = {};
    for (const [name, input] of inputs) {
      if (input.value !== "") {
        values[name] = input.value;
      }
    }
    void insertPrompt(serverName, p, values, submit);
  });
  return form;
}

async function insertPrompt(
  serverName: string,
  p: MCPPromptInfo,
  args: Record<string, string>,
  btn: HTMLButtonElement,
): Promise<void> {
  btn.disabled = true;
  try {
    const res = await getPromptContent.dispatch({
      server: serverName,
      prompt: p.prompt_name,
      arguments: args,
    });
    if (res === null) {
      return;
    }
    const text = promptResultToText(res);
    if (text === "") {
      showToast("Prompt returned no text.", "info");
      return;
    }
    insertIntoPrompt(text);
    showToast(`Inserted "${orFallback(p.name, p.prompt_name)}" into the prompt.`, "success");
  } finally {
    btn.disabled = false;
  }
}

async function insertResource(
  serverName: string,
  res: MCPResourceInfo,
  btn: HTMLButtonElement,
): Promise<void> {
  btn.disabled = true;
  try {
    const result = await getResourceContent.dispatch({ server: serverName, uri: res.uri });
    if (result === null) {
      return;
    }
    const text = resourceResultToText(result);
    if (text === "") {
      showToast("Resource has no text content to insert.", "info");
      return;
    }
    const heading = orFallback(res.name, res.uri);
    insertIntoPrompt(`# ${heading}\n\n${text}`);
    showToast(`Inserted "${heading}" into the prompt.`, "success");
  } finally {
    btn.disabled = false;
  }
}

/** Append text to the prompt input (preserving any draft) and notify the
 *  prompt-input module so it re-sizes + re-enables the send button. */
function insertIntoPrompt(text: string): void {
  const input = $.promptInput;
  input.value = input.value === "" ? text : `${input.value}\n\n${text}`;
  input.focus();
  input.dispatchEvent(new Event("input", { bubbles: true }));
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
    // Pull the newly-connected server's advertised prompts/resources (they
    // ride /api/mcp/status, not the mcp_connected payload). Coalesced.
    mcpState.refetchStatus();
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

  // Reflect the org MCP policy (disable add + show notice when suppressed).
  // Fires immediately if governance is already known, then on every change.
  onGovernanceChange(applyGovernance);

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
