// ---------------------------------------------------------------------------
// Knowledge bases: manage the workspace semantic-search index (kiro-cli's
// _kiro/knowledge store) from Settings → Instructions, alongside the steering
// docs / skills / agents list — knowledge bases are the same "workspace
// context" family.
//
// The list is server-canonical (it lives in kiro-cli's global disk store, not
// vibekit's chat store), so this module fetches GET /api/knowledge and renders
// it; mutations go through the knowledge.add / knowledge.remove actions and
// refetch. Indexing runs in the BACKGROUND: `add` returns immediately and the
// new base shows up as an "indexing" row with a live progress bar. A
// user-initiated add does NOT push the knowledge_indexing SSE (verified live —
// only agent-declared knowledge_bases sync does), so progress is driven by
// POLLING GET /api/knowledge while any entry is still indexing; the SSE just
// triggers an extra refetch for the agent-sync case.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { onSSE } from "./bus.js";
import { byId } from "./dom.js";
import { reconcile } from "./reconcile.js";
import { showToast } from "./toast.js";
import { confirm as confirmDialog } from "./confirm.js";
import { apiGetTyped, CancellableSlot, fetchKiroSetting, type Decoder } from "./api-client.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { addKnowledge, removeKnowledge } from "./actions/knowledge.js";
import { ICON_PLUS_16, ICON_TRASH_14 } from "./icons.js";
import { asObject, decodeArray, optBool, optStr, reqNum, reqStr } from "./validators.js";

// --- Wire type + decoder (matches internal/hub/knowledge.go knowledgeContext) ---

interface KnowledgeContext {
  name: string;
  id: string;
  description?: string;
  path?: string;
  items_display?: string;
  item_count: number;
  indexing?: boolean;
}

const P = "$.knowledge.context";

const decodeContext: Decoder<KnowledgeContext> = (v) => {
  const o = asObject(v, P);
  const out: KnowledgeContext = {
    name: reqStr(o, "name", P),
    id: reqStr(o, "id", P),
    item_count: reqNum(o, "item_count", P),
  };
  const d = optStr(o, "description", P);
  if (d !== undefined) {
    out.description = d;
  }
  const p = optStr(o, "path", P);
  if (p !== undefined) {
    out.path = p;
  }
  const disp = optStr(o, "items_display", P);
  if (disp !== undefined) {
    out.items_display = disp;
  }
  const ix = optBool(o, "indexing", P);
  if (ix !== undefined) {
    out.indexing = ix;
  }
  return out;
};

const decodeList: Decoder<{ contexts: KnowledgeContext[] }> = (v) => {
  const o = asObject(v, "$.knowledge");
  return { contexts: decodeArray(o["contexts"], decodeContext, "$.knowledge.contexts") };
};

// --- Fetch + poll state ---

const KNOWLEDGE_FLAG = "chat.enableKnowledge";
/** Poll cadence while any base is still indexing. */
const POLL_MS = 1500;
/** Safety cap on consecutive polls so a wedged index can't poll forever. */
const MAX_POLLS = 200;

const listSlot = new CancellableSlot();
let pollTimer: ReturnType<typeof setTimeout> | null = null;
let pollsLeft = 0;

registerCleanup(() => {
  listSlot.abort();
  clearPoll();
});

function clearPoll(): void {
  if (pollTimer !== null) {
    clearTimeout(pollTimer);
    pollTimer = null;
  }
}

/** Collapse the show output to one row per base. During an add, `show` returns
 *  both the placeholder context (item_count 0, "(indexing...)") AND the active
 *  operation (indexing:true, progress) under the same name — the indexing
 *  entry wins so the row shows live progress. */
function mergeByName(contexts: KnowledgeContext[]): KnowledgeContext[] {
  const byName = new Map<string, KnowledgeContext>();
  for (const c of contexts) {
    const prev = byName.get(c.name);
    if (prev === undefined || (c.indexing === true && prev.indexing !== true)) {
      byName.set(c.name, c);
    }
  }
  return [...byName.values()];
}

/** Fetch + render the knowledge list. `fromPoll` distinguishes a poll tick
 *  (keeps the budget counting down) from a user/SSE-triggered load (resets the
 *  budget). Reschedules itself while any base is still indexing. */
