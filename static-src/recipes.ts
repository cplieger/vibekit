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
import { onBus, onSSE, BUS_RUNS_CHANGED } from "./bus.js";
import { reconcile } from "./reconcile.js";
import { loadRecipes, loadRuns, launchRun, cancelRun } from "./actions/runs.js";
import { loadSchedules, saveSchedule, deleteSchedule } from "./actions/schedules.js";
import { buildSchedulePicker, defaultSpec, summaryLine } from "./schedule-picker.js";
import type { ScheduleSpec, ScheduleView } from "./schedule-types.js";
import { createPopup } from "@cplieger/ui-primitives/popup";
import { openRunView } from "./run-view.js";
import { loadSettings } from "./persist.js";
import { toggleSettingsView } from "./tabs.js";
import type { Recipe, WorkflowRunRow } from "./types.js";

/** Last fetched recipe list, kept so a repaint needs no refetch. */
let recipes: Recipe[] = [];

/** The folded filter query the configuration browser's box is applying.
 *
 *  MODULE state, not a render parameter, and that distinction is load-bearing:
 *  this panel repaints itself on its own schedule (the run poll, the schedules
 *  fetch, the settings SSE), and a parameter threaded only through the render
 *  call would be dropped by the next one — so the filter would silently lift
 *  seconds after it was typed. */
let filterText = "";

/** Live (non-terminal) run per recipe NAME. The single-run rule makes the name
 *  a sufficient key: at most one live run per definition exists. */
let liveRuns = new Map<string, WorkflowRunRow>();

let container: HTMLElement | null = null;
let wired = false;

/** How many recipes exist and how many the filter is showing, so the page's one
 *  note reads the same on this tab as on the five document tabs. */
export interface RecipeCounts {
  total: number;
  shown: number;
}

/** Every field of a recipe row a reader could plausibly type at, folded once.
 *
 *  `plan` is deliberately absent. It is KAS's node tree as raw JSON, so folding
 *  it in would match on punctuation and internal key names (`nodeId`, `type`)
 *  that nobody is typing at, and a filter that matched invisible text would be
 *  answering a different question than the box appears to ask. `built_in`
 *  contributes the literal the row DISPLAYS as its badge, for the same reason
 *  docs.ts folds the badges it renders. */
function filterHaystack(r: Recipe): string {
  return [
    r.name,
    r.description ?? "",
    r.source,
    ...Object.keys(r.inputs ?? {}),
    r.built_in === true ? "bundled" : "",
  ]
    .join("\n")
    .toLowerCase();
}

function visibleRecipes(): Recipe[] {
  if (filterText === "") {
    return recipes;
  }
  return recipes.filter((r) => filterHaystack(r).includes(filterText));
}

/** Render the Workflows tab into its panel. Called by docs.ts on tab switch
 *  and on its inventory refetches; fetches recipes + runs and repaints.
 *
 *  Takes the page's filter, because the box lives on docs.ts and the rows live
 *  here. What it is SHOWING travels back the other way, through the counts
 *  listener below — one writer, on every repaint path, rather than a return value
 *  the async refetch would immediately contradict. */
export function renderRecipesPanel(panel: HTMLElement, filter = ""): void {
  container = panel;
  // Folded HERE, so no caller has to know the convention. docs.ts already hands
  // over a folded query; taking it on trust would make this function's answer
  // depend on a rule stated in another module.
  filterText = filter.trim().toLowerCase();
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
    // The schedule form's unattended note reads the auto-approve setting, and
    // that setting is changed on another page. Without this the note would be
    // correct as of whenever this tab last rendered, which is the boilerplate a
    // live read-out exists to avoid. Same hook docs.ts uses for its inventory.
    onSSE("settings_updated", () => {
      void refreshAutoApprove();
    });
  }
  if (recipes.length === 0) {
    panel.replaceChildren(el("div", { className: "list-empty" }, "Loading workflows…"));
    onCounts?.(counts());
  } else {
    // Already fetched: paint (and report) in this tick, so a tab switch does not
    // leave the previous tab's count sitting under the box until the refetch
    // lands.
    paint();
  }
  void (async () => {
    const [r] = await Promise.all([loadRecipes.dispatch(undefined), refreshRuns()]);
    if (r !== null) {
      recipes = r.recipes;
    }
    paint();
    // Decoration, deliberately off the critical path: the workflow list must
    // still render when the schedule endpoint is unavailable.
    void refreshSchedules();
    void refreshAutoApprove();
  })();
}

