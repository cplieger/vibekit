// ---------------------------------------------------------------------------
// MCP panels: add/edit modal forms (registry search, npm, remote, raw JSON),
// submit helpers, and key/value pair editors.
// ---------------------------------------------------------------------------

import { $, el } from "./dom.js";
import { closeModal, RollingOutput } from "./modals.js";
import { type Server, type KeyPair, type Transport, mcpState } from "./mcp-state.js";
import { renderKeyPairList, appendKeyPair, collectKeyPairs } from "./mcp-pairs.js";
import { buildChip } from "./ui-primitives.js";
import { saveServer, searchRegistry } from "./actions/mcp.js";
import { enableTool, getToolsStatus } from "./actions/tools.js";
import { subscribeToActions, bindLoadingState } from "./actions/index.js";
import type { ActionErrorLike } from "./actions/index.js";
import { initSearchPanel, setSwitchMode, cleanupSearch } from "./mcp-panels-search.js";

// --- Add / edit modal ---

export type AddMode = "search" | "remote" | "npm" | "raw";

interface EditingContext {
  id: string;
}

class EditSession {
  editing: EditingContext = { id: "" };
  disabledToolsList: string[] = [];

  reset(): void {
    this.editing = { id: "" };
    this.disabledToolsList = [];
  }

  startEdit(id: string): void {
    this.reset();
    this.editing = { id };
  }

  startAdd(): void {
    this.reset();
  }
}

const session = new EditSession();

/** Cancel any in-flight work and tear down search subscription when
 *  the modal is dismissed (close button, Escape, or overlay click). */
export function cleanupModal(): void {
  cleanupSearch();
  searchRegistry.cancel();
}

export function setEditing(ctx: EditingContext): void {
  if (ctx.id === "") {
    session.startAdd();
  } else {
    session.startEdit(ctx.id);
  }
}

interface InitArgs {
  mode: AddMode;
  server: Server | null;
}

export function initModal(args: InitArgs): void {
  const title = el<HTMLSpanElement>("mcp-modal-title");
  title.textContent = session.editing.id === "" ? "Connect integration" : "Edit integration";

  const tabs = el<HTMLDivElement>("mcp-modal-tabs");
  tabs.classList.toggle("hidden", session.editing.id !== "");
  for (const btn of tabs.querySelectorAll<HTMLButtonElement>(".mcp-modal-tab")) {
    btn.classList.toggle("active", btn.dataset["mcpMode"] === args.mode);
    btn.onclick = (): void => {
      setMode(btn.dataset["mcpMode"] as AddMode, null);
    };
  }

  initDisabledToolsSection(args.server);
  setMode(args.mode, args.server);
}

const PANEL_MODES: Readonly<
  Record<AddMode, { transport: Transport | null; init: (existing: Server | null) => void }>
> = {
  search: {
    transport: null,
    init: () => {
      initSearchPanel();
    },
  },
  remote: {
    transport: "http",
    init: (s) => {
      initRemotePanel(s);
    },
  },
  npm: {
    transport: "stdio",
    init: (s) => {
      initNpmPanel(s);
    },
  },
  raw: {
    transport: "stdio",
    init: (s) => {
      initRawPanel(s);
    },
  },
};

function setMode(mode: AddMode, existing: Server | null): void {
  for (const panel of document.querySelectorAll<HTMLDivElement>("[data-mcp-mode]")) {
    panel.classList.toggle("hidden", panel.dataset["mcpMode"] !== mode);
  }
  for (const btn of document.querySelectorAll<HTMLButtonElement>(".mcp-modal-tab")) {
    btn.classList.toggle("active", btn.dataset["mcpMode"] === mode);
  }

  PANEL_MODES[mode].init(existing);
}

// --- Submit helpers ---

async function submitServer(
  body: Partial<Server>,
  errEl: HTMLElement,
  saveBtn: HTMLButtonElement | null,
): Promise<boolean> {
  errEl.classList.add("hidden");
  errEl.textContent = "";

  if (session.editing.id !== "") {
    body.disabled_tools = session.disabledToolsList;
  }

  const unbind =
    saveBtn != null
      ? bindLoadingState("mcp.save_server", saveBtn, { pendingClass: "btn-loading" })
      : undefined;

  let capturedError: ActionErrorLike | undefined;
  const unsub = subscribeToActions((inst) => {
    if (inst.name === "mcp.save_server" && inst.status === "error") {
      capturedError = inst.error;
    }
  });

  const r = await saveServer.dispatch(
    { id: session.editing.id, body },
    {
      onSettled: () => {
        unsub();
        unbind?.();
      },
    },
  );

  if (r === null) {
    errEl.textContent = capturedError?.message ?? "Save failed.";
    errEl.classList.remove("hidden");
    return false;
  }
  closeModal($.mcpModal);
  mcpState.refetchServers();
  return true;
}

