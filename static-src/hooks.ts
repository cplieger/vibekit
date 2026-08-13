// ---------------------------------------------------------------------------
// Hooks: manage the workspace's .kiro/hooks/*.json hooks from Settings →
// Instructions, alongside steering docs / skills / agents / knowledge bases —
// hooks are the same ".kiro/* workspace config" family.
//
// The list is server-canonical (KAS reads the hook files on the utility bridge;
// see internal/hub/hooks.go), so this module fetches GET /api/hooks and renders
// it; mutations go through the hooks.set_enabled / hooks.run actions and
// refetch. The server broadcasts an hooks_changed SSE after a toggle / run /
// external file change, which triggers a (debounced) refetch — so a toggle on
// one device reflects on every device.
//
// "Run now" is offered for runCommand hooks only: POST /api/hooks/{id}/trigger
// runs the command (via KAS's triggerHook → executeHook callback, server-side)
// and returns its output, shown inline. askAgent hooks have no "Run now" here —
// running one means asking the agent its prompt, which is the normal chat flow;
// creating hooks likewise stays with vibekit's create_hook command. This
// surface manages (list / toggle / run / status) existing hooks.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { onSSE } from "./bus.js";
import { byId } from "./dom.js";
import { reconcile } from "./reconcile.js";
import { showToast } from "./toast.js";
import { apiGetTyped, CancellableSlot, type Decoder } from "./api-client.js";
import { registerCleanup } from "./actions/index.js";
import { runHook, setHookEnabled } from "./actions/hooks.js";
import type { HookRunResult } from "./actions/hooks.js";
import { openFile } from "./editor-openers.js";
import { ICON_EDIT_14, ICON_PLAY } from "./icons.js";
import { asObject, decodeArray, optStr, reqBool, reqStr } from "./validators.js";

// --- Wire type + decoder (matches internal/hub/hooks.go hookInfo) ---

interface Hook {
  id: string;
  name: string;
  trigger: string;
  action_type: string; // "runCommand" | "askAgent"
  /** "workspace" (.kiro/hooks in the workspace) or "global" (~/.kiro/hooks,
   *  kiro-cli 2.13+ — applies in every workspace). Global rows get a scope
   *  badge and no editor link (their file lives in the blocked HOME tree);
   *  tolerated absent (older server) → treated as workspace. */
  scope?: string;
  command?: string;
  prompt?: string;
  matcher?: string;
  file_path?: string;
  disabled_reason?: string;
  enabled: boolean;
}

const P = "$.hook";

const decodeHook: Decoder<Hook> = (v) => {
  const o = asObject(v, P);
  const out: Hook = {
    id: reqStr(o, "id", P),
    name: reqStr(o, "name", P),
    trigger: reqStr(o, "trigger", P),
    action_type: reqStr(o, "action_type", P),
    enabled: reqBool(o, "enabled", P),
  };
  const scope = optStr(o, "scope", P);
  if (scope !== undefined) {
    out.scope = scope;
  }
  const command = optStr(o, "command", P);
  if (command !== undefined) {
    out.command = command;
  }
  const prompt = optStr(o, "prompt", P);
  if (prompt !== undefined) {
    out.prompt = prompt;
  }
  const matcher = optStr(o, "matcher", P);
  if (matcher !== undefined) {
    out.matcher = matcher;
  }
  const filePath = optStr(o, "file_path", P);
  if (filePath !== undefined) {
    out.file_path = filePath;
  }
  const reason = optStr(o, "disabled_reason", P);
  if (reason !== undefined) {
    out.disabled_reason = reason;
  }
  return out;
};

const decodeList: Decoder<{ hooks: Hook[] }> = (v) => {
  const o = asObject(v, "$.hooks");
  return { hooks: decodeArray(o["hooks"], decodeHook, "$.hooks.hooks") };
};

// --- Fetch state ---

const listSlot = new CancellableSlot();
/** Transient per-hook "Run now" output, keyed by hook id. Not part of the
 *  server list — surfaced inline until the next run / list change. */
const runOutputs = new Map<string, HookRunResult>();
/** Last rendered list, so a run can re-render one row without a refetch. */
let lastHooks: Hook[] = [];

const SSE_REFETCH_DEBOUNCE_MS = 250;
let refetchTimer: ReturnType<typeof setTimeout> | undefined;

registerCleanup(() => {
  listSlot.abort();
  if (refetchTimer !== undefined) {
    clearTimeout(refetchTimer);
    refetchTimer = undefined;
  }
  runOutputs.clear();
  lastHooks = [];
});

/** Fetch + render the hook list. */
export function loadHooks(): void {
  const signal = listSlot.start();
  void apiGetTyped("/api/hooks", decodeList, signal).then((d) => {
    if (signal.aborted) {
      return;
    }
    if (d === null) {
      renderError();
      return;
    }
    renderList(d.hooks);
  });
}

/** Coalesce an hooks_changed SSE burst into one refetch. */
function scheduleRefetch(): void {
  if (refetchTimer !== undefined) {
    clearTimeout(refetchTimer);
  }
  refetchTimer = setTimeout(() => {
    refetchTimer = undefined;
    loadHooks();
  }, SSE_REFETCH_DEBOUNCE_MS);
}

