// The /docs metadata filter (item 7's configuration-browser half).
//
// A FILTER, not a search: everything it matches on is already in memory, so there
// is no request, no truncation to report and no debounce worth waiting for. What
// it can REACH is the bound worth stating — a name, a description, a path, an
// inclusion mode, an agent's model and tool names, a hook's trigger — and never a
// document's BODY, which is the file browser's recursive grep one view away.
//
// A separate file from docs.test.ts on purpose: `initDocsView` is guarded by a
// module `inited` flag, so a second block in that file would open the page after
// the first had already claimed the flag and would then be asserting against a
// filter that was never built. A fresh module graph is the honest fixture.
import { describe, it, expect, vi, beforeAll } from "vitest";
import type { PageFind } from "./find-registry.js";

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(), apiGetTyped: vi.fn() }));
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));
vi.mock("./tabs.js", () => ({
  setDocsTab: vi.fn(),
  // No onShow argument any more — the tab factory reaches `loadDocsView` through
  // its own lazy import. This suite drives the page directly, so the toggle only
  // has to resolve.
  toggleDocsView: vi.fn(() => Promise.resolve()),
}));
// `onBus` as well as `onSSE`: the Workflows tab hands its panel to recipes.ts,
// which subscribes to the run bus, and one of the cases below switches to it.
vi.mock("./bus.js", () => ({
  onSSE: vi.fn(() => () => undefined),
  onBus: vi.fn(() => () => undefined),
  BUS_RUNS_CHANGED: "runs:changed",
}));
vi.mock("./git-status-store.js", () => ({
  initGitStatusStore: vi.fn(),
  onGitStatusChange: vi.fn(() => () => undefined),
  statusFor: vi.fn(() => ""),
}));
vi.mock("./actions/hooks.js", () => ({ setHookEnabled: { dispatch: vi.fn() } }));
vi.mock("./recipes.js", () => ({
  // The Workflows panel owns its own rows, so the page hands it the filter and
  // the panel reports back through the listener. Both halves are stubbed here so
  // the assertions below can watch the handoff.
  renderRecipesPanel: vi.fn((c: HTMLElement, filter?: string) => {
    c.replaceChildren();
    recipeFilter = filter ?? "";
  }),
  setRecipeCountsListener: vi.fn((fn: (c: { total: number; shown: number }) => void) => {
    reportCounts = fn;
  }),
}));

/** The filter the Workflows panel was last rendered with. */
let recipeFilter = "";
/** The panel's way back to the page's note. */
let reportCounts: ((c: { total: number; shown: number }) => void) | null = null;

type DocRecord = Record<string, unknown>;

let filterInput: HTMLInputElement;
let setDocs: (d: DocRecord[]) => void;
let render: () => void;
let forceTab: (t: string) => void;
let find: PageFind;

beforeAll(async () => {
  document.body.innerHTML = `
    <div id="docs-view">
      <nav id="docs-tab-bar">
        <button type="button" data-docs-tab="steering"></button>
        <button type="button" data-docs-tab="skills"></button>
        <button type="button" data-docs-tab="agents"></button>
        <button type="button" data-docs-tab="specs"></button>
        <button type="button" data-docs-tab="hooks"></button>
        <button type="button" data-docs-tab="workflows"></button>
      </nav>
      <select id="docs-tab-select"></select>
      <div data-docs-panel="steering" class="list-container docs-panel"></div>
      <div data-docs-panel="skills" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="agents" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="specs" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="hooks" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="workflows" class="list-container docs-panel hidden"></div>
    </div>`;
  const { apiGet, apiGetTyped } = await import("./api-client.js");
  vi.mocked(apiGet).mockResolvedValue({ docs: [] });
  vi.mocked(apiGetTyped).mockResolvedValue({ hooks: [] });

  const mod = await import("./docs.js");
  // The page's own loader, not `showDocsView`: that one toggles the TAB, which is a
  // round trip and registers no find.
  mod.loadDocsView("steering");
  setDocs = mod._setDocsForTest as unknown as (d: DocRecord[]) => void;
  render = mod._renderActiveForTest;
  forceTab = mod.forceDocsTab as unknown as (t: string) => void;
  // The page hands its find to the leaf registry rather than exporting a focuser,
  // so this is how the box is reached — the same door Ctrl-F and the toolbar
  // magnifier use.
  const { pageFind } = await import("./find-registry.js");
  const registered = pageFind("docs");
  if (registered === undefined) {
    throw new Error("the docs page registered no find");
  }
  find = registered;
  // A POPUP: nothing is built until it is opened, so the field does not exist yet.
  find.open();
  filterInput = document.getElementById("docs-filter-input") as HTMLInputElement;
});

