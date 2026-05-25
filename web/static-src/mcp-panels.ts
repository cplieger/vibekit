// ---------------------------------------------------------------------------
// MCP panels: add/edit modal forms (registry search, npm, remote, raw JSON),
// submit helpers, and key/value pair editors.
// ---------------------------------------------------------------------------

import { $, el } from "./dom.js";
import { closeModal } from "./modals.js";
import { type Server, type KeyPair, type Transport, refetchServers } from "./mcp-state.js";
import { renderKeyPairList, appendKeyPair, collectKeyPairs } from "./mcp-pairs.js";
import { buildChip } from "./ui-primitives.js";
import { saveServer, searchRegistry, type RegistrySearchResult } from "./actions/mcp.js";
import { subscribeToActions, bindLoadingState, debouncedDispatch } from "./actions/index.js";
import type { ActionErrorLike } from "./actions/index.js";
import type { DebouncedDispatch } from "./actions/index.js";
import { registerCleanup } from "./actions/cleanup.js";

// --- Add / edit modal ---

export type AddMode = "search" | "remote" | "npm" | "raw";

export interface EditingContext {
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

let session = new EditSession();

/** Cancel any in-flight work and tear down search subscription when
 *  the modal is dismissed (close button, Escape, or overlay click). */
export function cleanupModal(): void {
  debouncedSearch?.cancel();
  searchRegistry.cancel();
  searchUnsub?.();
  searchUnsub = null;
}

export function setEditing(ctx: EditingContext): void {
  if (ctx.id === "") {
    session.startAdd();
  } else {
    session.startEdit(ctx.id);
  }
}

export interface InitArgs { mode: AddMode; server: Server | null }

export function initModal(args: InitArgs): void {
  const title = el<HTMLSpanElement>("mcp-modal-title");
  title.textContent = session.editing.id === "" ? "Connect integration" : "Edit integration";

  const tabs = el<HTMLDivElement>("mcp-modal-tabs");
  tabs.classList.toggle("hidden", session.editing.id !== "");
  for (const btn of tabs.querySelectorAll<HTMLButtonElement>(".mcp-modal-tab")) {
    btn.classList.toggle("active", btn.dataset["mcpMode"] === args.mode);
    btn.onclick = (): void => setMode(btn.dataset["mcpMode"] as AddMode, null);
  }

  initDisabledToolsSection(args.server);
  setMode(args.mode, args.server);
}

const PANEL_MODES: Readonly<Record<AddMode, { transport: Transport | null; init: (existing: Server | null) => void }>> = {
  search: { transport: null,    init: () => initSearchPanel() },
  remote: { transport: "http",  init: (s) => initRemotePanel(s) },
  npm:    { transport: "stdio", init: (s) => initNpmPanel(s) },
  raw:    { transport: "stdio", init: (s) => initRawPanel(s) },
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

async function submitServer(body: Partial<Server>, errEl: HTMLElement, saveBtn: HTMLButtonElement | null): Promise<boolean> {
  errEl.classList.add("hidden");
  errEl.textContent = "";

  if (session.editing.id !== "") {
    body.disabled_tools = session.disabledToolsList;
  }

  const unbind = saveBtn != null
    ? bindLoadingState("mcp.save_server", saveBtn, { pendingClass: "btn-loading" })
    : undefined;

  let capturedError: ActionErrorLike | undefined;
  const unsub = subscribeToActions((inst) => {
    if (inst.name === "mcp.save_server" && inst.status === "error") {
      capturedError = inst.error;
    }
  });

  const r = await saveServer.dispatch({ id: session.editing.id, body });

  unsub();
  unbind?.();

  if (r === null) {
    errEl.textContent = capturedError?.message ?? "Save failed.";
    errEl.classList.remove("hidden");
    return false;
  }
  closeModal($.mcpModal);
  void refetchServers();
  return true;
}

// --- Panel: registry search ---

interface RegistryEntry {
  name: string;
  title?: string;
  description?: string;
  version?: string;
  repository?: string;
  packages?: Array<{
    registry_type: string;
    identifier: string;
    version?: string;
    env_vars?: Array<{ name: string; description?: string; required?: boolean; secret?: boolean }>;
  }>;
  remotes?: Array<{
    type: string;
    url: string;
    headers?: Array<{ name: string; description?: string; value?: string; required?: boolean; secret?: boolean }>;
  }>;
}

let debouncedSearch: DebouncedDispatch<{ q: string }> | null = null;
let searchUnsub: (() => void) | null = null;
registerCleanup(() => { debouncedSearch?.cancel(); searchUnsub?.(); });

function initSearchPanel(): void {
  const input = el<HTMLInputElement>("mcp-search-input");
  const results = el<HTMLDivElement>("mcp-search-results");
  const btn = el<HTMLButtonElement>("mcp-search-btn");
  input.value = "";
  results.replaceChildren();
  input.focus();

  // Tear down any prior subscription before re-installing — initSearchPanel
  // is called every time the user switches to the Search tab.
  searchUnsub?.();
  debouncedSearch = debouncedDispatch(searchRegistry, { wait: 200 });

  searchUnsub = subscribeToActions((inst) => {
    if (inst.name !== "mcp.search_registry") return;
    if (inst.status === "success") {
      const d = inst.result as RegistrySearchResult | undefined;
      const q = (inst.args as { q: string }).q;
      renderSearchResults(results, d, q);
    } else if (inst.status === "error") {
      renderSearchError(results, (inst.args as { q: string }).q);
    }
  });

  input.oninput = (): void => {
    const q = input.value.trim();
    if (q === "") { results.replaceChildren(); debouncedSearch!.cancel(); return; }
    debouncedSearch!({ q });
  };

  input.onkeydown = (e: KeyboardEvent): void => {
    if (e.key === "Enter") {
      e.preventDefault();
      const q = input.value.trim();
      if (q === "") return;
      debouncedSearch!.flush({ q });
    }
  };

  btn.onclick = (): void => {
    const q = input.value.trim();
    if (q === "") return;
    debouncedSearch!.flush({ q });
  };
}

function renderSearchResults(results: HTMLDivElement, d: RegistrySearchResult | undefined, q: string): void {
  results.replaceChildren();
  if (d === undefined || d === null) {
    renderSearchError(results, q);
    return;
  }
  if (d.servers.length === 0) {
    const empty = document.createElement("p");
    empty.className = "mcp-empty";
    empty.textContent = `No results for "${q}".`;
    results.appendChild(empty);
    return;
  }
  for (const entry of d.servers) results.appendChild(renderRegistryResult(entry));
}

function renderSearchError(results: HTMLDivElement, q: string): void {
  results.replaceChildren();
  const err = document.createElement("p");
  err.className = "mcp-empty";
  err.textContent = "Registry unreachable. Use the Remote URL or npm package forms instead.";
  results.appendChild(err);
  const retryBtn = document.createElement("button");
  retryBtn.type = "button";
  retryBtn.className = "btn-small";
  retryBtn.textContent = "Retry";
  bindLoadingState("mcp.search_registry", retryBtn);
  retryBtn.addEventListener("click", () => { void searchRegistry.dispatch({ q }); });
  results.appendChild(retryBtn);
}

function renderRegistryResult(entry: RegistryEntry): HTMLDivElement {
  const row = document.createElement("div");
  row.className = "mcp-result";

  const head = document.createElement("div");
  head.className = "mcp-result-head";
  const name = document.createElement("span");
  name.className = "mcp-result-name";
  name.textContent = entry.title ?? entry.name;
  const version = document.createElement("span");
  version.className = "mcp-result-version";
  version.textContent = entry.version ?? "";
  head.append(name, version);

  const desc = document.createElement("p");
  desc.className = "mcp-result-desc";
  desc.textContent = entry.description ?? entry.name;

  row.append(head, desc);

  for (const pkg of entry.packages ?? []) {
    row.appendChild(renderInstallBtn(entry, "npm", pkg.identifier, pkg.env_vars ?? []));
  }
  for (const rem of entry.remotes ?? []) {
    row.appendChild(renderInstallBtn(entry, rem.type, rem.url,
      (rem.headers ?? []).map((h) => ({ name: h.name, description: h.description, required: h.required, secret: h.secret }))));
  }

  return row;
}

function renderInstallBtn(
  entry: RegistryEntry,
  kind: string,
  identifier: string,
  fields: Array<{ name: string; description?: string | undefined; required?: boolean | undefined; secret?: boolean | undefined }>,
): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small mcp-install-btn";
  btn.textContent = `Use ${kind}: ${identifier}`;
  btn.addEventListener("click", () => {
    const slug = simplifyName(entry.name);
    if (kind === "npm") {
      setMode("npm", null);
      fillNpmForm(slug, identifier, fields);
    } else {
      setMode("remote", null);
      fillRemoteForm(slug, kind === "sse" ? "sse" : "http", identifier, fields);
    }
  });
  return btn;
}

export function simplifyName(full: string): string {
  const slash = full.lastIndexOf("/");
  const raw = slash >= 0 ? full.slice(slash + 1) : full;
  return raw.replace(/[^A-Za-z0-9_-]/g, "-").replace(/^-+|-+$/g, "").slice(0, 48) || "server";
}

// --- Panel: npm (stdio via npx) ---

function initNpmPanel(existing: Server | null): void {
  const name = el<HTMLInputElement>("mcp-npm-name");
  const pkg = el<HTMLInputElement>("mcp-npm-pkg");
  const prewarm = el<HTMLInputElement>("mcp-npm-prewarm");
  const envList = el<HTMLDivElement>("mcp-npm-env");
  const errEl = el<HTMLParagraphElement>("mcp-npm-error");
  errEl.classList.add("hidden");
  errEl.textContent = "";

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
    const transport: Transport = PANEL_MODES.npm.transport!;
    void submitServer({
      transport,
      name: name.value.trim(),
      command: "npx",
      args,
      env: collectKeyPairs(envList),
      prewarm: prewarm.checked,
      enabled: existing?.enabled ?? true,
    }, errEl, el<HTMLButtonElement>("mcp-npm-save"));
  };
}

