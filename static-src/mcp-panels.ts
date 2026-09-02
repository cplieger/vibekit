// ---------------------------------------------------------------------------
// MCP panels: add/edit modal forms (registry search, npm, remote, raw JSON),
// submit helpers, and key/value pair editors.
// ---------------------------------------------------------------------------

import { $, byId } from "./dom.js";
import { el } from "@cplieger/reactive";
import { closeModal, RollingOutput } from "./modals.js";
import { type Server, type Transport, mcpState, discoverySignalFor } from "./mcp-state.js";
import {
  type EditablePair,
  renderKeyPairList,
  appendKeyPair,
  collectKeyPairs,
} from "./mcp-pairs.js";
import { buildChip } from "./chip.js";
import {
  type ValidationField,
  importServers,
  saveServer,
  searchRegistry,
  validationFieldsOf,
} from "./actions/mcp.js";
import { getToolsStatus } from "./actions/tools.js";
import { installToolAndWait } from "./tools.js";
import { bindLoadingState } from "./actions/index.js";
import {
  type InstallField,
  initSearchPanel,
  setSwitchMode,
  cleanupSearch,
} from "./mcp-panels-search.js";

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
  const title = byId<HTMLSpanElement>("mcp-modal-title");
  title.textContent = session.editing.id === "" ? "Connect integration" : "Edit integration";

  const tabs = byId<HTMLDivElement>("mcp-modal-tabs");
  tabs.classList.toggle("hidden", session.editing.id !== "");
  for (const btn of tabs.querySelectorAll<HTMLButtonElement>(".mcp-modal-tab")) {
    btn.onclick = (): void => {
      setMode(btn.dataset["mcpMode"] as AddMode, null);
    };
  }

  initDisabledToolsSection(args.server);
  setMode(args.mode, args.server);
}

// Each mode's initialiser. There is no per-mode `transport` any more: the paste
// panel used to declare "stdio", which is what stopped a pasted remote server
// from going through it at all, and once that was gone the field had exactly one
// reader left. The npm form states its own transport at its save site.
const PANEL_MODES: Readonly<Record<AddMode, (existing: Server | null) => void>> = {
  search: () => {
    initSearchPanel();
  },
  remote: (s) => {
    initRemotePanel(s);
  },
  npm: (s) => {
    initNpmPanel(s);
  },
  raw: (s) => {
    initRawPanel(s);
  },
};

// A `data-mcp-mode` attribute marks TWO different things — one panel and one tab
// button per mode — so every selector over it has to say which. Both of these
// were written as bare `[data-mcp-mode]` and both were wrong for it: the panel
// loop below hid the tab BUTTONS (and, since `search` has no button, hid all of
// them, leaving the bar as an empty 5px strip nothing could reopen), and the npm
// panel's Node banner prepended itself into the npm tab button, which precedes
// the panel in the document.
const PANEL_SELECTOR = ".mcp-mode-panel[data-mcp-mode]";
const TAB_SELECTOR = ".mcp-modal-tab[data-mcp-mode]";

function setMode(mode: AddMode, existing: Server | null): void {
  // HTMLElement, not HTMLDivElement: the remote panel is a <form> (its password
  // field has to sit in one), and this loop only touches classList and dataset.
  for (const panel of document.querySelectorAll<HTMLElement>(PANEL_SELECTOR)) {
    panel.classList.toggle("hidden", panel.dataset["mcpMode"] !== mode);
  }
  for (const btn of document.querySelectorAll<HTMLButtonElement>(TAB_SELECTOR)) {
    btn.classList.toggle("active", btn.dataset["mcpMode"] === mode);
    btn.setAttribute("aria-selected", String(btn.dataset["mcpMode"] === mode));
  }

  PANEL_MODES[mode](existing);
}

// --- Validation failures ---
//
// The server accumulates across independent checks, so one response can name
// three bad fields. Printing three sentences above one box would leave the user
// hunting for which inputs they were about, so the field attribution is spent on
// MARKING the inputs and the messages sit under them as a list.