// --- Rendering ---

function renderError(): void {
  byId<HTMLDivElement>("hooks-list").replaceChildren(
    el("div", { className: "list-empty" }, "Couldn't load hooks."),
  );
}

function renderList(items: Hook[]): void {
  lastHooks = items;
  // Drop run outputs for hooks that no longer exist.
  const ids = new Set(items.map((h) => h.id));
  for (const k of [...runOutputs.keys()]) {
    if (!ids.has(k)) {
      runOutputs.delete(k);
    }
  }
  const container = byId<HTMLDivElement>("hooks-list");
  // Drop any prior non-keyed placeholder (empty / error) before reconcile.
  for (const child of [...container.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  if (items.length === 0) {
    container.replaceChildren(el("div", { className: "list-empty" }, "No hooks yet."));
    return;
  }
  reconcile(container, items, {
    key: (h: Hook) => `hook:${h.id}`,
    mount: (h: Hook) => mountRow(h),
    update: (row: HTMLElement, h: Hook) => {
      fillRow(row, h);
    },
  });
}

function mountRow(h: Hook): HTMLElement {
  const row = el("div", { className: "list-row hook-row", "data-hook-id": h.id });
  fillRow(row, h);
  return row;
}

/** Content signature — includes the transient run output so a fresh run
 *  re-renders the row (and a stable row keeps DOM identity across refetches,
 *  preserving toggle focus).
 *
 *  Both levels use keyenc `join`. The fields are arbitrary text (`command`,
 *  `prompt`, `matcher` come from the hook file; `output` is captured process
 *  output), so the old "\u00a7"/"|" separators were only unlikely, not
 *  reserved — a hook whose command contained one could produce the same
 *  signature as a genuinely different hook state. The nested run-output
 *  signature is composed the way keyenc composes: it gets its own `join`, and
 *  that single result becomes ONE component of the outer join, so its
 *  contents cannot reach the outer field boundaries. Consequence of a
 *  collision was a STALE ROW (row identity is `hook:${id}`, see renderList),
 *  not a wrong or dropped row. */
function rowSig(h: Hook): string {
  const out = runOutputs.get(h.id);
  const outSig = out ? join(out.ran ? "1" : "0", String(out.exit_code), out.output) : "";
  return join(
    h.enabled ? "1" : "0",
    h.trigger,
    h.action_type,
    h.scope ?? "",
    h.command ?? "",
    h.prompt ?? "",
    h.matcher ?? "",
    h.disabled_reason ?? "",
    outSig,
  );
}

/** Global hooks live in ~/.kiro/hooks (kiro-cli 2.13+) and apply in every
 *  workspace. Absent scope (older server) counts as workspace. */
function isGlobalHook(h: Hook): boolean {
  return h.scope === "global";
}

function fillRow(row: HTMLElement, h: Hook): void {
  const sig = rowSig(h);
  if (row.getAttribute("data-sig") === sig) {
    return;
  }
  row.setAttribute("data-sig", sig);
  row.classList.toggle("hook-off", !h.enabled);
  row.replaceChildren(...rowChildren(h));
}

function rowChildren(h: Hook): HTMLElement[] {
  const main = el("div", { className: "hook-main" }, hookHeader(h), hookBody(h));
  const children: HTMLElement[] = [main, hookControls(h)];
  const out = runOutputs.get(h.id);
  if (out?.ran === true) {
    children.push(outputPanel(out));
  }
  return children;
}

function hookHeader(h: Hook): HTMLElement {
  const header = el(
    "div",
    { className: "hook-header" },
    el("span", { className: "hook-name" }, h.name),
    el("span", { className: "hook-badge hook-trigger" }, h.trigger),
  );
  if (h.action_type === "askAgent") {
    header.appendChild(el("span", { className: "hook-badge hook-action-agent" }, "Agent"));
  } else {
    header.appendChild(el("span", { className: "hook-badge hook-action-cmd" }, "Command"));
  }
  if (isGlobalHook(h)) {
    // Scope badge only for global rows — workspace is the default and a
    // badge on every row would be noise. The tooltip carries the file path
    // (global rows have no open-in-editor affordance; the file lives in the
    // blocked container-HOME tree).
    header.appendChild(
      el(
        "span",
        {
          className: "hook-badge hook-scope-global",
          "data-tooltip": `${h.file_path ?? "~/.kiro/hooks"} — applies in every workspace`,
        },
        "Global",
      ),
    );
  }
  if (h.matcher !== undefined && h.matcher !== "") {
    header.appendChild(
      el("span", { className: "hook-badge hook-matcher", "data-tooltip": "Matcher" }, h.matcher),
    );
  }
  return header;
}

function hookBody(h: Hook): HTMLElement {
  if (h.action_type === "askAgent") {
    return el("div", { className: "hook-summary hook-prompt" }, h.prompt ?? "");
  }
  return el("code", { className: "hook-summary hook-cmd" }, h.command ?? "");
}

function hookControls(h: Hook): HTMLElement {
  const controls = el("div", { className: "hook-controls" });
  if (h.disabled_reason !== undefined && h.disabled_reason !== "") {
    controls.appendChild(
      el("span", { className: "hook-reason", "data-tooltip": h.disabled_reason }, "disabled"),
    );
  }
  if (h.action_type === "runCommand") {
    const run = el("button", {
      type: "button",
      className: "list-row-btn hook-run",
      "data-tooltip": "Run now",
      "aria-label": `Run hook ${h.name}`,
    }) as HTMLButtonElement;
    run.innerHTML = ICON_PLAY;
    controls.appendChild(run);
  }
  // Editor link for workspace hooks only: a global hook's file path is a
  // ~-display path under the container HOME, which the file surface blocks
  // (its path rides the Global badge tooltip instead).
  if (h.file_path !== undefined && h.file_path !== "" && !isGlobalHook(h)) {
    const open = el("button", {
      type: "button",
      className: "list-row-btn hook-open",
      "data-path": h.file_path,
      "data-tooltip": "Open hook file",
      "aria-label": `Open ${h.file_path}`,
    }) as HTMLButtonElement;
    open.innerHTML = ICON_EDIT_14;
    controls.appendChild(open);
  }
  controls.appendChild(enableToggle(h));
  return controls;
}

function enableToggle(h: Hook): HTMLElement {
  const input = el("input", {
    type: "checkbox",
    className: "hook-toggle",
    "aria-label": `${h.enabled ? "Disable" : "Enable"} hook ${h.name}`,
  }) as HTMLInputElement;
  input.checked = h.enabled;
  return el(
    "label",
    { className: "toggle toggle-inline" },
    input,
    el("span", { className: "toggle-slider" }),
  );
}

function outputPanel(out: HookRunResult): HTMLElement {
  const status =
    out.exit_code === 0
      ? el("span", { className: "hook-exit hook-exit-ok" }, "exit 0")
      : el("span", { className: "hook-exit hook-exit-fail" }, `exit ${String(out.exit_code)}`);
  const body = out.output.trim() === "" ? "(no output)" : out.output;
  return el(
    "div",
    { className: "hook-output" },
    el("div", { className: "hook-output-head" }, status),
    el("pre", { className: "hook-output-body" }, body),
  );
}

// --- Handlers (delegated on the list container) ---

function onToggle(id: string, enabled: boolean): void {
  // Server-canonical: dispatch, then refetch to reconcile the toggle from
  // authoritative state (also re-syncs the checkbox if the write failed).
  void setHookEnabled.dispatch({ id, enabled }).then(() => {
    loadHooks();
  });
}

async function onRun(id: string, btn: HTMLButtonElement): Promise<void> {
  btn.disabled = true;
  const original = btn.innerHTML;
  btn.textContent = "Running…";
  const res = await runHook.dispatch({ id });
  btn.disabled = false;
  btn.innerHTML = original;
  if (res === null) {
    showToast("Hook run failed", "error");
    return;
  }
  runOutputs.set(id, res);
  rerenderRow(id);
}

/** Re-render one row from the cached list so a run's output shows without a
 *  server refetch (the hook data itself is unchanged). */
function rerenderRow(id: string): void {
  const h = lastHooks.find((x) => x.id === id);
  if (h === undefined) {
    return;
  }
  for (const row of byId<HTMLDivElement>("hooks-list").querySelectorAll<HTMLElement>(".hook-row")) {
    if (row.getAttribute("data-hook-id") === id) {
      fillRow(row, h);
      return;
    }
  }
}

function hookIDOf(target: HTMLElement): string {
  return target.closest<HTMLElement>(".hook-row")?.getAttribute("data-hook-id") ?? "";
}

// --- Init (once, at settings init) ---

export function initHooks(): void {
  const list = byId<HTMLDivElement>("hooks-list");

  list.addEventListener("click", (e) => {
    const target = e.target as HTMLElement;
    const runBtn = target.closest<HTMLButtonElement>(".hook-run");
    if (runBtn !== null) {
      const id = hookIDOf(runBtn);
      if (id !== "") {
        void onRun(id, runBtn);
      }
      return;
    }
    const openBtn = target.closest<HTMLElement>(".hook-open");
    if (openBtn !== null) {
      const path = openBtn.getAttribute("data-path");
      if (path !== null && path !== "") {
        openFile(path);
      }
    }
  });

  list.addEventListener("change", (e) => {
    const target = e.target as HTMLElement;
    if (target.classList.contains("hook-toggle")) {
      const id = hookIDOf(target);
      if (id !== "") {
        onToggle(id, (target as HTMLInputElement).checked);
      }
    }
  });

  // The server broadcasts hooks_changed after a create / toggle / external
  // .kiro/hooks edit; refetch (debounced) so every device stays in sync.
  onSSE("hooks_changed", () => {
    scheduleRefetch();
  });
}

/** Reset module state (fetch slot + run outputs + cached list) for test
 *  isolation. Production never calls this. */
export function _resetForTest(): void {
  listSlot.abort();
  if (refetchTimer !== undefined) {
    clearTimeout(refetchTimer);
    refetchTimer = undefined;
  }
  runOutputs.clear();
  lastHooks = [];
}