function panel(name: string): HTMLElement {
  return document.querySelector(`[data-docs-panel="${name}"]`) as HTMLElement;
}

function names(name = "steering"): string[] {
  return [...panel(name).querySelectorAll(".list-row-name")].map((e) => e.textContent ?? "");
}

/** Type and apply. Enter rather than the debounce: the query is synchronous (the
 *  inventory is already here), so the shell renders in this same tick. */
function type(value: string): void {
  filterInput.value = value;
  filterInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", cancelable: true }));
}

const steering = (over: DocRecord = {}): DocRecord => ({
  category: "steering",
  name: "alpha",
  path: "workspace/.kiro/steering/alpha.md",
  ...over,
});

describe("the box itself", () => {
  it("is a role=search landmark, and the same popup the transcript uses", () => {
    const region = document.getElementById("docs-filter");
    expect(region?.getAttribute("role")).toBe("search");
    expect(region?.getAttribute("aria-label")).toBe("Filter documents");
    // The shared skin plus the primitive's hook: one control, one position, on
    // every page that has a search box.
    expect(region?.className).toContain("page-find");
    expect(region?.className).toContain("search-pop");
    expect(region?.className).toContain("uip-popup");
  });

  it("carries the shared field attributes rather than its own spelling of them", () => {
    expect(filterInput.getAttribute("autocomplete")).toBe("off");
    expect(filterInput.getAttribute("autocapitalize")).toBe("off");
    expect(filterInput.getAttribute("spellcheck")).toBe("false");
    expect(filterInput.getAttribute("enterkeyhint")).toBe("search");
    // NOT type=search: the platform's own clear affordance belongs on a permanent
    // box, and this one carries its own ×. Two clear controls a thumb-width apart
    // doing different things is worse than one.
    expect(filterInput.type).toBe("text");
  });

  it("has NO match-case toggle, because every filter in the app folds both sides", () => {
    // The query AND the row it is matched against, so a toggle would be wired to
    // nothing.
    expect(document.querySelector('#docs-filter [aria-label="Match case"]')).toBeNull();
  });

  it("has a × now, because there is something to close, and it says FILTER", () => {
    expect(document.querySelector('#docs-filter [aria-label="Close filter"]')).not.toBeNull();
  });

  it("carries the FUNNEL, because it only narrows rows already here", () => {
    // The magnifier is for a box that reaches past the page — History's, which
    // reads every chat file on disk. This one cannot see a document's body.
    expect(document.querySelector("#docs-filter .page-find-icon polygon")).not.toBeNull();
    expect(document.querySelector("#docs-filter .page-find-icon circle")).toBeNull();
  });
});

describe("what it matches", () => {
  it("narrows by name, case-insensitively", () => {
    setDocs([steering({ name: "Alpha" }), steering({ name: "beta", path: "b.md" })]);
    render();
    expect(names()).toEqual(["Alpha", "beta"]);
    type("ALP");
    expect(names()).toEqual(["Alpha"]);
    type("");
  });

  it("reaches a description, a path and an inclusion mode, not only the name", () => {
    setDocs([
      steering({ name: "one", description: "about redis caching", path: "one.md" }),
      steering({ name: "two", path: "workspace/.kiro/steering/deploy-notes.md" }),
      steering({ name: "three", inclusion: "fileMatch", path: "three.md" }),
    ]);
    render();
    type("redis");
    expect(names()).toEqual(["one"]);
    type("deploy-notes");
    expect(names()).toEqual(["two"]);
    type("filematch");
    expect(names()).toEqual(["three"]);
    type("");
  });

  it("reaches an agent's model and its tool names", () => {
    forceTab("agents");
    setDocs([
      {
        category: "agent",
        name: "reviewer",
        path: "r.md",
        model: "sonnet-4",
        tools: ["grepSearch", "readFile"],
      },
      {
        category: "agent",
        name: "planner",
        path: "p.md",
        model: "haiku",
        tools: ["listDirectory"],
      },
    ]);
    render();
    expect(names("agents")).toEqual(["reviewer", "planner"]);
    type("sonnet");
    expect(names("agents")).toEqual(["reviewer"]);
    type("listdirectory");
    expect(names("agents")).toEqual(["planner"]);
    type("");
    forceTab("steering");
    render();
  });

  it("cannot reach a document's BODY, which is the bound worth stating", () => {
    // The inventory carries front-matter and nothing else; searching bodies is the
    // file browser's recursive grep. A filter that appeared to search text it never
    // reads would be the silent miss chat-search.ts exists to prevent.
    setDocs([steering({ name: "alpha", description: "short summary" })]);
    render();
    type("some sentence deep inside the file");
    expect(names()).toEqual([]);
    type("");
  });
});

