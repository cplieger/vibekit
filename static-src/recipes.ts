// ---------------------------------------------------------------------------
// The Workflows sub-tab of the configuration browser (/docs/workflows): the
// launchable recipes, each with the app's ONE run affordance — Run ⇄ Cancel.
//
// RPC-sourced, unlike the five document tabs. A workflow definition is not a
// `.kiro` file: the bundled recipes are compiled INTO the KAS bundle, and
// agent-authored ones land under KAS's own sessions tree — a file scan here
// would show zero workflows forever. `GET /api/recipes` fronts
// `_kiro/workflow/listRecipes`, which returns both populations together.
//
// The button is Run ⇄ Cancel because a recipe runs ONCE at a time (user
// decision) — one live run per definition, globally, whoever launched it — so
// a row maps to at most one run and can represent that run's state honestly.
// Cancel here is a STOP, the user's gesture throughout the app; there is no
// Retry, Continue, Pause or Resume anywhere (a terminal run's row returns to
// Run, which starts a FRESH run).
//
// Launching is PARENTLESS and opens a run tab: it touches no chat, appends to
// no transcript, and wakes nothing on completion. The agent's own launches
// (run_workflow mid-turn) never come through here and keep their chat.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { onBus, BUS_RUNS_CHANGED } from "./bus.js";
import { reconcile } from "./reconcile.js";
import { loadRecipes, loadRuns, launchRun, cancelRun } from "./actions/runs.js";
import { openLiveRunView } from "./run-view.js";
import type { Recipe, WorkflowRunRow } from "./types.js";

/** Last fetched recipe list, kept so a repaint needs no refetch. */
let recipes: Recipe[] = [];

/** Live (non-terminal) run per recipe NAME. The single-run rule makes the name
 *  a sufficient key: at most one live run per definition exists. */
let liveRuns = new Map<string, WorkflowRunRow>();

let container: HTMLElement | null = null;
let wired = false;

/** Render the Workflows tab into its panel. Called by docs.ts on tab switch
 *  and on its inventory refetches; fetches recipes + runs and repaints. */
export function renderRecipesPanel(panel: HTMLElement): void {
  container = panel;
  if (!wired) {
    wired = true;
    // A run starting or finishing flips a row's button. The bus event fires on
    // the run SSE events, so the tab tracks launches made anywhere — another
    // device, the agent, the TUI via a later list refresh.
    onBus(BUS_RUNS_CHANGED, () => {
      if (isShowing()) {
        void refreshRuns();
      }
    });
  }
  if (recipes.length === 0) {
    panel.replaceChildren(el("div", { className: "list-empty" }, "Loading workflows…"));
  }
  void (async () => {
    const [r] = await Promise.all([loadRecipes.dispatch(undefined), refreshRuns()]);
    if (r !== null) {
      recipes = r.recipes;
    }
    paint();
  })();
}

function isShowing(): boolean {
  return (
    container !== null && container.childElementCount > 0 && !container.classList.contains("hidden")
  );
}

async function refreshRuns(): Promise<void> {
  const d = await loadRuns.dispatch(undefined);
  if (d === null) {
    return;
  }
  const next = new Map<string, WorkflowRunRow>();
  for (const run of d.runs) {
    if (!isTerminal(run.status ?? "")) {
      next.set(run.name, run);
    }
  }
  liveRuns = next;
  paint();
}

/** Mirrors the server's terminalRunStatus: paused is NOT terminal — a paused
 *  run still blocks a relaunch and its row must offer Cancel, or the single-run
 *  rule would wedge the recipe with no way out. */
function isTerminal(status: string): boolean {
  return (
    status === "completed" || status === "failed" || status === "aborted" || status === "cancelled"
  );
}

