// ---------------------------------------------------------------------------
// Tests for the Workflows sub-tab. Two halves.
//
// The Run ⇄ Cancel row logic. Each case pins a piece of the single-run contract:
//   - the button names the recipe's ONE possible live run (Run ⇄ Cancel flips
//     on the run list, not on who launched it)
//   - paused is NOT terminal: a paused run still blocks a relaunch, so its row
//     must offer Cancel or the recipe wedges with no way out
//   - launching with declared inputs collects them inline (no modal)
//   - a launch opens the run tab that OWNS the run
//
// The row's LAYOUT, which this suite deliberately did not cover before and
// which is what let a regression ship. `.docs-row` became a horizontal flex
// container (a delete button had to sit beside the activation surface) and the
// five document tabs moved their stack onto a `.docs-row-surface` column. This
// tab was not migrated, so its four blocks — top line, schedule summary,
// description, inputs note — were authored to stack and were laid side by side
// instead, each block's left edge being the running sum of the text widths
// before it.
//
// The layout cases are a DOM half and a SOURCE half, and they need each other.
// The test page loads no app stylesheet, so "these blocks share a left
// edge" is not observable at runtime; it is the conjunction of two facts that
// are — the blocks are siblings of one container, and that container is a
// stretch column whose members carry no inline-start offset.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { loadCSS, ruleBody, ruleContaining } from "./__test-helpers__/css-rules.js";

/** Every authored client source file plus the shipped page, inlined as text.
 *  `import.meta.glob` replaces the directory walk this used to do with `readdir`:
 *  the corpus is the same set of files, resolved at transform time instead. */
const authoredSource = import.meta.glob<string>(
  ["./*.ts", "./actions/*.ts", "./handlers/*.ts", "./fundamentals/*.ts", "../static/index.html"],
  { query: "?raw", import: "default", eager: true },
);

const dispatched: { name: string; args: unknown }[] = [];

vi.mock("./actions/runs.js", () => ({
  loadRecipes: {
    dispatch: vi.fn((args: unknown) => {
      dispatched.push({ name: "recipes", args });
      return Promise.resolve(recipesReply);
    }),
  },
  loadRuns: {
    dispatch: vi.fn((args: unknown) => {
      dispatched.push({ name: "runs", args });
      return Promise.resolve(runsReply);
    }),
  },
  launchRun: {
    dispatch: vi.fn(
      (
        args: unknown,
        opts?: { onSuccess?: (d: { workflow_id: string; name: string }) => void },
      ) => {
        dispatched.push({ name: "launch", args });
        opts?.onSuccess?.({ workflow_id: "wf_new", name: "goal" });
        return Promise.resolve({ workflow_id: "wf_new", name: "goal" });
      },
    ),
  },
  cancelRun: {
    dispatch: vi.fn((args: unknown) => {
      dispatched.push({ name: "cancel", args });
      return Promise.resolve({ ok: true });
    }),
  },
}));
vi.mock("./run-view.js", () => ({ openRunView: vi.fn() }));
// The unattended note's auto-approve read-out. Unmocked, refreshAutoApprove
// reaches /api/settings through the actions transport (which the api-client mock
// does not cover), fire-and-forget, so the request was still open when the window
// tore down and printed an unhandled AbortError.
vi.mock("./persist.js", () => ({ loadSettings: vi.fn(async () => ({})) }));

// The Schedule button's actions: unmocked they reach the network, and a row's
// summary line is decoration this suite does not assert on.
vi.mock("./actions/schedules.js", () => ({
  loadSchedules: { dispatch: vi.fn(async () => ({ schedules: [] })) },
  saveSchedule: { dispatch: vi.fn(async () => null) },
  deleteSchedule: { dispatch: vi.fn(async () => null) },
}));

import { renderRecipesPanel, setRecipeCountsListener } from "./recipes.js";
import { openRunView } from "./run-view.js";
import { launchRun, cancelRun } from "./actions/runs.js";
import type { RecipesResponse, WorkflowRun, ResumableSession } from "./types.js";

let recipesReply: RecipesResponse = { recipes: [] };
let runsReply: { sessions: ResumableSession[]; runs: WorkflowRun[] } = {
  sessions: [],
  runs: [],
};

