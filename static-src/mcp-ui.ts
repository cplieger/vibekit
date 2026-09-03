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
  type Origin,
  type PrewarmState,
  type ServerDiscovery,
  type MCPPromptInfo,
  type MCPPromptArg,
  type MCPResourceInfo,
  servers,
  unconfiguredNames,
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
  relayOAuthCallback,
  getPromptContent,
  getResourceContent,
} from "./actions/mcp.js";
import { promptResultToText, resourceResultToText } from "./mcp-content.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";

// --- Section scaffold ---

let sectionBody: HTMLDivElement | null = null;
let foreignBody: HTMLDivElement | null = null;
let emptyMsg: HTMLParagraphElement | null = null;
let listBound = false;
let foreignBound = false;
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

  const title = el("h2", { className: "section-title" }, "MCP integrations");

  const hint = el(
    "p",
    { className: "section-hint" },
    "Connect your agent to external systems: GitHub, Linear, Postgres, Sentry, or anything else that speaks the Model Context Protocol. Changes apply immediately, including to chats already running. Disabled servers are kept on disk but don't consume context tokens or spawn subprocesses. For servers that need a local package (pip, npm, a binary), install it from the Tools section above first (Add tool), then fill in credentials here.",
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
  // Read-only rows for servers the agent reported that this page does not
  // configure. A separate container, not extra entries in the configured
  // collection: that collection is keyed by a persisted server id these servers
  // have none of, and every mutation path on it (edit, delete, toggle, prewarm
  // mapping) addresses a record that does not exist for them.
  foreignBody = el("div", { className: "mcp-server-list mcp-foreign-list" }) as HTMLDivElement;

  section.replaceChildren(
    title,
    hint,
    actionRow,
    govDisabledMsg,
    emptyMsg,
    sectionBody,
    foreignBody,
  );
  bindServerList();
  bindForeignList();
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

/** Render the read-only rows for servers this page does not configure. One
 *  effect over the name list plus one per-row effect, mirroring the configured
 *  list's shape. */
function bindForeignList(): void {
  if (foreignBound || foreignBody === null) {
    return;
  }
  foreignBound = true;
  const host = foreignBody;
  registerCleanup(
    effect(() => {
      const names = unconfiguredNames.value;
      const rows = names.map((name) => mountForeignRow(name));
      host.replaceChildren(...rows);
      host.hidden = rows.length === 0;
    }),
  );
}

/** One read-only row: status dot, name, provenance chip, and the discovery
 *  disclosure when the server advertises anything.
 *
 *  No toggle, no edit, no delete, and no Reconnect. There is no persisted record
 *  behind the row, so every one of those affordances would address a server id
 *  that does not exist; the row's job is to stop the page being silent about an
 *  integration whose tools are in the agent's tool list. */
function mountForeignRow(name: string): HTMLElement {
  const dot = el("span", { className: "mcp-dot", role: "img" });
  const nameEl = el("span", { className: "mcp-row-name" }, name);
  const originChip = buildOriginChip();
  const metaText = el("span", { className: "mcp-row-meta-text" });
  const discoveryBox = el("div", { className: "mcp-discovery" }) as HTMLDivElement;
  const body = el(
    "div",
    { className: "mcp-row-body" },
    el("div", { className: "mcp-row-name-line" }, dot, nameEl),
    el("div", { className: "mcp-row-meta" }, originChip, metaText),
    discoveryBox,
  );
  const row = el("div", { className: "mcp-row mcp-row-readonly" }, body) as HTMLDivElement;

  // The row is rebuilt whenever the name list changes, so its subscription dies
  // with it; the enclosing list effect disposes nested effects on re-run.
  effect(() => {
    const st = statusSignalFor(name).value;
    applyStatusDotForeign(dot, name, st);
    applyOriginChip(originChip, st.origin);
    metaText.textContent = renderForeignMeta(st);
    renderDiscovery(discoveryBox, name, discoverySignalFor(name).value, true);
  });
  return row;
}

/** Status dot for a read-only row. Split from applyStatusDot because that one
 *  reads the config record's `enabled` flag, and there is no record here — the
 *  server's own reported state is the only input. */
function applyStatusDotForeign(dot: HTMLSpanElement, name: string, st: RuntimeStatus): void {
  dot.className = "mcp-dot";
  const meta = STATUS_META[st.state] ?? STATUS_META.idle; // eslint-disable-line @typescript-eslint/no-unnecessary-condition
  dot.classList.add(meta.css);
  if (isFailedWithError(st)) {
    dot.title = `Failed to initialise: ${st.error}`;
    dot.setAttribute("aria-label", `${name}: failed — ${st.error}`);
    return;
  }
  dot.title = meta.title;
  dot.setAttribute("aria-label", `${name}: ${meta.title.toLowerCase()}`);
}

/** Meta text for a read-only row. Exported for testing. */
export function renderForeignMeta(st: RuntimeStatus): string {
  if (isFailedWithError(st)) {
    return `Failed to start — ${st.error}`;
  }
  switch (st.state) {
    case "connected":
      return "Connected — its tools are available to the agent";
    case "needs_auth":
      return "Waiting for sign-in";
    case "disabled":
      return "Disabled — the agent is not using it";
    default:
      return "Not connected";
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
  // The origin chip is a held element beside the meta text rather than part of
  // it: renderMeta stays a pure string function (unit-testable, no DOM), and the
  // chip keeps its own identity across effect runs so it is patched, not rebuilt.
  const originChip = buildOriginChip();
  const metaText = el("span", { className: "mcp-row-meta-text" });
  const meta = el("div", { className: "mcp-row-meta" }, originChip, metaText);
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
  let oauthRelay: HTMLDetailsElement | null = null;
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
      applyOriginChip(originChip, st.origin);
      metaText.textContent = renderMeta(cur, st);

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
        // The relay is offered only while a callback could still be delivered.
        // Once this attempt's code has been relayed the box goes away: the code
        // is spent, so a second paste can only fail, and leaving the field there
        // would invite exactly that.
        if (st.relayed) {
          oauthRelay?.remove();
          oauthRelay = null;
        } else if (oauthRelay === null) {
          oauthRelay = renderOAuthRelay(cur.name);
          body.appendChild(oauthRelay);
        }
      } else if (oauthPill !== null || oauthRelay !== null) {
        oauthPill?.remove();
        oauthPill = null;
        oauthUrl = null;
        oauthRelay?.remove();
        oauthRelay = null;
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
  idle: { css: "idle", title: "Not connected — no chat is running" },
  failed: { css: "failed", title: "Failed to initialise" },
  disabled: { css: "disabled", title: "Disabled" },
};

/** What each non-user origin says on the row. `user` has no entry: the config
 *  list already owns that row, so a chip declaring the obvious would sit on
 *  every row and mean nothing. */
const ORIGIN_META: Readonly<Record<Exclude<Origin, "user">, { label: string; title: string }>> = {
  power: {
    label: "from a Power",
    title:
      "An installed Power contributed this server. Manage it where the Power is installed — this page cannot edit or remove it.",
  },
  unknown: {
    label: "not managed here",
    title:
      "The agent reported this server, but it is not in this page's configuration. It comes from a config vibekit does not manage, so it cannot be edited or removed here.",
  },
};

/** Build the (initially hidden) provenance chip. */
function buildOriginChip(): HTMLSpanElement {
  return el("span", { className: "mcp-origin", hidden: true });
}

/** Show or hide the provenance chip for an origin. Exported for testing. */
export function applyOriginChip(chip: HTMLSpanElement, origin: Origin): void {
  if (origin === "user") {
    chip.hidden = true;
    chip.textContent = "";
    chip.removeAttribute("title");
    return;
  }
  const meta = ORIGIN_META[origin];
  chip.hidden = false;
  chip.textContent = meta.label;
  chip.title = meta.title;
}

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
  const source = s.transport === "stdio" ? (s.command ?? "") : (s.url ?? "");
  if (isFailedWithError(st)) {
    return `${source} — ${st.error}`;
  }
  return source;
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

/** The rescue box for a sign-in whose redirect landed on the wrong machine.
 *
 *  WHY IT EXISTS. KAS binds its OAuth redirect listener on the CONTAINER's
 *  localhost. A browser reaching vibekit from a phone or another laptop is sent
 *  to ITS OWN localhost, where nothing answers, so the sign-in dies on a
 *  connection-refused page and clicking the pill again just repeats it. For a
 *  container reached over the network that is the normal case.
 *
 *  Inline, not a modal: the address being pasted is on the clipboard from
 *  another tab, and the refusal has to land next to the field that caused it.
 *  The details element is collapsed by default so a local-browser sign-in (which
 *  needs none of this) sees one line rather than a form. */
export function renderOAuthRelay(serverName: string): HTMLDetailsElement {
  const input = el("input", {
    type: "url",
    className: "mcp-relay-input",
    // No `required`/`pattern`: every rejection is the server's to make against
    // the authorization URL it stored, and a browser-side pattern would only
    // duplicate part of that rule and drift from it.
    placeholder: "http://localhost:1234/oauth/callback?code=…",
    "aria-label": "The address the sign-in page could not reach",
    autocomplete: "off",
    spellcheck: "false",
  }) as HTMLInputElement;

  const note = el("p", { className: "mcp-relay-note" }) as HTMLParagraphElement;
  const setNote = (text: string, kind: "err" | "ok" | "") => {
    note.textContent = text;
    note.classList.toggle("mcp-relay-err", kind === "err");
    note.classList.toggle("mcp-relay-ok", kind === "ok");
  };

  const submit = el(
    "button",
    { type: "button", className: "btn-small" },
    "Finish",
  ) as HTMLButtonElement;
  submit.addEventListener("click", () => {
    const pasted = input.value.trim();
    if (pasted === "") {
      setNote("Paste the address from the page that failed to load.", "err");
      return;
    }
    submit.disabled = true;
    setNote("Delivering…", "");
    void relayOAuthCallback
      .dispatch(
        { server: serverName, redirect_url: pasted },
        {
          onSuccess: () => {
            // The code is delivered; the token exchange is still KAS's to
            // finish. So this says DELIVERED and refetches — claiming
            // "connected" here would be inventing a state transition that only
            // `_kiro/mcp/status` can report.
            setNote("Delivered. Waiting for the server to finish signing in…", "ok");
            input.value = "";
            mcpState.refetchStatus();
          },
          // The server's reason names which part of the address was wrong, and
          // it is the only thing that can: it is checked against the
          // authorization URL KAS stored, which the client never sees.
          onError: (err) => {
            setNote(err.message, "err");
          },
        },
      )
      .finally(() => {
        submit.disabled = false;
      });
  });

  const box = el(
    "details",
    { className: "mcp-relay" },
    el("summary", {}, "The sign-in page did not load?"),
    el(
      "p",
      { className: "mcp-relay-help" },
      "The sign-in redirects to an address only this container can reach. Copy the " +
        "whole address from the page that failed to load and paste it here.",
    ),
    el("div", { className: "mcp-relay-row" }, input, submit),
    note,
  ) as HTMLDetailsElement;
  return box;
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
        `Remove "${s.name}"? The agent loses access to this integration immediately.`,
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
    mcpState.setStatusFromEvent(p.server, { name: p.server, state: "connected" });
    // Pull the newly-connected server's advertised prompts/resources (they
    // ride /api/mcp/status, not the mcp_connected payload) — and, for a server
    // this page does not configure, the origin that makes its row appear.
    mcpState.refetchStatus();
  });
  onSSE("mcp_oauth_needed", (_chat, p) => {
    mcpState.setStatusFromEvent(p.server, {
      name: p.server,
      state: "needs_auth",
      oauth_url: p.url,
      // A fresh authorization attempt, so the relay latch clears with it — the
      // server's recordOAuth replaces the whole record for the same reason. A
      // stale `true` here would hide the paste box for a code that was never
      // delivered, which is the one state the user cannot recover from.
      relayed: false,
    });
  });
  onSSE("mcp_failed", (_chat, p) => {
    // Read the PREVIOUS state before writing the new one: the toast fires on the
    // transition into `failed`, not on the frame. See announceMCPFailure.
    announceMCPFailure(p.server, p.error, statusSignalFor(p.server).peek().state);
    mcpState.setStatusFromEvent(p.server, { name: p.server, state: "failed", error: p.error });
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

// --- Broken-server notice ---
//
// A toast when a server turns out to be broken, and NO proactive probing. Not
// every chat uses an MCP server, so an MCP problem must not block or complicate
// the chat flow, and a health page is a different surface. What arrives here is
// the failure kiro-cli already reported (`_kiro/mcp/status` carrying
// `_kiro.dev/mcp/server_init_failure`), so the state, the reason and the delivery
// mechanism all existed and only the notice did not.
//
// This joins the one existing `mcp_failed` consumer rather than adding a second
// subscription to the same event: that handler already holds both the server name
// and the reason, and it is where the previous state can still be read.

/** DEDUPE IS REQUIRED, and it keys on the state TRANSITION rather than on the
 *  frame. Each bridge emits its own `_kiro/mcp/status` on connect and
 *  `recordInitFailure` broadcasts unconditionally, so a reconnect storm is a
 *  broadcast storm; without this a wedged server would produce one toast per
 *  bridge per reconnect. Leaving `failed` re-arms it, which is what keeps the
 *  next genuine failure audible. */
export function announceMCPFailure(server: string, reason: string, prevState: RuntimeState): void {
  if (prevState === "failed") {
    return;
  }
  showToast(mcpFailureText(server, reason), "error");
}

/** The captured reason, not a generic message: `error` is kiro-cli's own text and
 *  it is what separates "command not found" from a handshake timeout. It CAN be
 *  empty (adaptStatus defaults a missing one to ""), so the fallback still names
 *  the server — a toast that says only "an integration failed" sends the reader
 *  looking for which one. */
export function mcpFailureText(server: string, reason: string): string {
  const trimmed = reason.trim();
  if (trimmed === "") {
    return `Integration "${server}" failed to start.`;
  }
  return `Integration "${server}" failed to start: ${trimmed}`;
}