/** Wire field name -> the form input that holds it. The wire names are the ones
 *  the server already put in its messages (`oauth_client_secret`, `headers`), so
 *  this is a lookup rather than a translation. A field with no input here (a
 *  `transport` refusal on the raw-paste panel, say) still gets its message
 *  printed; only the mark is skipped. */
const FIELD_INPUT_IDS: Readonly<Record<string, readonly string[]>> = {
  name: ["mcp-remote-name", "mcp-npm-name"],
  url: ["mcp-remote-url"],
  command: ["mcp-npm-pkg"],
  args: ["mcp-npm-pkg"],
  transport: ["mcp-remote-transport"],
  headers: ["mcp-remote-headers"],
  env: ["mcp-npm-env"],
  oauth_client_id: ["mcp-remote-oauth-client-id"],
  oauth_client_secret: ["mcp-remote-oauth-client-secret"],
};

const CLS_FIELD_INVALID = "field-invalid";

/** Drop every mark a previous submit left. Runs at the top of each submit, so a
 *  field the user has since fixed stops claiming to be wrong. */
function clearFieldMarks(): void {
  for (const node of document.querySelectorAll<HTMLElement>("." + CLS_FIELD_INVALID)) {
    node.classList.remove(CLS_FIELD_INVALID);
    node.removeAttribute("aria-invalid");
  }
}

/** Mark the inputs the server named, and return the message lines to print. */
function markInvalidFields(fields: readonly ValidationField[]): string[] {
  const lines: string[] = [];
  for (const f of fields) {
    lines.push(f.message);
    for (const id of FIELD_INPUT_IDS[f.field] ?? []) {
      const node = document.getElementById(id);
      if (node === null) {
        continue;
      }
      node.classList.add(CLS_FIELD_INVALID);
      node.setAttribute("aria-invalid", "true");
    }
  }
  return lines;
}

/** Render a dispatch failure into an inline error element.
 *
 *  A validation failure lists every field the server named; anything else (a
 *  parse error, a name conflict, a network death) keeps the single-message shape
 *  it always had, which is why the field list is absent rather than empty on
 *  those paths. */
function showSubmitError(
  errEl: HTMLElement,
  err: { message: string; cause?: unknown } | undefined,
  fallback: string,
): void {
  const fields = validationFieldsOf(err);
  errEl.replaceChildren();
  if (fields.length > 1) {
    const lines = markInvalidFields(fields);
    errEl.appendChild(el("span", {}, `${String(lines.length)} problems to fix:`));
    const list = el("ul", { className: "mcp-error-list" });
    for (const line of lines) {
      list.appendChild(el("li", {}, line));
    }
    errEl.appendChild(list);
  } else {
    if (fields.length === 1) {
      markInvalidFields(fields);
    }
    errEl.textContent = err !== undefined ? err.message : fallback;
  }
  errEl.classList.remove("hidden");
}

// --- Submit helpers ---

async function submitServer(
  body: Partial<Server>,
  errEl: HTMLElement,
  saveBtn: HTMLButtonElement | null,
): Promise<boolean> {
  errEl.classList.add("hidden");
  errEl.replaceChildren();
  clearFieldMarks();

  if (session.editing.id !== "") {
    body.disabled_tools = session.disabledToolsList;
  }

  const unbind =
    saveBtn != null
      ? bindLoadingState("mcp.save_server", saveBtn, { pendingClass: "btn-loading" })
      : undefined;

  // The typed outcome carries THIS dispatch's terminal state, so a
  // concurrent save for another server can't cross-contaminate the inline
  // error (the previous subscribeToActions + name-filter capture could).
  const o = await saveServer.dispatch(
    { id: session.editing.id, body },
    {
      onSettled: () => {
        unbind?.();
      },
    },
  ).outcome;

  if (o.status !== "success") {
    showSubmitError(errEl, o.status === "error" ? o.error : undefined, "Save failed.");
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
    // Any non-npm registry hit lands on the remote panel. `kind` is the
    // normalised vibekit transport ("http" or "sse", mapped from the
    // registry's remote type by supportedRemoteTypes server-side), so we
    // preselect it in the panel's transport selector.
    setMode("remote", null);
    fillRemoteForm(slug, identifier, fields, kind);
  }
});