// --- Panel: registry search (delegated to mcp-panels-search.ts) ---

// Wire the switch-mode callback so search results can switch panels.
setSwitchMode((kind, slug, identifier, fields) => {
  if (kind === "npm") {
    setMode("npm", null);
    fillNpmForm(slug, identifier, fields);
  } else {
    setMode("remote", null);
    fillRemoteForm(slug, kind === "sse" ? "sse" : "http", identifier, fields);
  }
});

// --- Panel: npm (stdio via npx) ---

function initNpmPanel(existing: Server | null): void {
  const name = el<HTMLInputElement>("mcp-npm-name");
  const pkg = el<HTMLInputElement>("mcp-npm-pkg");
  const prewarm = el<HTMLInputElement>("mcp-npm-prewarm");
  const envList = el<HTMLDivElement>("mcp-npm-env");
  const errEl = el<HTMLParagraphElement>("mcp-npm-error");
  errEl.classList.add("hidden");
  errEl.textContent = "";

  // npx-based MCP servers need the Node runtime, which is opt-in. Probe
  // and, if missing, show an inline install affordance gating the form.
  void gateNpmPanelOnNode();

  if (existing !== null) {
    name.value = existing.name;
    pkg.value = extractNpxPackage(existing);
    prewarm.checked = existing.prewarm === true;
    renderKeyPairList(envList, existing.env ?? [], "env");
  } else {
    name.value = "";
    pkg.value = "";
    prewarm.checked = true;
    renderKeyPairList(envList, [], "env");
  }

  el<HTMLButtonElement>("mcp-npm-add-env").onclick = (): void => {
    appendKeyPair(envList, { name: "", value: "" }, "env");
  };

  el<HTMLButtonElement>("mcp-npm-save").onclick = (): void => {
    const args = ["-y", pkg.value.trim()].filter((a) => a !== "");
    const transport: Transport = PANEL_MODES.npm.transport!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    void submitServer(
      {
        transport,
        name: name.value.trim(),
        command: "npx",
        args,
        env: collectKeyPairs(envList),
        prewarm: prewarm.checked,
        enabled: existing?.enabled ?? true,
      },
      errEl,
      el<HTMLButtonElement>("mcp-npm-save"),
    );
  };
}

// Probe Node availability and, when missing, render an inline banner
// inside the npm panel that installs the Node runtime on click. The
// package fields stay usable (the user can fill them in while Node
// installs), but the banner makes the dependency explicit and the
// install one-click. After a successful enable the banner removes
// itself. Mirrors the Sources sub-tab's auto-install-on-intent flow.
async function gateNpmPanelOnNode(): Promise<void> {
  const panel = document.querySelector<HTMLDivElement>('[data-mcp-mode="npm"]');
  if (panel === null) {
    return;
  }
  const existingBanner = panel.querySelector(".mcp-node-banner");
  if (existingBanner !== null) {
    existingBanner.remove();
  }

  const status = await getToolsStatus.dispatch();
  if (status !== null && status["npx"] === true) {
    return; // Node already present, nothing to do.
  }

  const banner = document.createElement("div");
  banner.className = "mcp-node-banner inline-install-banner";
  const msg = document.createElement("p");
  msg.className = "section-hint";
  msg.textContent =
    "npx-based MCP servers need the Node.js runtime (~100 MB). It isn't installed yet.";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small btn-primary";
  btn.textContent = "Install Node.js runtime";
  const out = document.createElement("div");
  out.className = "rolling-output hidden";

  btn.addEventListener("click", () => {
    void (async () => {
      btn.disabled = true;
      const roll = new RollingOutput(out, "git-output-modal");
      out.classList.remove("hidden");
      roll.append("Installing Node.js runtime…");
      const d = await enableTool.dispatch({ section: "runtimes", name: "node" });
      if (d === null || d.error !== undefined) {
        roll.append(`Install failed${d?.error !== undefined ? `: ${d.error}` : ""}`);
        btn.disabled = false;
        return;
      }
      roll.append(d.output ?? "");
      // Re-probe; if npx is now present, drop the banner.
      const after = await getToolsStatus.dispatch();
      if (after !== null && after["npx"] === true) {
        banner.remove();
      } else {
        btn.disabled = false;
      }
    })();
  });

  banner.append(msg, btn, out);
  panel.prepend(banner);
}