function paint(): void {
  if (container === null) {
    return;
  }
  if (recipes.length === 0) {
    container.replaceChildren(el("div", { className: "list-empty" }, "No workflows available."));
    return;
  }
  for (const child of [...container.children]) {
    if (child.getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  reconcile(container, recipes, {
    key: (r: Recipe) => r.source,
    mount: (r: Recipe) => recipeRow(r),
    update: (row: HTMLElement, r: Recipe) => {
      syncButton(row, r);
    },
  });
}

function recipeRow(r: Recipe): HTMLElement {
  const name = el("span", { className: "list-row-name" }, r.name);
  const meta = el(
    "span",
    { className: "list-row-meta docs-row-meta" },
    ...(r.built_in === true ? [el("span", { className: "docs-badge" }, "bundled")] : []),
  );
  const btn = el("button", {
    type: "button",
    className: "btn-small recipe-run-btn",
  }) as HTMLButtonElement;
  // The recipe is resolved at CLICK time, not captured at mount: reconcile
  // keeps a row whose key matches and only runs update(), so a mount-time
  // closure would keep serving the recipe as it looked on FIRST paint — a
  // refetched input set or description would never reach the click.
  btn.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    const current = recipes.find((x) => x.source === r.source);
    if (current !== undefined) {
      onRunButton(current);
    }
  });

  const row = el(
    "div",
    { className: "list-row docs-row recipe-row", "data-recipe": r.source },
    el("div", { className: "docs-row-top" }, name, meta, btn),
  );
  const desc = r.description ?? "";
  if (desc !== "") {
    row.appendChild(el("div", { className: "docs-row-sub" }, desc));
  }
  const inputs = Object.keys(r.inputs ?? {});
  if (inputs.length > 0) {
    row.appendChild(
      el("div", { className: "recipe-inputs-note text-muted" }, `Inputs: ${inputs.join(", ")}`),
    );
  }
  syncButton(row, r);
  return row;
}

/** Reflect the recipe's live-run state onto its button. */
function syncButton(row: HTMLElement, r: Recipe): void {
  const btn = row.querySelector<HTMLButtonElement>(".recipe-run-btn");
  if (btn === null) {
    return;
  }
  const live = liveRuns.get(r.name);
  btn.textContent = live === undefined ? "Run" : "Cancel";
  btn.classList.toggle("danger", live !== undefined);
  btn.setAttribute(
    "aria-label",
    live === undefined ? `Run ${r.name}` : `Cancel the running ${r.name}`,
  );
}

function onRunButton(r: Recipe): void {
  const live = liveRuns.get(r.name);
  if (live !== undefined) {
    void cancelRun.dispatch(live.workflow_id);
    return;
  }
  const declared = Object.keys(r.inputs ?? {});
  if (declared.length === 0) {
    launch(r, {});
    return;
  }
  toggleInputForm(r, declared);
}

/** Inline input collection — an expanding row section, deliberately not a
 *  modal. Empty values are allowed (KAS accepts them; templates resolve
 *  empty), so this collects rather than validates. */
function toggleInputForm(r: Recipe, declared: string[]): void {
  const row = container?.querySelector<HTMLElement>(`[data-recipe="${CSS.escape(r.source)}"]`);
  if (row === null || row === undefined) {
    return;
  }
  const existing = row.querySelector(".recipe-input-form");
  if (existing !== null) {
    existing.remove();
    return;
  }
  const fields = new Map<string, HTMLInputElement>();
  const form = el("form", { className: "recipe-input-form" });
  for (const key of declared) {
    const input = el("input", {
      type: "text",
      className: "recipe-input",
      placeholder: r.inputs?.[key] ?? "string",
      "aria-label": `${r.name} input ${key}`,
    }) as HTMLInputElement;
    fields.set(key, input);
    form.appendChild(el("label", { className: "recipe-input-label" }, `${key}: `, input));
  }
  const go = el(
    "button",
    { type: "submit", className: "btn-small" },
    "Launch",
  ) as HTMLButtonElement;
  form.appendChild(go);
  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    const inputs: Record<string, string> = {};
    for (const [key, field] of fields) {
      if (field.value !== "") {
        inputs[key] = field.value;
      }
    }
    form.remove();
    launch(r, inputs);
  });
  row.appendChild(form);
  fields.values().next().value?.focus();
}

function launch(r: Recipe, inputs: Record<string, string>): void {
  void launchRun.dispatch(
    { source: r.source, inputs },
    {
      onSuccess: (d) => {
        // Optimistic flip: the run_started event confirms it, and the refetch
        // corrects a failed launch the server accepted but KAS then refused.
        liveRuns.set(r.name, { workflow_id: d.workflow_id, name: d.name, updated_at: Date.now() });
        paint();
        openLiveRunView(d.workflow_id, d.name);
      },
    },
  );
}