function recipe(name: string, inputs?: Record<string, string>): RecipesResponse["recipes"][0] {
  const base = { name, source: `bundled://${name}`, description: `${name} desc` };
  return inputs === undefined ? base : { ...base, inputs };
}

function run(name: string, id: string, status: string): WorkflowRun {
  return { workflow_id: id, name, status, updated_at: 0 };
}

async function render(filter = ""): Promise<HTMLElement> {
  const panel = document.createElement("div");
  document.body.appendChild(panel);
  renderRecipesPanel(panel, filter);
  // renderRecipesPanel awaits its two fetches before painting.
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  return panel;
}

function names(panel: HTMLElement): string[] {
  return [...panel.querySelectorAll(".list-row-name")].map((e) => e.textContent ?? "");
}

function buttonFor(panel: HTMLElement, source: string): HTMLButtonElement | null {
  return panel.querySelector<HTMLButtonElement>(`[data-recipe="${source}"] .recipe-run-btn`);
}

beforeEach(() => {
  document.body.replaceChildren();
  dispatched.length = 0;
  vi.mocked(openRunView).mockClear();
  vi.mocked(launchRun.dispatch).mockClear();
  vi.mocked(cancelRun.dispatch).mockClear();
  recipesReply = { recipes: [] };
  runsReply = { sessions: [], runs: [] };
});

describe("the Run ⇄ Cancel row", () => {
  it("labels an idle recipe Run and a live one Cancel", async () => {
    recipesReply = { recipes: [recipe("goal"), recipe("investigate")] };
    runsReply.runs = [run("investigate", "wf_9", "running")];
    const panel = await render();

    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Run");
    expect(buttonFor(panel, "bundled://investigate")?.textContent).toBe("Cancel");
  });

  it("treats a PAUSED run as live — Cancel, or the recipe wedges", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_1", "paused")];
    const panel = await render();
    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Cancel");
  });

  it("returns a TERMINAL run's row to Run — a fresh run, never a retry", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_1", "failed")];
    const panel = await render();
    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Run");
  });

  it("launches an input-less recipe on click and opens its run tab", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    const panel = await render();

    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(launchRun.dispatch).mock.calls[0]?.[0]).toEqual({
      source: "bundled://goal",
      inputs: {},
    });
    expect(vi.mocked(openRunView)).toHaveBeenCalledWith("wf_new", "goal");
  });

  it("cancels the LIVE run on click, whoever launched it", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_7", "running")];
    const panel = await render();

    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(cancelRun.dispatch).mock.calls[0]?.[0]).toBe("wf_7");
    expect(vi.mocked(launchRun.dispatch)).not.toHaveBeenCalled();
  });

  it("collects declared inputs inline before launching — no modal", async () => {
    recipesReply = { recipes: [recipe("goal", { prompt: "prompt", max_iterations: "string" })] };
    const panel = await render();

    // First click expands the form instead of launching.
    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(launchRun.dispatch)).not.toHaveBeenCalled();
    const form = panel.querySelector<HTMLFormElement>(".recipe-input-form");
    expect(form).not.toBeNull();

    // Fill one field, leave the other empty (allowed), submit.
    const field = form?.querySelector<HTMLInputElement>("input");
    if (field !== null && field !== undefined) {
      field.value = "make the tests pass";
    }
    form?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    const args = vi.mocked(launchRun.dispatch).mock.calls[0]?.[0] as {
      source: string;
      inputs: Record<string, string>;
    };
    expect(args.source).toBe("bundled://goal");
    expect(args.inputs).toEqual({ prompt: "make the tests pass" });
  });
});