function fillNpmForm(
  name: string,
  pkg: string,
  fields: {
    name: string;
    description?: string | undefined;
    required?: boolean | undefined;
    secret?: boolean | undefined;
  }[],
): void {
  el<HTMLInputElement>("mcp-npm-name").value = name;
  el<HTMLInputElement>("mcp-npm-pkg").value = pkg;
  el<HTMLInputElement>("mcp-npm-prewarm").checked = true;
  const list = el<HTMLDivElement>("mcp-npm-env");
  renderKeyPairList(
    list,
    fields.map((f) => ({ name: f.name, value: "" })),
    "env",
  );
}

export function extractNpxPackage(s: Server): string {
  for (const arg of s.args ?? []) {
    const a = arg.trim();
    if (a === "" || a === "-y" || a === "--yes") {
      continue;
    }
    return a;
  }
  return "";
}

// --- Panel: remote (http/sse) ---

function initRemotePanel(existing: Server | null): void {
  const name = el<HTMLInputElement>("mcp-remote-name");
  const typeSel = el<HTMLSelectElement>("mcp-remote-type");
  const url = el<HTMLInputElement>("mcp-remote-url");
  const oauthClientID = el<HTMLInputElement>("mcp-remote-oauth-client-id");
  const headers = el<HTMLDivElement>("mcp-remote-headers");
  const errEl = el<HTMLParagraphElement>("mcp-remote-error");
  errEl.classList.add("hidden");
  errEl.textContent = "";

  if (existing !== null) {
    name.value = existing.name;
    typeSel.value = existing.transport === "sse" ? "sse" : "http";
    url.value = existing.url ?? "";
    oauthClientID.value = existing.oauth_client_id ?? "";
    renderKeyPairList(headers, existing.headers ?? [], "header");
  } else {
    name.value = "";
    typeSel.value = PANEL_MODES.remote.transport!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    url.value = "";
    oauthClientID.value = "";
    renderKeyPairList(headers, [], "header");
  }

  el<HTMLButtonElement>("mcp-remote-add-header").onclick = (): void => {
    appendKeyPair(headers, { name: "", value: "" }, "header");
  };

  el<HTMLButtonElement>("mcp-remote-save").onclick = (): void => {
    const transport: Transport = typeSel.value === "sse" ? "sse" : "http";
    const body: Partial<Server> = {
      transport,
      name: name.value.trim(),
      url: url.value.trim(),
      headers: collectKeyPairs(headers),
      enabled: existing?.enabled ?? true,
    };
    const oauthID = oauthClientID.value.trim();
    if (oauthID !== "") {
      body.oauth_client_id = oauthID;
    }
    void submitServer(body, errEl, el<HTMLButtonElement>("mcp-remote-save"));
  };
}

function fillRemoteForm(
  name: string,
  type: Transport,
  url: string,
  fields: {
    name: string;
    description?: string | undefined;
    required?: boolean | undefined;
    secret?: boolean | undefined;
  }[],
): void {
  el<HTMLInputElement>("mcp-remote-name").value = name;
  el<HTMLSelectElement>("mcp-remote-type").value = type;
  el<HTMLInputElement>("mcp-remote-url").value = url;
  const list = el<HTMLDivElement>("mcp-remote-headers");
  renderKeyPairList(
    list,
    fields.map((f) => ({ name: f.name, value: "" })),
    "header",
  );
}

// --- Panel: raw JSON ---

function initRawPanel(existing: Server | null): void {
  const textarea = el<HTMLTextAreaElement>("mcp-raw-input");
  const err = el<HTMLParagraphElement>("mcp-raw-error");
  err.classList.add("hidden");
  err.textContent = "";

  if (existing !== null) {
    textarea.value = JSON.stringify(rawEditShape(existing), null, 2);
  } else {
    textarea.value = RAW_TEMPLATE;
  }

  el<HTMLButtonElement>("mcp-raw-save").onclick = (): void => {
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(textarea.value) as Record<string, unknown>;
    } catch (e: unknown) {
      err.textContent = "Invalid JSON: " + (e instanceof Error ? e.message : String(e));
      err.classList.remove("hidden");
      return;
    }
    const body = rawSubmitShape(parsed);
    if (body === null) {
      err.textContent = "JSON must include { name, command, args, env? } for a stdio server.";
      err.classList.remove("hidden");
      return;
    }
    body.enabled = existing?.enabled ?? true;
    void submitServer(body, err, el<HTMLButtonElement>("mcp-raw-save"));
  };
}