export function loadKnowledge(fromPoll = false): void {
  if (!fromPoll) {
    pollsLeft = MAX_POLLS;
  }
  clearPoll();
  const signal = listSlot.start();
  void apiGetTyped("/api/knowledge", decodeList, signal).then((d) => {
    if (signal.aborted) {
      return;
    }
    if (d === null) {
      renderError();
      return;
    }
    const merged = mergeByName(d.contexts);
    renderList(merged);
    if (merged.some((c) => c.indexing === true) && pollsLeft > 0) {
      pollsLeft--;
      pollTimer = setTimeout(() => {
        loadKnowledge(true);
      }, POLL_MS);
    }
  });
  // The enable-hint reads a kiro-cli setting (a subprocess shell-out), so only
  // refresh it on a user/SSE-triggered load — not on every poll tick.
  if (!fromPoll) {
    void refreshHint();
  }
}

/** Show/hide the "knowledge is off" hint based on the chat.enableKnowledge
 *  flag. Management works regardless of the flag; the hint just explains that
 *  the agent won't consult these bases during chats while it's off. */
async function refreshHint(): Promise<void> {
  const enabled = await fetchKiroSetting(KNOWLEDGE_FLAG, (raw) => raw === "true", true);
  byId<HTMLParagraphElement>("knowledge-hint").hidden = enabled;
}

// --- Rendering ---

function renderError(): void {
  const container = byId<HTMLDivElement>("knowledge-list");
  container.replaceChildren(
    el("div", { className: "list-empty" }, "Couldn't load knowledge bases."),
  );
}