describe("what it says", () => {
  it("reports how much of the tab it is showing, and stays silent with no filter", () => {
    setDocs([steering({ name: "a", path: "a.md" }), steering({ name: "b", path: "b.md" })]);
    render();
    const note = document.getElementById("docs-filter-note");
    type("");
    expect(note?.textContent, "a count restating the whole list is noise").toBe("");
    type("a");
    expect(note?.textContent).toBe("1 of 2 shown.");
    type("");
  });

  it("says NO MATCHES rather than the category's empty text", () => {
    // "No steering docs in .kiro/steering/." is a lie when the docs are one
    // keystroke away. git-changes-tab.ts draws the same distinction.
    setDocs([steering({ name: "alpha" })]);
    render();
    type("zzzz");
    expect(panel("steering").textContent).toContain("No documents match the filter");
    expect(panel("steering").textContent).not.toContain(".kiro/steering/");
    type("");
    expect(names()).toEqual(["alpha"]);
  });

  it("keeps the category's own empty text when nothing is filtered", () => {
    setDocs([]);
    render();
    type("");
    expect(panel("steering").textContent).toContain("No steering docs in .kiro/steering/.");
  });
});

describe("Workflows, the tab that used to be excluded", () => {
  // The box was HIDDEN there and Ctrl-F declined, on the reasoning that the tab is
  // RPC-sourced and escapes to recipes.ts before any docs logic runs. True about
  // where the rows come from, and not the same claim as "nothing to filter" — a
  // recipe has a name, a description, a source and declared inputs. The filter
  // reaches the panel now instead of hiding from it.
  it("hands its filter through to the panel that owns those rows", () => {
    forceTab("workflows");
    type("goal");
    expect(recipeFilter).toBe("goal");
    type("");
    expect(recipeFilter).toBe("");
    forceTab("steering");
    render();
  });

  it("accepts Ctrl-F there, and the box is the same one", () => {
    forceTab("workflows");
    render();
    expect(find.open()).toBe(true);
    expect(document.activeElement).toBe(filterInput);
    forceTab("steering");
    render();
  });

  it("takes the panel's own counts for the note, so it reads the same on six tabs", () => {
    forceTab("workflows");
    type("goal");
    reportCounts?.({ total: 9, shown: 2 });
    expect(document.getElementById("docs-filter-note")?.textContent).toBe("2 of 9 shown.");
    type("");
    forceTab("steering");
    render();
  });
});

describe("dismissal", () => {
  it("closes on Escape, and the CLOSE is what lifts the filter", async () => {
    // The rule a hidden box needs and a permanent one did not: a popup that closed
    // holding `alp` would leave the page showing one of two rows with nothing on
    // screen saying why, and the way back would be a box the reader has no reason
    // to think is still armed.
    setDocs([steering({ name: "alpha" }), steering({ name: "beta", path: "b.md" })]);
    render();
    find.open();
    type("alp");
    expect(names()).toEqual(["alpha"]);
    filterInput.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    // The popup's leave lifecycle hides the panel on a transitionend (or its
    // 400ms fallback), and focus only leaves the field once it does — a real
    // browser does not move focus on the same tick the key was handled.
    await vi.waitFor(() => {
      expect(find.focused()).toBe(false);
    });
    expect(filterInput.value).toBe("");
    expect(names()).toEqual(["alpha", "beta"]);
  });

  it("lifts the filter on the toolbar button's second click too", () => {
    setDocs([steering({ name: "alpha" }), steering({ name: "beta", path: "b.md" })]);
    render();
    find.open();
    type("alp");
    expect(names()).toEqual(["alpha"]);
    find.toggle();
    expect(names()).toEqual(["alpha", "beta"]);
  });
});