function fillNpmForm(
  name: string, pkg: string,
  fields: Array<{ name: string; description?: string | undefined; required?: boolean | undefined; secret?: boolean | undefined }>,
): void {
  el<HTMLInputElement>("mcp-npm-name").value = name;
  el<HTMLInputElement>("mcp-npm-pkg").value = pkg;
  el<HTMLInputElement>("mcp-npm-prewarm").checked = true;
  const list = el<HTMLDivElement>("mcp-npm-env");
  renderKeyPairList(list, fields.map((f) => ({ name: f.name, value: "" })), "env");
}

export function extractNpxPackage(s: Server): string {
  for (const arg of s.args ?? []) {
    const a = arg.trim();
    if (a === "" || a === "-y" || a === "--yes") continue;
    return a;
  }
  return "";
}

// --- Panel: remote (http/sse) ---

function initRemotePanel(existing: Server | null): void {
  const name = el<HTMLInputElement>("mcp-remote-name");
  const typeSel = el<HTMLSelectElement>("mcp-remote-type");
  const url = el<HTMLInputElement>("mcp-remote-url");
  const headers = el<HTMLDivElement>("mcp-remote-headers");
  const errEl = el<HTMLParagraphElement>("mcp-remote-error");
  errEl.classList.add("hidden");
  errEl.textContent = "";

  if (existing !== null) {
    name.value = existing.name;
    typeSel.value = existing.transport === "sse" ? "sse" : "http";
    url.value = existing.url ?? "";
    renderKeyPairList(headers, existing.headers ?? [], "header");
  } else {
    name.value = "";
    typeSel.value = PANEL_MODES.remote.transport!;
    url.value = "";
    renderKeyPairList(headers, [], "header");
  }

  el<HTMLButtonElement>("mcp-remote-add-header").onclick = (): void => {
    appendKeyPair(headers, { name: "", value: "" }, "header");
  };

  el<HTMLButtonElement>("mcp-remote-save").onclick = (): void => {
    const transport: Transport = typeSel.value === "sse" ? "sse" : "http";
    void submitServer({
      transport,
      name: name.value.trim(),
      url: url.value.trim(),
      headers: collectKeyPairs(headers),
      enabled: existing?.enabled ?? true,
    }, errEl, el<HTMLButtonElement>("mcp-remote-save"));
  };
}