function renderList(items: KnowledgeContext[]): void {
  const container = byId<HTMLDivElement>("knowledge-list");
  // Drop any prior non-keyed placeholder (empty / error) before reconcile.
  for (const child of [...container.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  if (items.length === 0) {
    container.replaceChildren();
    container.appendChild(el("div", { className: "list-empty" }, "No knowledge bases yet."));
    return;
  }
  reconcile(container, items, {
    key: (c: KnowledgeContext) => `kb:${c.name}`,
    mount: (c: KnowledgeContext) => mountRow(c),
    update: (row: HTMLElement, c: KnowledgeContext) => {
      fillRow(row, c);
    },
  });
}

function mountRow(c: KnowledgeContext): HTMLElement {
  const row = el("div", { className: "list-row knowledge-row" });
  fillRow(row, c);
  return row;
}

/** Rebuild a row's children only when its rendered state changed, so a stable
 *  row keeps its DOM identity (and any focus) across polls; only the indexing
 *  row — whose items_display advances — actually re-renders each tick. */
function fillRow(row: HTMLElement, c: KnowledgeContext): void {
  const sig = `${c.indexing === true ? "1" : "0"}|${String(c.item_count)}|${c.items_display ?? ""}|${c.path ?? ""}`;
  if (row.getAttribute("data-sig") === sig) {
    return;
  }
  row.setAttribute("data-sig", sig);
  row.classList.toggle("knowledge-indexing", c.indexing === true);
  row.replaceChildren(...rowChildren(c));
}

function rowChildren(c: KnowledgeContext): HTMLElement[] {
  const name = el("span", { className: "list-row-name" }, c.name);
  if (c.indexing === true) {
    return [name, progressEl(c.items_display)];
  }
  const count = `${String(c.item_count)} item${c.item_count === 1 ? "" : "s"}`;
  const metaText = c.path !== undefined && c.path !== "" ? `${count} · ${c.path}` : count;
  const meta = el("span", { className: "list-row-meta knowledge-meta" }, metaText);
  return [name, meta, removeBtn(c.name)];
}

/** Parse the leading integer percentage from an items_display string
 *  ("42%", "42% · ETA 3s", "0%"); null when there's no percentage
 *  ("Cancelled", "Failed"). */
function parsePct(display: string | undefined): number | null {
  const m = /^(\d+)%/.exec(display ?? "");
  return m ? Math.min(100, Number(m[1])) : null;
}

function progressEl(display: string | undefined): HTMLElement {
  const wrap = el("span", { className: "knowledge-progress" });
  const pct = parsePct(display);
  if (pct !== null) {
    const fill = el("span", { className: "knowledge-bar-fill" });
    fill.style.inlineSize = `${String(pct)}%`;
    wrap.appendChild(el("span", { className: "knowledge-bar", role: "progressbar" }, fill));
  }
  const text = display !== undefined && display !== "" ? `Indexing… ${display}` : "Indexing…";
  wrap.appendChild(el("span", { className: "knowledge-progress-text" }, text));
  return wrap;
}

function removeBtn(name: string): HTMLElement {
  const btn = el("button", {
    type: "button",
    className: "list-row-btn knowledge-remove",
    "data-tooltip": "Remove",
    "aria-label": `Remove knowledge base ${name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_TRASH_14;
  btn.addEventListener("click", (e) => {
    e.stopPropagation();
    void onRemove(name);
  });
  return btn;
}

async function onRemove(name: string): Promise<void> {
  const ok = await confirmDialog(
    `Remove knowledge base "${name}"? The indexed data is deleted and the agent loses access to it.`,
    "Remove",
    "destructive",
  );
  if (!ok) {
    return;
  }
  void removeKnowledge.dispatch(
    { name },
    {
      onSuccess: () => {
        loadKnowledge();
      },
    },
  );
}

// --- Add form (inline, toggled by the + button) ---

function buildAddForm(): HTMLFormElement {
  const pathInput = el("input", {
    type: "text",
    id: "knowledge-add-path",
    className: "knowledge-add-input",
    placeholder: "Directory path (e.g. docs or /abs/path)…",
    "aria-label": "Knowledge base directory path",
  }) as HTMLInputElement;
  const nameInput = el("input", {
    type: "text",
    id: "knowledge-add-name",
    className: "knowledge-add-input",
    placeholder: "Name (optional)…",
    "aria-label": "Knowledge base name",
  }) as HTMLInputElement;
  const submit = el(
    "button",
    { type: "submit", className: "btn-small" },
    "Add",
  ) as HTMLButtonElement;
  const cancel = el("button", { type: "button", className: "btn-small" }, "Cancel");
  cancel.addEventListener("click", () => {
    hideAddForm();
  });
  const form = el(
    "form",
    { className: "knowledge-add-form", id: "knowledge-add-form" },
    pathInput,
    nameInput,
    submit,
    cancel,
  ) as HTMLFormElement;
  form.hidden = true;
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    void onAdd(pathInput, nameInput);
  });
  registerCleanup(bindLoadingState("knowledge.add", submit, { preserveDisabled: true }));
  return form;
}

async function onAdd(pathInput: HTMLInputElement, nameInput: HTMLInputElement): Promise<void> {
  const path = pathInput.value.trim();
  if (path === "") {
    pathInput.focus();
    return;
  }
  const name = nameInput.value.trim();
  const res = await addKnowledge.dispatch({ path, name });
  if (res === null) {
    // The action's default error toast already surfaced the server message
    // (bad path / usage error); keep the form open so the user can fix it.
    return;
  }
  showToast(`Indexing "${name !== "" ? name : path}" in the background…`, "success");
  pathInput.value = "";
  nameInput.value = "";
  hideAddForm();
  loadKnowledge();
}

function showAddForm(): void {
  byId<HTMLFormElement>("knowledge-add-form").hidden = false;
  byId<HTMLInputElement>("knowledge-add-path").focus();
}

function hideAddForm(): void {
  byId<HTMLFormElement>("knowledge-add-form").hidden = true;
}

// --- Init (once, at settings init) ---

export function initKnowledge(): void {
  const addBtn = byId<HTMLButtonElement>("knowledge-add-btn");
  addBtn.innerHTML = ICON_PLUS_16;

  const form = buildAddForm();
  byId<HTMLDivElement>("knowledge-list").before(form);

  addBtn.addEventListener("click", () => {
    if (byId<HTMLFormElement>("knowledge-add-form").hidden) {
      showAddForm();
    } else {
      hideAddForm();
    }
  });

  // Agent-declared knowledge_bases sync pushes this; refetch so the list +
  // any progress reflect it. (User adds are covered by the poll loop.)
  onSSE("knowledge_indexing", () => {
    loadKnowledge();
  });
}