/** What the current filter is showing, for the page's note. */
function counts(): RecipeCounts {
  return { total: recipes.length, shown: visibleRecipes().length };
}

/** Where a repaint's counts go. Set once by docs.ts, because the recipe fetch
 *  lands long after `renderRecipesPanel` returned its first answer — without
 *  this, the tab's note would read "0 of 0 shown." until the next keystroke. */
let onCounts: ((c: RecipeCounts) => void) | null = null;

export function setRecipeCountsListener(fn: (c: RecipeCounts) => void): void {
  onCounts = fn;
}

/** The auto-approve setting's current value, for the schedule form's unattended
 *  note. Off is the safe default and the server's own: absent or unreadable
 *  settings mean off there too, so a failed read cannot make the note claim more
 *  permission than the run will get. */
let autoApprove = false;

async function refreshAutoApprove(): Promise<void> {
  const s = await loadSettings();
  autoApprove = s.scheduled_auto_approve === true;
}

/** Schedules by recipe source. One per recipe, matching the single-run rule. */
let schedules = new Map<string, ScheduleView>();

/** Refetch the schedule set and repaint the summary lines. */
async function refreshSchedules(): Promise<void> {
  const d = await loadSchedules.dispatch(undefined);
  if (d === null) {
    return;
  }
  const next = new Map<string, ScheduleView>();
  for (const v of d.schedules) {
    next.set(v.source, v);
  }
  schedules = next;
  paint();
}

/** Reflect a recipe's schedule onto its row's summary line. */
function syncSchedule(row: HTMLElement, source: string): void {
  const line = row.querySelector<HTMLElement>(".recipe-sched-summary");
  if (line === null) {
    return;
  }
  const view = schedules.get(source);
  line.textContent = summaryLine(view);
  line.classList.toggle("is-scheduled", view?.enabled === true);
  const btn = row.querySelector<HTMLButtonElement>(".recipe-sched-btn");
  if (btn !== null) {
    btn.classList.toggle("on", view?.enabled === true);
  }
}

/** Open the recurrence picker anchored to a row's Schedule button.
 *
 *  ONE popup per button, created on first use and toggled after that. It used to
 *  construct a fresh `createPopup` on every click, so pressing Schedule three
 *  times built three pickers stacked on the same anchor — each with its own Save
 *  and Remove wired to the same recipe. The group only closes OTHER groups'
 *  popovers; it cannot dedupe instances the caller keeps making. */