describe("the recipe row stacks", () => {
  const docs = loadCSS("28-docs.css");

  function rowFor(panel: HTMLElement, source: string): HTMLElement {
    const row = panel.querySelector<HTMLElement>(`[data-recipe="${source}"]`);
    if (row === null) {
      throw new Error(`no row rendered for ${source}`);
    }
    return row;
  }

  // Each case names its own recipe. The module keeps the last fetched list, and
  // `reconcile` keys rows by source and runs only `update()` on a key it already
  // has — so reusing one name across cases would let a row MOUNTED from the
  // previous case's recipe survive into this one and be asserted against the new
  // one's fields. A unique key per case is what forces a fresh mount.

  it("hosts every block on the surface and nothing beside it", async () => {
    // The DOM half of "the blocks share a left edge". A block appended to the
    // ROW is a flex item on the row's horizontal main axis; a block appended to
    // the surface is a member of its column. So the assertion is membership: the
    // row has exactly ONE child, and all four blocks are inside it.
    recipesReply = { recipes: [recipe("stack-all", { prompt: "prompt" })] };
    const panel = await render();
    const row = rowFor(panel, "bundled://stack-all");

    expect(row.children.length).toBe(1);
    const surface = row.children[0];
    expect(surface?.className).toBe("docs-row-surface");

    const blocks = [...(surface?.children ?? [])].map((c) => c.className);
    expect(blocks).toEqual([
      "docs-row-top",
      "recipe-sched-summary",
      "docs-row-sub",
      "recipe-inputs-note",
    ]);
  });

  it("declares the surface a stretch column, with no per-block inset", () => {
    // The SOURCE half. Siblings in a stretch column each span the container's
    // content box, so their left edges are equal — unless one carries its own
    // inline-start offset, which the schedule summary did: 4px of
    // padding-inline-start, the exact residual left over once the stack was
    // restored, and the only remaining reason the three lines disagreed.
    const surface = ruleBody(docs, ".docs-row-surface");
    expect(/flex-direction:\s*column/.test(surface)).toBe(true);
    expect(/align-items:\s*stretch/.test(surface)).toBe(true);

    for (const block of [".recipe-sched-summary", ".recipe-inputs-note", ".docs-row-sub"]) {
      const body = ruleBody(docs, block);
      expect(/padding-inline-start|padding-left|margin-inline-start|margin-left/.test(body)).toBe(
        false,
      );
    }
  });

  it("keeps the badge beside the title rather than at the far edge", async () => {
    // Adjacency in the DOM is necessary but not sufficient: the badge was
    // already the name's next sibling while it rendered at the row's right
    // edge, because `.list-row-name { flex: 1 }` ate the free space between
    // them. Both facts, therefore.
    recipesReply = { recipes: [{ ...recipe("badge-beside"), built_in: true }] };
    const panel = await render();
    const top = rowFor(panel, "bundled://badge-beside").querySelector(".docs-row-top");

    expect(top?.children[0]?.className).toBe("list-row-name");
    expect(top?.children[1]?.className).toContain("docs-row-meta");
    expect(top?.children[1]?.textContent).toBe("bundled");

    const name = ruleContaining(docs, ".recipe-row .list-row-name", "top");
    expect(/flex:\s*0 1 auto/.test(name.body)).toBe(true);
  });

  it("stacks the badge under the title on a phone, from THIS stylesheet", () => {
    // The cascade trap. 28-docs.css is unlayered and 50-mobile.css sits inside
    // `@layer mobile`, so an unlayered rule wins whatever the media query says
    // — a copy of this block over there would never apply, which is why
    // 50-mobile.css's own .page-content padding override is already dead.
    const mobile = ruleContaining(docs, ".recipe-row .list-row-name", "40rem");
    expect(/flex-basis:\s*100%/.test(mobile.body)).toBe(true);
    expect(
      /flex-wrap:\s*wrap/.test(ruleContaining(docs, ".recipe-row .docs-row-top", "40rem").body),
    ).toBe(true);

    expect(loadCSS("50-mobile.css")).not.toMatch(/recipe/);
  });

  it("hosts the inputs form off the row's main line", async () => {
    // Two 14rem inputs cannot fit a ~750px line, and the panel's `overflow-y:
    // auto` computes `overflow-x` to `auto` with it, so a two-input recipe could
    // put a horizontal scrollbar on the whole page.
    recipesReply = {
      recipes: [recipe("form-host", { prompt: "prompt", max_iterations: "string" })],
    };
    const panel = await render();
    const row = rowFor(panel, "bundled://form-host");
    buttonFor(panel, "bundled://form-host")?.click();

    const form = row.querySelector(".recipe-input-form");
    expect(form).not.toBeNull();
    expect(form?.parentElement?.className).toBe("docs-row-surface");
    // Nothing joined the row's own axis on the way.
    expect(row.children.length).toBe(1);
  });

  it("declares .recipe-run-btn exactly once", () => {
    // It was declared twice at equal specificity with conflicting
    // margin-inline-start, so which one won was a source-order accident.
    // ruleContaining requires exactly one match and reports the count it found.
    const rule = ruleContaining(docs, ".recipe-run-btn", "top");
    expect(/margin-inline-start:\s*0/.test(rule.body)).toBe(true);
  });

  it("gives the pointer only to a surface that actually activates", () => {
    // `.docs-row { cursor: pointer }` reached every row on all six tabs. A
    // recipe row has no click handler (this stylesheet says so itself) and an
    // inert document row — a global hook — cannot be opened either, so both
    // offered a pointer and then did nothing. docs.ts sets role=button exactly
    // when it wires the open, which makes the attribute the honest condition.
    expect(/cursor/.test(ruleBody(docs, ".docs-row"))).toBe(false);
    const surface = ruleContaining(docs, '.docs-row-surface[role="button"]', "top");
    expect(/cursor:\s*pointer/.test(surface.body)).toBe(true);
  });

  it("carries the row name's weight, not a size, and on all six tabs", () => {
    // The title is byte-identical to the five sibling tabs, History and Tools:
    // `.list-row-name`, mono, --fs-sm, weight 400. So it is not small, it is
    // light — and a size bump on this tab alone would make Workflows disagree
    // with the five tabs it shares a tab bar with. `.docs-row` is exactly those
    // six, and --fw-medium at the same size is what .mcp-row-name already does.
    const rule = ruleContaining(docs, ".docs-row .list-row-name", "top");
    expect(/font-weight:\s*var\(--fw-medium\)/.test(rule.body)).toBe(true);
    expect(/font-size/.test(rule.body)).toBe(false);
    expect(
      /font-weight:\s*var\(--fw-medium\)/.test(ruleBody(loadCSS("60-mcp.css"), ".mcp-row-name")),
    ).toBe(true);
  });
});