// --- Panel: npm (stdio via npx) ---

function initNpmPanel(existing: Server | null): void {
  const name = byId<HTMLInputElement>("mcp-npm-name");
  const pkg = byId<HTMLInputElement>("mcp-npm-pkg");
  const prewarm = byId<HTMLInputElement>("mcp-npm-prewarm");
  const envList = byId<HTMLDivElement>("mcp-npm-env");
  const errEl = byId<HTMLParagraphElement>("mcp-npm-error");
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

  byId<HTMLButtonElement>("mcp-npm-add-env").onclick = (): void => {
    appendKeyPair(envList, { name: "", value: "" }, "env");
  };

  byId<HTMLButtonElement>("mcp-npm-save").onclick = (): void => {
    const args = ["-y", pkg.value.trim()].filter((a) => a !== "");
    void submitServer(
      {
        transport: "stdio",
        name: name.value.trim(),
        command: "npx",
        args,
        env: collectKeyPairs(envList),
        prewarm: prewarm.checked,
        enabled: existing?.enabled ?? true,
      },
      errEl,
      byId<HTMLButtonElement>("mcp-npm-save"),
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
  const panel = document.querySelector<HTMLDivElement>('.mcp-mode-panel[data-mcp-mode="npm"]');
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

  const banner = el("div", { className: "mcp-node-banner inline-install-banner" });
  const msg = el(
    "p",
    { className: "section-hint" },
    "npx-based MCP servers need the Node.js runtime (~100 MB). It isn't installed yet.",
  );
  const btn = el(
    "button",
    { type: "button", className: "btn-small btn-primary" },
    "Install Node.js runtime",
  ) as HTMLButtonElement;
  const out = el("div", {
    className: "rolling-output hidden",
    role: "log",
    "aria-live": "polite",
    "aria-label": "Node install progress",
  }) as HTMLDivElement;

  // Disable the button while the install job runs (auto re-enabled on
  // settle); replaces the manual btn.disabled toggles.
  bindLoadingState("tools.ensure", btn);
  btn.addEventListener("click", () => {
    void (async () => {
      const roll = new RollingOutput(out, "git-output-modal");
      out.classList.remove("hidden");
      roll.append("Installing Node.js runtime…");
      const res = await installToolAndWait("node", (line) => {
        roll.append(line);
      });
      if (!res.ok) {
        roll.append(`Install failed${res.error !== undefined ? `: ${res.error}` : ""}`);
        return;
      }
      // Re-probe; if npx is now present, drop the banner.
      const after = await getToolsStatus.dispatch();
      if (after !== null && after["npx"] === true) {
        banner.remove();
      }
    })();
  });

  banner.append(msg, btn, out);
  panel.prepend(banner);
}

function fillNpmForm(name: string, pkg: string, fields: InstallField[]): void {
  byId<HTMLInputElement>("mcp-npm-name").value = name;
  byId<HTMLInputElement>("mcp-npm-pkg").value = pkg;
  byId<HTMLInputElement>("mcp-npm-prewarm").checked = true;
  renderKeyPairList(byId<HTMLDivElement>("mcp-npm-env"), declaredRows(fields), "env");
}

/** Carry the publisher's declared fields onto the form rows.
 *
 *  The names were already prefilled; the description, the required marker and
 *  the secret hint were thrown away here, which is why a server could install
 *  cleanly and then fail with nothing on screen saying it wanted a token. */
function declaredRows(fields: InstallField[]): EditablePair[] {
  return fields.map((f) => ({
    name: f.name,
    value: "",
    declared: {
      description: f.description,
      required: f.required,
      secret: f.secret,
    },
  }));
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

// --- Panel: remote (Streamable HTTP or legacy SSE; the transport is
// chosen via the panel's transport selector and emitted as the ACP
// `type` discriminator — kiro-cli v3/KAS accepts both http and sse) ---

/** Normalise a remote-panel transport-select value to a valid remote
 *  Transport. Only "http" and "sse" are remote transports; anything else
 *  (including undefined) falls back to the recommended "http". */
function remoteTransport(v: string | undefined): Transport {
  return v === "sse" ? "sse" : "http";
}

function initRemotePanel(existing: Server | null): void {
  const name = byId<HTMLInputElement>("mcp-remote-name");
  const url = byId<HTMLInputElement>("mcp-remote-url");
  const transportSel = byId<HTMLSelectElement>("mcp-remote-transport");
  const oauthClientID = byId<HTMLInputElement>("mcp-remote-oauth-client-id");
  const oauthClientSecret = byId<HTMLInputElement>("mcp-remote-oauth-client-secret");
  const headers = byId<HTMLDivElement>("mcp-remote-headers");
  const errEl = byId<HTMLParagraphElement>("mcp-remote-error");
  errEl.classList.add("hidden");
  errEl.textContent = "";

  if (existing !== null) {
    name.value = existing.name;
    url.value = existing.url ?? "";
    oauthClientID.value = existing.oauth_client_id ?? "";
    oauthClientSecret.value = existing.oauth_client_secret ?? "";
    renderKeyPairList(headers, existing.headers ?? [], "header");
  } else {
    name.value = "";
    url.value = "";
    oauthClientID.value = "";
    oauthClientSecret.value = "";
    renderKeyPairList(headers, [], "header");
  }
  // Preselect the stored transport (http/sse); default to http for a new
  // server. A stdio server never reaches this panel (openEditModal routes
  // it to the npm panel), so existing.transport is always http or sse here.
  transportSel.value = remoteTransport(existing?.transport);

  byId<HTMLButtonElement>("mcp-remote-add-header").onclick = (): void => {
    appendKeyPair(headers, { name: "", value: "" }, "header");
  };

  // The SUBMIT event, not the button's click. The remote panel is a real <form>
  // (a password field outside one is what Chromium's "[DOM] Password field is not
  // contained in a form" warns about), so Save is `type="submit"` and Enter in any
  // field reaches the same path — which is what a form is for. `preventDefault` is
  // required: the default action navigates.
  remotePanel().onsubmit = (ev: SubmitEvent): void => {
    ev.preventDefault();
    const body: Partial<Server> = {
      transport: remoteTransport(transportSel.value),
      name: name.value.trim(),
      url: url.value.trim(),
      headers: collectKeyPairs(headers),
      enabled: existing?.enabled ?? true,
    };
    const oauthID = oauthClientID.value.trim();
    if (oauthID !== "") {
      body.oauth_client_id = oauthID;
    }
    // Secret round-trips as "***" when already stored; sending it back
    // unchanged preserves it server-side (mergeSecret), any other value
    // replaces it, empty leaves it untouched on create / clears intent.
    const oauthSecret = oauthClientSecret.value.trim();
    if (oauthSecret !== "") {
      body.oauth_client_secret = oauthSecret;
    }
    void submitServer(body, errEl, byId<HTMLButtonElement>("mcp-remote-save"));
  };
}

/** The remote panel's form element. Resolved by the same `[data-mcp-mode]`
 *  attribute the panel loop uses, so there is one way to name a panel. */
function remotePanel(): HTMLFormElement {
  const form = document.querySelector<HTMLFormElement>(
    'form.mcp-mode-panel[data-mcp-mode="remote"]',
  );
  if (form === null) {
    throw new Error("mcp: remote panel form missing");
  }
  return form;
}

function fillRemoteForm(
  name: string,
  url: string,
  fields: InstallField[],
  transportHint?: string,
): void {
  byId<HTMLInputElement>("mcp-remote-name").value = name;
  byId<HTMLInputElement>("mcp-remote-url").value = url;
  byId<HTMLSelectElement>("mcp-remote-transport").value = remoteTransport(transportHint);
  renderKeyPairList(byId<HTMLDivElement>("mcp-remote-headers"), declaredRows(fields), "header");
}

// --- Panel: paste a block ---
//
// Every MCP server's README hands out a JSON block, and this is where it goes.
// The panel does NOT translate it: the server owns that (internal/mcp/paste.go),
// because the translation and the naming of an unknown key are one job and it
// belongs at the decode boundary. So this parses only far enough to catch
// invalid JSON without a round trip, then posts the object unchanged.
//
// A block may name SEVERAL servers, and pasting installs all of them — the block
// is one artifact the user copied out of one README, so asking them to pick one
// adds a step that gains nothing. The server is all-or-nothing on failure, so a
// bad entry means nothing lands and the message names it; because a re-paste of
// an already-configured server is a no-op, fixing the block and pasting again
// re-lands the entries that were fine at no cost.

function initRawPanel(_existing: Server | null): void {
  // Editing never reaches this panel — openEditModal routes stdio to the npm
  // form and remote to the remote form — so there is no edit shape to build and
  // no PUT path here. It is an add-only surface.
  const textarea = byId<HTMLTextAreaElement>("mcp-raw-input");
  const err = byId<HTMLParagraphElement>("mcp-raw-error");
  err.classList.add("hidden");
  err.textContent = "";
  textarea.value = RAW_TEMPLATE;

  const saveBtn = byId<HTMLButtonElement>("mcp-raw-save");
  saveBtn.onclick = (): void => {
    err.classList.add("hidden");
    err.textContent = "";
    let parsed: unknown;
    try {
      parsed = JSON.parse(textarea.value);
    } catch (e: unknown) {
      showPasteError(err, "Invalid JSON: " + (e instanceof Error ? e.message : String(e)));
      return;
    }
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      showPasteError(err, "Paste a JSON object: either an mcpServers block or one server.");
      return;
    }
    void submitPaste(parsed as Record<string, unknown>, err, saveBtn);
  };
}

function showPasteError(err: HTMLParagraphElement, msg: string): void {
  err.replaceChildren();
  err.textContent = msg;
  err.classList.remove("hidden");
}

async function submitPaste(
  block: Record<string, unknown>,
  err: HTMLParagraphElement,
  saveBtn: HTMLButtonElement,
): Promise<void> {
  clearFieldMarks();
  const unbind = bindLoadingState("mcp.import_servers", saveBtn, { pendingClass: "btn-loading" });
  const o = await importServers.dispatch(block, {
    onSettled: () => {
      unbind();
    },
  }).outcome;
  if (o.status !== "success") {
    // A pasted block is the case D80 exists for: several fields wrong at once,
    // none of them typed here. The textarea holds every one of them, so there is
    // no input to mark — the list under the box IS the answer.
    showSubmitError(err, o.status === "error" ? o.error : undefined, "Connect failed.");
    return;
  }
  closeModal($.mcpModal);
  mcpState.refetchServers();
}

const RAW_TEMPLATE = `{
  "mcpServers": {
    "my-server": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem"],
      "env": {
        "MY_TOKEN": "..."
      }
    }
  }
}
`;

// --- Disabled tools chip list ---

function initDisabledToolsSection(server: Server | null): void {
  const section = byId<HTMLDivElement>("mcp-disabled-tools");
  const chips = byId<HTMLDivElement>("mcp-disabled-chips");
  const input = byId<HTMLInputElement>("mcp-disabled-input");
  const addBtn = byId<HTMLButtonElement>("mcp-disabled-add");

  if (server === null) {
    section.classList.add("hidden");
    session.disabledToolsList = [];
    return;
  }

  section.classList.remove("hidden");
  session.disabledToolsList = [...(server.disabled_tools ?? [])];
  // The tool names come from the RUNTIME status (what the connected server
  // advertises), not from the config record — they are a discovery result, and
  // the config file is KAS's now. Empty until a chat has connected the server,
  // which is the honest state: nothing has told us its tools yet.
  const knownTools = discoverySignalFor(server.name).peek().tools;
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
  renderToolSuggestions(section, knownTools, chips);
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

  const label = el("span", { className: "mcp-tool-suggestions-label" }, "Available:");
  suggestionsEl = el("div", { className: "mcp-tool-suggestions" }, label);

  for (const name of available) {
    const pill = el("button", { type: "button", className: "action-pill mono" }, name);
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