const RAW_TEMPLATE = `{
  "name": "my-server",
  "command": "/path/to/binary",
  "args": ["--flag", "value"],
  "env": {
    "MY_TOKEN": "..."
  }
}
`;

export function rawEditShape(s: Server): Record<string, unknown> {
  const env: Record<string, string> = {};
  for (const kv of s.env ?? []) {
    env[kv.name] = kv.value;
  }
  return {
    name: s.name,
    command: s.command ?? "",
    args: s.args ?? [],
    env,
    prewarm: s.prewarm ?? false,
  };
}

export function rawSubmitShape(parsed: Record<string, unknown>): Partial<Server> | null {
  const name = typeof parsed["name"] === "string" ? parsed["name"] : "";
  const command = typeof parsed["command"] === "string" ? parsed["command"] : "";
  if (name === "" || command === "") {
    return null;
  }
  const argsIn = parsed["args"];
  const args = Array.isArray(argsIn)
    ? argsIn.filter((a): a is string => typeof a === "string")
    : [];
  const envIn = parsed["env"];
  const env: KeyPair[] = [];
  if (typeof envIn === "object" && envIn !== null) {
    for (const [k, v] of Object.entries(envIn)) {
      if (typeof v === "string") {
        env.push({ name: k, value: v });
      }
    }
  }
  const transport: Transport = PANEL_MODES.raw.transport!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  const prewarm = parsed["prewarm"] === true;
  return { transport, name, command, args, env, prewarm };
}

// --- Disabled tools chip list ---

function initDisabledToolsSection(server: Server | null): void {
  const section = el<HTMLDivElement>("mcp-disabled-tools");
  const chips = el<HTMLDivElement>("mcp-disabled-chips");
  const input = el<HTMLInputElement>("mcp-disabled-input");
  const addBtn = el<HTMLButtonElement>("mcp-disabled-add");

  if (server === null) {
    section.classList.add("hidden");
    session.disabledToolsList = [];
    return;
  }

  section.classList.remove("hidden");
  session.disabledToolsList = [...(server.disabled_tools ?? [])];
  const knownTools = server.known_tools ?? [];
  renderDisabledChips(chips, section, knownTools);

  const add = (): void => {
    const name = input.value.trim();
    if (name === "" || session.disabledToolsList.includes(name)) {
      return;
    }
    session.disabledToolsList.push(name);
    input.value = "";
    renderDisabledChips(chips, section, knownTools);
  };

  addBtn.onclick = add;
  input.onkeydown = (e: KeyboardEvent): void => {
    if (e.key === "Enter") {
      e.preventDefault();
      add();
    }
  };

  // Render known tools as clickable suggestions below the input.
  renderToolSuggestions(section, server.known_tools ?? [], chips);
}

function renderToolSuggestions(
  section: HTMLDivElement,
  knownTools: string[],
  chips: HTMLDivElement,
): void {
  let suggestionsEl = section.querySelector(".mcp-tool-suggestions");
  if (suggestionsEl !== null) {
    suggestionsEl.remove();
  }
  const available = knownTools.filter((t) => !session.disabledToolsList.includes(t));
  if (available.length === 0) {
    return;
  }

  suggestionsEl = document.createElement("div");
  suggestionsEl.className = "mcp-tool-suggestions";
  const label = document.createElement("span");
  label.className = "mcp-tool-suggestions-label";
  label.textContent = "Available:";
  suggestionsEl.appendChild(label);

  for (const name of available) {
    const pill = document.createElement("button");
    pill.type = "button";
    pill.className = "action-pill mono";
    pill.textContent = name;
    pill.addEventListener("click", () => {
      if (!session.disabledToolsList.includes(name)) {
        session.disabledToolsList.push(name);
        renderDisabledChips(chips, section, knownTools);
        renderToolSuggestions(section, knownTools, chips);
      }
    });
    suggestionsEl.appendChild(pill);
  }
  section.appendChild(suggestionsEl);
}

function renderDisabledChips(
  container: HTMLDivElement,
  section?: HTMLDivElement,
  knownTools?: string[],
): void {
  container.replaceChildren();
  for (const name of session.disabledToolsList) {
    container.appendChild(
      buildChip({
        label: name,
        code: true,
        chipClass: "chip mono",
        removeTitle: "Unblock",
        onRemove: () => {
          session.disabledToolsList = session.disabledToolsList.filter((n) => n !== name);
          renderDisabledChips(container, section, knownTools);
          if (section !== undefined && knownTools !== undefined) {
            renderToolSuggestions(section, knownTools, container);
          }
        },
      }),
    );
  }
}