function wireSchedulePopup(btn: HTMLButtonElement, source: string): void {
  let popup: ReturnType<typeof createPopup> | null = null;
  let open = false;

  btn.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    if (open && popup !== null) {
      popup.hide();
      open = false;
      return;
    }
    const view = schedules.get(source);
    const body = el("div", { className: "sched-popup" });
    // Rebuilt per open rather than cached: the picker renders the CURRENT
    // schedule, and a cached body would show whatever the set looked like the
    // first time the button was pressed.
    popup?.hide();
    popup = createPopup(body, { trigger: btn, group: "recipe-schedule" });
    const close = (): void => {
      popup?.hide();
      open = false;
    };
    body.appendChild(
      buildSchedulePicker({
        spec: view?.spec ?? defaultSpec(),
        enabled: view?.enabled ?? false,
        exists: view !== undefined,
        autoApprove,
        onOpenPermissions: () => {
          close();
          void toggleSettingsView("permissions");
        },
        // The picker's flag is carried through rather than re-decided here: it
        // was hardcoded true at both ends, so a paused schedule was unreachable
        // even once the form could express it.
        onSave: (spec: ScheduleSpec, enabled: boolean) => {
          close();
          void (async () => {
            await saveSchedule.dispatch({ source, spec, enabled });
            await refreshSchedules();
          })();
        },
        onRemove: () => {
          close();
          void (async () => {
            if (view !== undefined) {
              await deleteSchedule.dispatch(view.id);
            }
            await refreshSchedules();
          })();
        },
        onClose: close,
      }),
    );
    popup.show();
    open = true;
  });
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
  const rows = visibleRecipes();
  // Every repaint path reports, not just the fetch: the note describes what is on
  // screen, so whichever caller changed what is on screen owes the update.
  onCounts?.(counts());
  if (rows.length === 0) {
    // Two different sentences, because they answer different questions. "No
    // workflows available." under an active filter is the same lie docs.ts
    // records for its category text: the workflows exist, they are one keystroke
    // away.
    container.replaceChildren(
      el(
        "div",
        { className: "list-empty" },
        recipes.length === 0 ? "No workflows available." : "No workflows match the filter.",
      ),
    );
    return;
  }
  for (const child of [...container.children]) {
    if (child.getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  // Keyed on `source`, which is stable across a filter change — so a keystroke
  // removes and re-adds the rows that left and arrived rather than rebuilding the
  // list. `recipeRow`'s click-time lookup deliberately stays against the
  // UNFILTERED `recipes`: a row is clicked for the recipe it names, whatever the
  // box says.
  reconcile(container, rows, {
    key: (r: Recipe) => r.source,
    mount: (r: Recipe) => recipeRow(r),
    update: (row: HTMLElement, r: Recipe) => {
      syncButton(row, r);
      syncSchedule(row, r.source);
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

  const schedBtn = el("button", {
    type: "button",
    className: "btn-small recipe-sched-btn",
    "aria-label": `Schedule ${r.name}`,
  }) as HTMLButtonElement;
  schedBtn.textContent = "Schedule";
  wireSchedulePopup(schedBtn, r.source);

  // Every block goes on the SURFACE, never on the row. `.docs-row` is a
  // horizontal flex container (it holds an activation surface beside a control
  // slot), so a block appended to the row is laid out BESIDE its siblings rather
  // than under them — which is what this function used to do, for four blocks,
  // giving each one a left edge equal to the running sum of the text widths
  // before it. The column lives on `.docs-row-surface`; the five document tabs
  // moved onto it and this one was not migrated with them.
  const surface = el(
    "div",
    { className: "docs-row-surface" },
    el("div", { className: "docs-row-top" }, name, meta, schedBtn, btn),
  );
  surface.appendChild(el("div", { className: "recipe-sched-summary" }));
  const desc = r.description ?? "";
  if (desc !== "") {
    surface.appendChild(el("div", { className: "docs-row-sub" }, desc));
  }
  const inputs = Object.keys(r.inputs ?? {});
  if (inputs.length > 0) {
    surface.appendChild(
      el("div", { className: "recipe-inputs-note" }, `Inputs: ${inputs.join(", ")}`),
    );
  }
  const row = el(
    "div",
    { className: "list-row docs-row recipe-row", "data-recipe": r.source },
    surface,
  );
  syncButton(row, r);
  syncSchedule(row, r.source);
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
  // The form is a block of the row's column, so it hosts on the surface like
  // every other block. On the row it became a flex item on the main line, where
  // two declared inputs at min-width 14rem cannot fit and pushed the panel into
  // a horizontal scrollbar (its overflow-y: auto computes overflow-x: auto too).
  const host = row.querySelector<HTMLElement>(".docs-row-surface");
  if (host === null) {
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
  host.appendChild(form);
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
        openRunView(d.workflow_id, d.name);
      },
    },
  );
}