function fillRemoteForm(
  name: string, type: Transport, url: string,
  fields: Array<{ name: string; description?: string | undefined; required?: boolean | undefined; secret?: boolean | undefined }>,
): void {
  el<HTMLInputElement>("mcp-remote-name").value = name;
  el<HTMLSelectElement>("mcp-remote-type").value = type;
  el<HTMLInputElement>("mcp-remote-url").value = url;
  const list = el<HTMLDivElement>("mcp-remote-headers");
  renderKeyPairList(list, fields.map((f) => ({ name: f.name, value: "" })), "header");
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
      err.textContent =
        "JSON must include { name, command, args, env? } for a stdio server.";
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
  for (const kv of s.env ?? []) env[kv.name] = kv.value;
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
  if (name === "" || command === "") return null;
  const argsIn = parsed["args"];
  const args = Array.isArray(argsIn) ? argsIn.filter((a): a is string => typeof a === "string") : [];
  const envIn = parsed["env"];
  const env: KeyPair[] = [];
  if (typeof envIn === "object" && envIn !== null) {
    for (const [k, v] of Object.entries(envIn)) {
      if (typeof v === "string") env.push({ name: k, value: v });
    }
  }
  const transport: Transport = PANEL_MODES.raw.transport!;
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
    if (name === "" || session.disabledToolsList.includes(name)) return;
    session.disabledToolsList.push(name);
    input.value = "";
    renderDisabledChips(chips, section, knownTools);
  };

  addBtn.onclick = add;
  input.onkeydown = (e: KeyboardEvent): void => {
    if (e.key === "Enter") { e.preventDefault(); add(); }
  };

  // Render known tools as clickable suggestions below the input.
  renderToolSuggestions(section, server.known_tools ?? [], chips);
}

function renderToolSuggestions(section: HTMLDivElement, knownTools: string[], chips: HTMLDivElement): void {
  let suggestionsEl = section.querySelector(".mcp-tool-suggestions") as HTMLDivElement | null;
  if (suggestionsEl !== null) suggestionsEl.remove();
  const available = knownTools.filter((t) => !session.disabledToolsList.includes(t));
  if (available.length === 0) return;

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

function renderDisabledChips(container: HTMLDivElement, section?: HTMLDivElement, knownTools?: string[]): void {
  container.replaceChildren();
  for (const name of session.disabledToolsList) {
    container.appendChild(buildChip({
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
    }));
  }
}