describe("the muted classes are gone rather than defined", () => {
  // `.text-muted` and `.text-sm` were on seven elements across four files and
  // declared in no stylesheet, so "Not scheduled" rendered at full primary ink
  // and outshouted the `.is-scheduled` accent meant to distinguish it. They were
  // REPLACED rather than defined: the utilities layer ranks below every
  // unlayered feature slice, so a `.text-muted` there would have lost to the
  // component rules at some of those very sites (`.recipe-inputs-note` already
  // sets colour unlayered) and won at others — a class that works in some places
  // is worse than one that works nowhere. Each site takes its ink from its own
  // component rule instead.

  it("names neither class anywhere in authored source", () => {
    const offenders: string[] = [];
    for (const [path, text] of Object.entries(authoredSource)) {
      // The test file naming the classes in its own prose is not a use site.
      if (path.endsWith("recipes.test.ts")) {
        continue;
      }
      if (/\btext-muted\b|\btext-sm\b/.test(text)) {
        offenders.push(path.replace(/^\.\//, ""));
      }
    }
    expect(offenders).toEqual([]);
  });

  it("mutes the dormant schedule line and lets a live one take the accent", () => {
    const docs = loadCSS("28-docs.css");
    expect(/color:\s*var\(--c-text-tertiary\)/.test(ruleBody(docs, ".recipe-sched-summary"))).toBe(
      true,
    );
    // Higher specificity, so the accent still wins for a live schedule.
    expect(
      /color:\s*var\(--c-accent\)/.test(ruleBody(docs, ".recipe-sched-summary.is-scheduled")),
    ).toBe(true);
  });

  it("gives every former use site a component rule that carries its ink", () => {
    // `.run-id` and `.run-output-empty` were this page's own; the page renders the
    // run CARD now, so their equivalents live with the component. Both are still
    // tertiary ink — that is what the muted-class sweep is checking — they are just
    // in the stylesheet that owns the element.
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(
        ruleBody(loadCSS("27-run-card.css"), ".run-output-val-empty"),
      ),
    ).toBe(true);
    // The run PAGE's own note moved to the exec view, which is a different component
    // with its own vocabulary — the page renders a delegated-execution view now rather
    // than a variant of the transcript's card.
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(
        ruleBody(loadCSS("31-exec-view.css"), ".ev-d-empty"),
      ),
    ).toBe(true);
    // The History search note moved with its box: the page search boxes are one
    // popup now (24-find.css `.page-find`), so `.hist-search-note` is gone and
    // `.page-find-note` carries the ink for all four of them.
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(ruleBody(loadCSS("24-find.css"), ".page-find-note")),
    ).toBe(true);
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(
        ruleBody(loadCSS("19-files.css"), ".fb-search-note"),
      ),
    ).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// The filter, which this tab did not have.
//
// The configuration browser's box was HIDDEN on this tab, on the reasoning that
// Workflows is RPC-sourced and escapes here before any docs logic runs — true
// about where the rows come from, and not the same claim as "nothing to filter".
// A recipe has a name, a description, a source and declared inputs, so the box
// reaches this panel now instead of hiding from it.
// ---------------------------------------------------------------------------

describe("the filter", () => {
  it("narrows by name, case-insensitively", async () => {
    recipesReply = { recipes: [recipe("goal"), recipe("triage")] };
    expect(names(await render())).toEqual(["goal", "triage"]);
    expect(names(await render("GOA"))).toEqual(["goal"]);
  });

  it("reaches a description, a source and a declared input name", async () => {
    recipesReply = {
      recipes: [
        { name: "one", source: "bundled://one", description: "reviews a pull request" },
        { name: "two", source: "workspace/.kiro/flows/deploy.workflow.json" },
        { name: "three", source: "bundled://three", inputs: { branch: "string" } },
      ],
    };
    expect(names(await render("pull request"))).toEqual(["one"]);
    expect(names(await render("deploy.workflow"))).toEqual(["two"]);
    expect(names(await render("branch"))).toEqual(["three"]);
  });

  it("matches the badge a bundled row DISPLAYS", async () => {
    // Same rule docs.ts applies to its own badges: a reader types at what they can
    // see.
    recipesReply = {
      recipes: [
        { name: "one", source: "bundled://one", built_in: true },
        { name: "two", source: "b://two" },
      ],
    };
    expect(names(await render("bundled"))).toEqual(["one"]);
  });

  it("cannot reach the node PLAN, which is raw JSON nobody types at", async () => {
    // Folding it in would match on punctuation and internal key names, so the box
    // would be answering a different question than it appears to ask.
    recipesReply = {
      recipes: [
        {
          name: "one",
          source: "bundled://one",
          plan: JSON.stringify({ nodeId: "n1", agentName: "reviewer" }),
        } as RecipesResponse["recipes"][0],
      ],
    };
    expect(names(await render("nodeId"))).toEqual([]);
    expect(names(await render(""))).toEqual(["one"]);
  });

  it("says NO MATCHES rather than claiming there are no workflows", async () => {
    // "No workflows available." under an active filter is the same lie docs.ts
    // records for its category text: they exist, they are one keystroke away.
    recipesReply = { recipes: [recipe("goal")] };
    const filtered = await render("zzzz");
    expect(filtered.textContent).toContain("No workflows match the filter");
    expect(filtered.textContent).not.toContain("No workflows available");
    recipesReply = { recipes: [] };
    expect((await render()).textContent).toContain("No workflows available");
  });

  it("reports its counts on every repaint, not only on the fetch", async () => {
    // The note describes what is on screen, so whichever caller changed what is on
    // screen owes the update — the run poll and the schedules fetch repaint too.
    const seen: { total: number; shown: number }[] = [];
    setRecipeCountsListener((c) => {
      seen.push(c);
    });
    recipesReply = { recipes: [recipe("goal"), recipe("triage")] };
    await render("goal");
    expect(seen.at(-1)).toEqual({ total: 2, shown: 1 });
    setRecipeCountsListener(() => undefined);
  });

  it("keeps a row's click bound to the recipe it names, not to the filtered index", async () => {
    // recipeRow resolves at CLICK time against the UNFILTERED list, which is what
    // survives reconcile keeping a row across a keystroke.
    recipesReply = { recipes: [recipe("goal"), recipe("triage")] };
    const panel = await render("triage");
    buttonFor(panel, "bundled://triage")?.click();
    expect(dispatched.filter((d) => d.name === "launch")).toHaveLength(1);
    expect(dispatched.at(-1)?.args).toMatchObject({ source: "bundled://triage" });
  });
});
